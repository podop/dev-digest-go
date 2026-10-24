// Package postgres reads the data of the leaderboard context with GORM. Nothing here writes: the
// store runs SELECTs over repos, pull_requests, reviews, findings, agents and agent_runs with
// row types declared in this package, every query scoped to one workspace. A request costs at most
// nine set-based queries (RepoScope 1, WindowFacts 4 + one per shard of repositories, at most
// four shards) and no query depends on the number of PRs
// or findings: findings are counted and ranked in SQL (queries.go). Every method takes its
// handle from platformpg.Conn, so a store called inside a transaction joins the caller's
// transaction.
package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"sync"
	"time"

	"gorm.io/gorm"

	"dev-digest-go/backend/internal/leaderboard/app"
	"dev-digest-go/backend/internal/leaderboard/domain"
	platformpg "dev-digest-go/backend/internal/platform/postgres"
)

// Store is the GORM-backed implementation of app.Reader.
type Store struct {
	db *gorm.DB
}

var _ app.Reader = (*Store)(nil)

// NewStore builds a Store on a GORM handle.
func NewStore(db *gorm.DB) *Store { return &Store{db: db} }

func (s *Store) conn(ctx context.Context) *gorm.DB { return platformpg.Conn(ctx, s.db) }

var uuidRe = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// RepoScope implements app.Reader.
func (s *Store) RepoScope(ctx context.Context, workspaceID, repo string) ([]domain.RepoRef, error) {
	var query *gorm.DB
	if repo == domain.RepoAll {
		query = repoAll(s.conn(ctx), workspaceID)
	} else {
		if !uuidRe.MatchString(repo) {
			return nil, fmt.Errorf("repo %q: %w", repo, domain.ErrNotFound)
		}
		query = repoOne(s.conn(ctx), workspaceID, repo)
	}
	rows, err := query.Rows()
	out := []domain.RepoRef{}
	if err := eachRow(rows, err, func(rows *sql.Rows) error {
		r, err := scanRepo(rows)
		out = append(out, r)
		return err
	}); err != nil {
		return nil, fmt.Errorf("select repos: %w", err)
	}
	if repo != domain.RepoAll && len(out) == 0 {
		return nil, fmt.Errorf("repo %s: %w", repo, domain.ErrNotFound)
	}
	return out, nil
}

// findingShards is the number of parts the repositories of a request are split into for the
// finding statistics (the heaviest query: it counts and ranks every finding of the window). The
// parts are read in parallel, so the request takes as long as the biggest part, and a smaller
// part is cheaper per finding (its working set stays in memory). Four parts were measured ~8 %
// faster than three at NFR1 on 8 cores, and six no faster than four (the pool and the cores are
// the limit). A pull request belongs to one repository, so counts, top findings and hot files of
// the parts are disjoint and simply add up (domain.HotFiles orders and caps the union of the hot
// files).
const findingShards = 4

// splitRepos deals ids out to at most n parts in turn, none of them empty.
func splitRepos(ids []string, n int) [][]string {
	n = min(n, len(ids))
	parts := make([][]string, n)
	for i, id := range ids {
		parts[i%n] = append(parts[i%n], id)
	}
	return parts
}

// windowRead is one query of WindowFacts; it appends what it reads to the slices it closes over.
type windowRead struct {
	name string
	run  func() error
}

// WindowFacts implements app.Reader: pulls, reviews, runs and open criticals over
// [win.ReadFrom(), win.To), and the finding statistics over each shard of the repositories (at
// most findingShards). They are independent reads, so they are sent concurrently on separate
// pool connections (the request takes as long as the slowest one, not their sum); inside a
// transaction, which is one connection, they run one after the other. The number of statements
// is at most 4 + findingShards, whatever the number of pull requests, reviews and findings.
func (s *Store) WindowFacts(ctx context.Context, workspaceID string, repoIDs []string, win domain.Window) (domain.Facts, error) {
	facts := domain.Facts{From: win.ReadFrom(), To: win.To}
	if len(repoIDs) == 0 {
		return facts, nil
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	args := queryArgs(workspaceID, repoIDs, facts.From, facts.To, win.From)
	db := s.conn(ctx)

	var (
		pulls    = []domain.PullFact{}
		reviews  = []domain.ReviewFact{}
		runs     = []domain.RunFact{}
		critical = map[string]int{}
	)
	reads := []windowRead{
		{"window pulls", func() error {
			rows, err := db.Raw(qWindowPulls, args...).Rows()
			return eachRow(rows, err, func(rows *sql.Rows) error {
				p, err := scanPull(rows)
				pulls = append(pulls, p)
				return err
			})
		}},
		{"window reviews", func() error {
			rows, err := windowReviews(db, workspaceID, repoIDs, facts.From, facts.To).Rows()
			return eachRow(rows, err, func(rows *sql.Rows) error {
				r, err := scanReview(rows)
				reviews = append(reviews, r)
				return err
			})
		}},
		{"window runs", func() error {
			rows, err := windowRuns(db, workspaceID, repoIDs, facts.From, facts.To, win.From).Rows()
			return eachRow(rows, err, func(rows *sql.Rows) error {
				r, err := scanRun(rows)
				runs = append(runs, r)
				return err
			})
		}},
		{"open criticals", func() error {
			rows, err := db.Raw(qOpenCritical, args...).Rows()
			return eachRow(rows, err, func(rows *sql.Rows) error {
				prID, n, err := scanOpenCritical(rows)
				critical[prID] = n
				return err
			})
		}},
	}
	shards := splitRepos(repoIDs, findingShards)
	stats := make([]statRows, len(shards))
	for i, part := range shards {
		shardArgs := queryArgs(workspaceID, part, facts.From, facts.To, win.From)
		shardArgs = append(shardArgs, sql.Named("top", domain.TopFindingsLimit), sql.Named("limit", domain.HotFilesLimit))
		reads = append(reads, windowRead{"finding statistics", func() error {
			rows, err := db.Raw(qFindingStats, shardArgs...).Rows()
			return eachRow(rows, err, stats[i].scan)
		}})
	}

	errs := make([]error, len(reads))
	if _, inTx := db.Statement.ConnPool.(gorm.TxCommitter); inTx {
		for i, read := range reads {
			if errs[i] = read.run(); errs[i] != nil {
				break
			}
		}
	} else {
		var wg sync.WaitGroup
		for i, read := range reads {
			wg.Go(func() {
				if errs[i] = read.run(); errs[i] != nil {
					cancel() // the others are moot
				}
			})
		}
		wg.Wait()
	}
	if err := firstReadError(reads, errs); err != nil {
		return domain.Facts{}, err
	}

	for i := range pulls {
		pulls[i].OpenCritical = critical[pulls[i].PRID]
	}
	facts.Pulls, facts.Reviews, facts.Runs = pulls, reviews, runs
	facts.Counts, facts.TopFindings, facts.HotFiles = []domain.ReviewCounts{}, []domain.FindingFact{}, []domain.HotFile{}
	for _, part := range stats {
		facts.Counts = append(facts.Counts, part.counts...)
		facts.TopFindings = append(facts.TopFindings, part.tops...)
		facts.HotFiles = append(facts.HotFiles, part.hot...)
	}
	return facts, nil
}

// firstReadError returns the error of the first failed read in the order of the reads, not the
// first to finish: a failure cancels the other reads, which then fail with context.Canceled.
func firstReadError(reads []windowRead, errs []error) error {
	for i, err := range errs {
		if err != nil && !errors.Is(err, context.Canceled) {
			return fmt.Errorf("select %s: %w", reads[i].name, err)
		}
	}
	for i, err := range errs {
		if err != nil {
			return fmt.Errorf("select %s: %w", reads[i].name, err)
		}
	}
	return nil
}

func queryArgs(workspaceID string, repoIDs []string, from, to, split time.Time) []any {
	return []any{
		sql.Named("ws", workspaceID), sql.Named("repos", repoIDs),
		sql.Named("from", from), sql.Named("to", to), sql.Named("split", split),
	}
}

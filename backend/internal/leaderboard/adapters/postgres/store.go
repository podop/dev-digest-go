// Package postgres reads the data of the leaderboard context with GORM. Nothing here writes: the
// store runs SELECTs over repos, pull_requests, reviews, findings, agents and agent_runs with
// row types declared in this package, every query scoped to one workspace. A request costs at
// most eight set-based queries (RepoScope 1, WindowFacts 5) and no query depends on
// the number of PRs. Every method takes its handle from platformpg.Conn, so a store called inside
// a transaction joins the caller's transaction.
package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
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
	var rows []repoRow
	if repo == domain.RepoAll {
		if err := s.conn(ctx).Raw(qRepoAll, workspaceID).Scan(&rows).Error; err != nil {
			return nil, fmt.Errorf("select repos: %w", err)
		}
	} else {
		if !uuidRe.MatchString(repo) {
			return nil, fmt.Errorf("repo %q: %w", repo, domain.ErrNotFound)
		}
		if err := s.conn(ctx).Raw(qRepoOne, workspaceID, repo).Scan(&rows).Error; err != nil {
			return nil, fmt.Errorf("select repo: %w", err)
		}
		if len(rows) == 0 {
			return nil, fmt.Errorf("repo %s: %w", repo, domain.ErrNotFound)
		}
	}
	out := make([]domain.RepoRef, len(rows))
	for i, r := range rows {
		out[i] = domain.RepoRef{ID: r.ID, Owner: r.Owner, Name: r.Name, FullName: r.FullName, LastPolledAt: utcPtr(r.LastPolledAt)}
	}
	return out, nil
}

// WindowFacts implements app.Reader: five queries (pulls, reviews, findings, runs, open
// criticals) over [win.ReadFrom(), win.To).
func (s *Store) WindowFacts(ctx context.Context, workspaceID string, repoIDs []string, win domain.Window) (domain.Facts, error) {
	facts := domain.Facts{From: win.ReadFrom(), To: win.To}
	if len(repoIDs) == 0 {
		return facts, nil
	}
	args := queryArgs(workspaceID, repoIDs, facts.From, facts.To, win.From)
	db := s.conn(ctx)

	var pulls []pullRow
	if err := db.Raw(qWindowPulls, args...).Scan(&pulls).Error; err != nil {
		return domain.Facts{}, fmt.Errorf("select window pulls: %w", err)
	}
	var reviews []reviewRow
	if err := db.Raw(qWindowReviews, args...).Scan(&reviews).Error; err != nil {
		return domain.Facts{}, fmt.Errorf("select window reviews: %w", err)
	}
	var findings []findingRow
	if err := db.Raw(qWindowFindings, args...).Scan(&findings).Error; err != nil {
		return domain.Facts{}, fmt.Errorf("select window findings: %w", err)
	}
	var runs []runRow
	if err := db.Raw(qWindowRuns, args...).Scan(&runs).Error; err != nil {
		return domain.Facts{}, fmt.Errorf("select window runs: %w", err)
	}
	var critical []openCriticalRow
	if err := db.Raw(qOpenCritical, args...).Scan(&critical).Error; err != nil {
		return domain.Facts{}, fmt.Errorf("select open criticals: %w", err)
	}
	openCritical := make(map[string]int, len(critical))
	for _, c := range critical {
		openCritical[c.PRID] = c.N
	}

	facts.Pulls = make([]domain.PullFact, len(pulls))
	for i, p := range pulls {
		facts.Pulls[i] = domain.PullFact{
			PRID: p.ID, RepoID: p.RepoID, Repo: p.Repo, Number: p.Number, Title: p.Title, Author: p.Author,
			HeadSHA: p.HeadSHA, Additions: p.Additions, Deletions: p.Deletions, FilesCount: p.FilesCount,
			Status: p.Status, Draft: p.Draft, LastReviewedSHA: p.LastReviewedSHA,
			OpenedAt: utcPtr(p.OpenedAt), UpdatedAt: utcPtr(p.UpdatedAt),
			MergedAt: utcPtr(p.MergedAt), ClosedAt: utcPtr(p.ClosedAt), FirstReviewAt: utcPtr(p.FirstReviewAt),
			OpenCritical: openCritical[p.ID],
		}
	}
	facts.Reviews = make([]domain.ReviewFact, len(reviews))
	for i, r := range reviews {
		facts.Reviews[i] = domain.ReviewFact{
			ReviewID: r.ID, PRID: r.PRID, AgentID: deref(r.AgentID), AgentName: deref(r.AgentName),
			Score: r.Score, CreatedAt: r.CreatedAt.UTC(),
		}
	}
	facts.Findings = make([]domain.FindingFact, len(findings))
	for i, f := range findings {
		facts.Findings[i] = domain.FindingFact{
			FindingID: f.ID, ReviewID: f.ReviewID, File: f.File, StartLine: f.StartLine,
			Severity: f.Severity, Title: f.Title,
			AcceptedAt: utcPtr(f.AcceptedAt), DismissedAt: utcPtr(f.DismissedAt),
		}
	}
	facts.Runs = make([]domain.RunFact, len(runs))
	for i, r := range runs {
		facts.Runs[i] = domain.RunFact{PRID: r.PRID, RanAt: r.RanAt.UTC(), CostUSD: r.CostUSD}
	}
	return facts, nil
}

func queryArgs(workspaceID string, repoIDs []string, from, to, split time.Time) []any {
	return []any{
		sql.Named("ws", workspaceID), sql.Named("repos", repoIDs),
		sql.Named("from", from), sql.Named("to", to), sql.Named("split", split),
	}
}

func utcPtr(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	u := t.UTC()
	return &u
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

package postgres

// Row scanning. The heavy statements (a few thousand pull requests, ten thousand reviews, tens
// of thousands of statistics rows at NFR1) are read with database/sql's Rows and plain
// pointers, straight into domain facts, instead of GORM's struct Scan: GORM's reflection path
// costs ~1.1 µs per column value against ~0.4 µs, which at 560k values per request was ~0.6 s of
// CPU (profile of the first AC12 fix round).
//
// Instants are selected as microseconds since the Unix epoch (bigint): scanning a timestamptz
// column through database/sql and pgx costs ~10 µs per value, a bigint ~0.4 µs.

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"dev-digest-go/backend/internal/leaderboard/domain"
)

// eachRow calls scan for every row of rows (the result of a Rows() call) and closes it.
func eachRow(rows *sql.Rows, err error, scan func(*sql.Rows) error) error {
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		if err := scan(rows); err != nil {
			return err
		}
	}
	return rows.Err()
}

// instant converts a nullable epoch-microseconds column to a UTC time.
func instant(micros *int64) *time.Time {
	if micros == nil {
		return nil
	}
	t := time.UnixMicro(*micros).UTC()
	return &t
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// scanRepo reads one row of repoColumns.
func scanRepo(rows *sql.Rows) (domain.RepoRef, error) {
	var r domain.RepoRef
	var polled *int64
	var provider, host, webURL *string
	if err := rows.Scan(&r.ID, &r.Owner, &r.Name, &r.FullName, &polled, &provider, &host, &webURL); err != nil {
		return domain.RepoRef{}, err
	}
	r.LastPolledAt = instant(polled)
	r.Provider, r.Host, r.WebURL = deref(provider), deref(host), deref(webURL)
	return r, nil
}

// scanPull reads one row of qWindowPulls (the column order of pullColumns, then first_review_at).
func scanPull(rows *sql.Rows) (domain.PullFact, error) {
	var p domain.PullFact
	var opened, updated, merged, closed, firstReview *int64
	if err := rows.Scan(&p.PRID, &p.RepoID, &p.Repo, &p.Number, &p.Title, &p.Author, &p.HeadSHA, &p.LastReviewedSHA,
		&p.Additions, &p.Deletions, &p.FilesCount, &p.Status, &p.Draft,
		&opened, &updated, &merged, &closed, &firstReview); err != nil {
		return domain.PullFact{}, err
	}
	p.OpenedAt, p.UpdatedAt, p.MergedAt, p.ClosedAt, p.FirstReviewAt =
		instant(opened), instant(updated), instant(merged), instant(closed), instant(firstReview)
	return p, nil
}

// scanReview reads one row of windowReviews. The agent name is NULL for a deleted agent.
func scanReview(rows *sql.Rows) (domain.ReviewFact, error) {
	var r domain.ReviewFact
	var agentID, agentName *string
	var created int64
	if err := rows.Scan(&r.ReviewID, &r.PRID, &agentID, &agentName, &r.Score, &created); err != nil {
		return domain.ReviewFact{}, err
	}
	r.AgentID, r.AgentName, r.CreatedAt = deref(agentID), deref(agentName), time.UnixMicro(created).UTC()
	return r, nil
}

// scanRun reads one row of windowRuns: the runs of a PR in one half, summed.
func scanRun(rows *sql.Rows) (domain.RunFact, error) {
	var r domain.RunFact
	var cur bool
	var ranAt int64
	if err := rows.Scan(&r.PRID, &cur, &ranAt, &r.CostUSD); err != nil {
		return domain.RunFact{}, err
	}
	r.RanAt = time.UnixMicro(ranAt).UTC()
	return r, nil
}

// scanOpenCritical reads one row of qOpenCritical.
func scanOpenCritical(rows *sql.Rows) (prID string, n int, err error) {
	err = rows.Scan(&prID, &n)
	return prID, n, err
}

// statRows collects the rows of qFindingStats into the facts they stand for.
type statRows struct {
	counts []domain.ReviewCounts
	tops   []domain.FindingFact
	hot    []domain.HotFile
}

// scan reads one row of qFindingStats and files it by kind.
func (s *statRows) scan(rows *sql.Rows) error {
	var (
		kind                                                 string
		reviewID, findingID, file, severity, title           *string
		repoID, repo, prs                                    *string
		startLine, findings, critical                        int
		accepted, dismissed                                  *int64
		allC, allW, allS, liveC, liveW, liveS, openCriticalN int
	)
	if err := rows.Scan(&kind, &reviewID, &findingID, &file, &startLine, &severity, &title, &accepted, &dismissed,
		&repoID, &repo, &prs, &allC, &allW, &allS, &liveC, &liveW, &liveS, &openCriticalN, &findings, &critical); err != nil {
		return err
	}
	switch kind {
	case kindCount:
		s.counts = append(s.counts, domain.ReviewCounts{
			ReviewID:     deref(reviewID),
			All:          domain.FindingCounts{Critical: allC, Warning: allW, Suggestion: allS},
			Live:         domain.FindingCounts{Critical: liveC, Warning: liveW, Suggestion: liveS},
			OpenCritical: openCriticalN,
		})
	case kindTop:
		s.tops = append(s.tops, domain.FindingFact{
			FindingID: deref(findingID), ReviewID: deref(reviewID), File: deref(file), StartLine: startLine,
			Severity: deref(severity), Title: deref(title), AcceptedAt: instant(accepted), DismissedAt: instant(dismissed),
		})
	case kindHot:
		var numbers []int
		if err := json.Unmarshal([]byte(deref(prs)), &numbers); err != nil {
			return fmt.Errorf("decode the PR numbers of hot file %q: %w", deref(file), err)
		}
		s.hot = append(s.hot, domain.HotFile{
			Path: deref(file), RepoID: deref(repoID), Repo: deref(repo), Findings: findings, Critical: critical, PRs: numbers,
		})
	default:
		return fmt.Errorf("unknown finding statistics row kind %q", kind)
	}
	return nil
}

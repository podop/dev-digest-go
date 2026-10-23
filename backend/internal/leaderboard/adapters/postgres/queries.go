package postgres

// The queries of the store: SELECT only, every statement scoped to one workspace on every table
// that has the column. A request runs at most eight of them in total: RepoScope 1, WindowFacts 5.
//
// The simple ones (repos, window reviews, window runs) use the GORM builder. Only three stay raw
// SQL, because the builder cannot express them (backend/AGENTS.md: Raw only for what the builder
// can't): qWindowFindings and qOpenCritical (a ROW_NUMBER() CTE joined back to findings) and
// qWindowPulls (a correlated MIN() subquery and an EXISTS predicate shared with qOpenCritical).

import (
	"time"

	"gorm.io/gorm"
)

// pullColumns is the column list of the window pull query.
const pullColumns = `p.id, p.repo_id, r.name AS repo, p.number, p.title, p.author, p.head_sha, p.last_reviewed_sha,
	p.additions, p.deletions, p.files_count, p.status, p.draft,
	p.opened_at, p.updated_at, p.merged_at, p.closed_at`

const repoColumns = "id, owner, name, full_name, last_polled_at"

// repoAll lists every repository of the workspace, ordered by full name in byte order (C
// collation, independent of the database locale), then by id.
func repoAll(db *gorm.DB, workspaceID string) *gorm.DB {
	return db.Table("repos").Select(repoColumns).
		Where("workspace_id = ?", workspaceID).
		Order(`full_name COLLATE "C"`).Order("id")
}

// repoOne selects one repository of the workspace (zero rows = not found). The caller has checked
// that repoID has the shape of a uuid, so the cast cannot fail.
func repoOne(db *gorm.DB, workspaceID, repoID string) *gorm.DB {
	return db.Table("repos").Select(repoColumns).
		Where("workspace_id = ? AND id = ?::uuid", workspaceID, repoID)
}

// windowPullPredicate selects the PRs of the range [@from, @to): a review of kind review created
// in it, OR a merge time in it, OR an opened time in it. It is written for a table aliased p.
const windowPullPredicate = `(
    EXISTS (SELECT 1 FROM reviews wr WHERE wr.pr_id = p.id AND wr.kind = 'review'
            AND wr.created_at >= @from AND wr.created_at < @to)
    OR (p.merged_at >= @from AND p.merged_at < @to)
    OR (p.opened_at >= @from AND p.opened_at < @to)
  )`

// qWindowPulls: the PRs of the range (windowPullPredicate). first_review_at is the first review
// ever (not limited to the range).
const qWindowPulls = `
SELECT ` + pullColumns + `,
	(SELECT MIN(rv.created_at) FROM reviews rv WHERE rv.pr_id = p.id AND rv.kind = 'review') AS first_review_at
FROM pull_requests p
JOIN repos r ON r.id = p.repo_id AND r.workspace_id = p.workspace_id
WHERE p.workspace_id = @ws AND p.repo_id IN @repos AND ` + windowPullPredicate

// windowReviews: every review of kind review created in [from, to) (all of them belong to a PR
// of qWindowPulls). The agent name is NULL for a deleted agent.
func windowReviews(db *gorm.DB, workspaceID string, repoIDs []string, from, to time.Time) *gorm.DB {
	return db.Table("reviews rv").
		Select("rv.id, rv.pr_id, rv.agent_id, a.name AS agent_name, rv.score, rv.created_at").
		Joins("JOIN pull_requests p ON p.id = rv.pr_id AND p.workspace_id = rv.workspace_id").
		Joins("LEFT JOIN agents a ON a.id = rv.agent_id").
		Where("rv.workspace_id = ? AND p.repo_id IN ? AND rv.kind = 'review'", workspaceID, repoIDs).
		Where("rv.created_at >= ? AND rv.created_at < ?", from, to)
}

// qWindowFindings: the findings (every kind) of the two snapshots: the newest review of kind
// review per (PR, agent) created in [@split, @to) and the newest per (PR, agent) created in
// [@from, @split) (the previous window; with @split = @from it is empty). PARTITION BY treats the
// NULL agent_id (deleted agents) as one partition, like the domain's snapshot.
const qWindowFindings = `
WITH ranked AS (
  SELECT rv.id,
         ROW_NUMBER() OVER (PARTITION BY rv.pr_id, rv.agent_id, (rv.created_at >= @split)
                            ORDER BY rv.created_at DESC, rv.id DESC) AS rn
  FROM reviews rv
  JOIN pull_requests p ON p.id = rv.pr_id AND p.workspace_id = rv.workspace_id
  WHERE rv.workspace_id = @ws AND p.repo_id IN @repos AND rv.kind = 'review'
    AND rv.created_at >= @from AND rv.created_at < @to
)
SELECT f.id, f.review_id, f.file, f.start_line, f.severity, f.title, f.accepted_at, f.dismissed_at
FROM ranked
JOIN findings f ON f.review_id = ranked.id
WHERE ranked.rn = 1`

// windowRuns: the agent runs of a PR of the filter that ran in [from, to), whatever their status.
func windowRuns(db *gorm.DB, workspaceID string, repoIDs []string, from, to time.Time) *gorm.DB {
	return db.Table("agent_runs ar").
		Select("ar.pr_id, ar.ran_at, ar.cost_usd").
		Joins("JOIN pull_requests p ON p.id = ar.pr_id AND p.workspace_id = ar.workspace_id").
		Where("ar.workspace_id = ? AND p.repo_id IN ?", workspaceID, repoIDs).
		Where("ar.ran_at >= ? AND ar.ran_at < ?", from, to)
}

// qOpenCritical counts, per PR of the range (windowPullPredicate, whatever its status), the
// CRITICAL findings (every kind) with neither accepted nor dismissed time in the PR's current
// reviews: the newest review of kind review per agent, ever. PARTITION BY treats the NULL
// agent_id (deleted agents) as one partition, so all deleted agents are one agent.
const qOpenCritical = `
WITH ranked AS (
  SELECT rv.id, rv.pr_id,
         ROW_NUMBER() OVER (PARTITION BY rv.pr_id, rv.agent_id ORDER BY rv.created_at DESC, rv.id DESC) AS rn
  FROM reviews rv
  JOIN pull_requests p ON p.id = rv.pr_id AND p.workspace_id = rv.workspace_id
  WHERE rv.workspace_id = @ws AND p.repo_id IN @repos AND rv.kind = 'review'
    AND ` + windowPullPredicate + `
)
SELECT ranked.pr_id, COUNT(*) AS n
FROM ranked
JOIN findings f ON f.review_id = ranked.id
WHERE ranked.rn = 1 AND f.severity = 'CRITICAL' AND f.accepted_at IS NULL AND f.dismissed_at IS NULL
GROUP BY ranked.pr_id`

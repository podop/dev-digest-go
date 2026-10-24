package postgres

// The queries of the store: SELECT only, every statement scoped to one workspace on every table
// that has the column. A request runs at most nine of them in total: RepoScope 1, WindowFacts 4
// (pulls, reviews, runs, open criticals) plus the finding statistics (finding counts, top findings,
// hot files: one statement per shard of repositories, at most four, see findingShards); the store
// sends those of WindowFacts concurrently.
//
// The simple ones (repos, window reviews, window runs) use the GORM builder. The others stay raw
// SQL, because the builder cannot express them (backend/AGENTS.md: Raw only for what the
// builder can't): the finding statistics (a ROW_NUMBER() CTE of the snapshot reviews joined to
// findings, a LATERAL top-N per PR, FETCH FIRST ... WITH TIES), qOpenCritical, and qWindowPulls (a
// correlated MIN() subquery and an EXISTS predicate shared with qOpenCritical).
//
// Findings never reach Go one by one: a busy workspace has ~100k of them in a 30-day window and
// the first version of this store read them all (3.6 s of the 4.9 s of AC12). The finding queries
// count and rank in SQL, so Go receives O(snapshot reviews) rows. Every rule that is expressed in
// SQL is also in the domain, with a comment on both sides: the triage state of a finding, the top
// findings order and cap (domain.topFindings, domain.TopFindingsLimit) and the hot files order
// and cap (domain.HotFiles, domain.HotFilesLimit).
//
// Instants are selected as microseconds since the Unix epoch (bigint), see rows.go.

import (
	"time"

	"gorm.io/gorm"
)

// pullColumns is the column list of the window pull query.
const pullColumns = `p.id, p.repo_id, r.name AS repo, p.number, p.title, p.author, p.head_sha, p.last_reviewed_sha,
	p.additions, p.deletions, p.files_count, p.status, p.draft,
	(EXTRACT(EPOCH FROM p.opened_at) * 1000000)::bigint AS opened_at, (EXTRACT(EPOCH FROM p.updated_at) * 1000000)::bigint AS updated_at,
	(EXTRACT(EPOCH FROM p.merged_at) * 1000000)::bigint AS merged_at, (EXTRACT(EPOCH FROM p.closed_at) * 1000000)::bigint AS closed_at`

const repoColumns = "id, owner, name, full_name, (EXTRACT(EPOCH FROM last_polled_at) * 1000000)::bigint AS last_polled_at"

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
	(EXTRACT(EPOCH FROM (SELECT MIN(rv.created_at) FROM reviews rv WHERE rv.pr_id = p.id AND rv.kind = 'review')) * 1000000)::bigint AS first_review_at
FROM pull_requests p
JOIN repos r ON r.id = p.repo_id AND r.workspace_id = p.workspace_id
WHERE p.workspace_id = @ws AND p.repo_id IN @repos AND ` + windowPullPredicate

// windowReviews: every review of kind review created in [from, to) (all of them belong to a PR
// of qWindowPulls). The agent name is NULL for a deleted agent.
func windowReviews(db *gorm.DB, workspaceID string, repoIDs []string, from, to time.Time) *gorm.DB {
	return db.Table("reviews rv").
		Select("rv.id, rv.pr_id, rv.agent_id, a.name AS agent_name, rv.score, (EXTRACT(EPOCH FROM rv.created_at) * 1000000)::bigint AS created_at").
		Joins("JOIN pull_requests p ON p.id = rv.pr_id AND p.workspace_id = rv.workspace_id").
		Joins("LEFT JOIN agents a ON a.id = rv.agent_id").
		Where("rv.workspace_id = ? AND p.repo_id IN ? AND rv.kind = 'review'", workspaceID, repoIDs).
		Where("rv.created_at >= ? AND rv.created_at < ?", from, to)
}

// snapshotCTE selects the reviews of kind review of the range [@from, @to) and numbers them so
// that rn = 1 marks the snapshot reviews: the newest review per (PR, agent) created in
// [@split, @to) (cur = true) and the newest per (PR, agent) created in [@from, @split) (cur =
// false; with @split = @from that half is empty). PARTITION BY treats the NULL agent_id (deleted
// agents) as one partition, like the domain's snapshot (domain.windowSnapshot).
const snapshotCTE = `
snap AS (
  SELECT rv.id, rv.pr_id, p.repo_id, p.number, (rv.created_at >= @split) AS cur,
         ROW_NUMBER() OVER (PARTITION BY rv.pr_id, rv.agent_id, (rv.created_at >= @split)
                            ORDER BY rv.created_at DESC, rv.id DESC) AS rn
  FROM reviews rv
  JOIN pull_requests p ON p.id = rv.pr_id AND p.workspace_id = rv.workspace_id
  WHERE rv.workspace_id = @ws AND p.repo_id IN @repos AND rv.kind = 'review'
    AND rv.created_at >= @from AND rv.created_at < @to
)`

// findingClass numbers the (severity, triage state) classes of a finding for a table aliased f:
// class = 3 * severity + state, severity 0 CRITICAL, 1 WARNING, 2 SUGGESTION and state 0 open, 1
// accepted, 2 dismissed. Dismissed means dismissed_at is set, which wins over accepted_at (EC7);
// accepted means accepted_at is set; open means neither (domain.findingState). The severity
// column is constrained to the three values (findings_severity_chk). Ordering by the class is
// the ordering "CRITICAL before WARNING before SUGGESTION, then open before accepted before
// dismissed" of plan P13 (domain.topFindings).
const findingClass = `((CASE f.severity WHEN 'CRITICAL' THEN 0 WHEN 'WARNING' THEN 1 ELSE 2 END) * 3
    + (CASE WHEN f.dismissed_at IS NOT NULL THEN 2 WHEN f.accepted_at IS NOT NULL THEN 1 ELSE 0 END))`

// Kinds of the rows of qFindingStats.
const (
	kindCount = "count"
	kindTop   = "top"
	kindHot   = "hot"
)

// qFindingStats counts and ranks the findings of the snapshot reviews of the repositories @repos
// in ONE pass and returns three kinds of rows, told apart by `kind` (the columns a kind does not
// use are NULL or 0):
//
//   - 'count', one per snapshot review that has findings: the counts the domain folds into
//     health, risk and the severity totals (domain.ReviewCounts): review_id; all_*, every state;
//     live_*, the not dismissed ones (class mod 3 < 2); open_critical, class 0.
//   - 'top', per PR and snapshot half the first @top findings in the order of plan P13
//     (domain.topFindings): class (findingClass), then file (byte order, like Go strings), start
//     line and finding id (a uuid orders like its lower-case text). @top is
//     domain.TopFindingsLimit. Columns: review_id, finding_id, file, start_line, severity,
//     title, accepted_at, dismissed_at. A LATERAL subquery takes the findings of the snapshot
//     reviews of one PR and half (≈ 40) and keeps the first @top of them (a top-N heapsort of
//     a few dozen rows per PR instead of one sort of every finding of the shard). The agent
//     name is not selected: the domain takes it from the snapshot review (and names a deleted
//     agent).
//   - 'hot', the hottest files of the CURRENT half (cur) over the snapshot findings
//     (domain.HotFiles, FR14): findings with a path, dismissed ones excluded (class mod 3 < 2:
//     accepted still count), grouped by (repository, path); columns repo_id, repo, file,
//     findings, critical, prs (the distinct PR numbers ascending, JSON). The files are cut with
//     the order of domain.HotFiles up to its repository-name step: findings desc, critical
//     desc, path asc (byte order), repository name asc, FETCH FIRST @limit ROWS WITH TIES
//     (@limit = domain.HotFilesLimit), so that files tied on all four keys beyond the cap are
//     kept and domain.HotFiles decides among them by the remaining keys (PR numbers, repository
//     id). A pull request belongs to one repository, so the rows of disjoint repository sets
//     are disjoint and add up.
//
// One pass over the findings because they are what costs (~100k at NFR1): they are read once into
// a materialised CTE (narrow rows: no title, no ids) that the count and the hot-files
// aggregations share; the top findings re-read only the findings of one PR at a time.
const qFindingStats = `
WITH ` + snapshotCTE + `,
sr AS MATERIALIZED (
  SELECT id, pr_id, repo_id, number, cur FROM snap WHERE rn = 1
),
fx AS MATERIALIZED (
  SELECT sr.id AS review_id, sr.repo_id, sr.number, sr.cur, f.file, ` + findingClass + ` AS c
  FROM sr
  JOIN findings f ON f.review_id = sr.id
),
grp AS (
  SELECT repo_id, file, COUNT(*) AS findings, COUNT(*) FILTER (WHERE c / 3 = 0) AS critical
  FROM fx
  WHERE cur AND c % 3 < 2 AND file <> ''
  GROUP BY repo_id, file
),
hot_top AS (
  SELECT g.repo_id, r.name AS repo, g.file, g.findings, g.critical
  FROM grp g
  JOIN repos r ON r.id = g.repo_id
  ORDER BY g.findings DESC, g.critical DESC, g.file COLLATE "C", r.name COLLATE "C"
  FETCH FIRST @limit ROWS WITH TIES
),
hot AS (
  SELECT h.repo_id, h.repo, h.file, h.findings, h.critical, to_json(ARRAY_AGG(DISTINCT fx.number ORDER BY fx.number)) AS prs
  FROM hot_top h
  JOIN fx ON fx.repo_id = h.repo_id AND fx.file = h.file AND fx.cur AND fx.c % 3 < 2
  GROUP BY h.repo_id, h.repo, h.file, h.findings, h.critical
)
SELECT '` + kindCount + `' AS kind, review_id, NULL::uuid AS finding_id, NULL::text AS file, 0 AS start_line, NULL::text AS severity,
       NULL::text AS title, NULL::bigint AS accepted_at, NULL::bigint AS dismissed_at, NULL::uuid AS repo_id, NULL::text AS repo,
       NULL::text AS prs,
       COUNT(*) FILTER (WHERE c / 3 = 0)               AS all_critical,
       COUNT(*) FILTER (WHERE c / 3 = 1)               AS all_warning,
       COUNT(*) FILTER (WHERE c / 3 = 2)               AS all_suggestion,
       COUNT(*) FILTER (WHERE c / 3 = 0 AND c % 3 < 2) AS live_critical,
       COUNT(*) FILTER (WHERE c / 3 = 1 AND c % 3 < 2) AS live_warning,
       COUNT(*) FILTER (WHERE c / 3 = 2 AND c % 3 < 2) AS live_suggestion,
       COUNT(*) FILTER (WHERE c = 0)                   AS open_critical,
       0 AS findings, 0 AS critical
FROM fx
GROUP BY review_id
UNION ALL
SELECT '` + kindTop + `', t.review_id, t.id, t.file, t.start_line, t.severity, t.title,
       (EXTRACT(EPOCH FROM t.accepted_at) * 1000000)::bigint, (EXTRACT(EPOCH FROM t.dismissed_at) * 1000000)::bigint, NULL, NULL, NULL,
       0, 0, 0, 0, 0, 0, 0, 0, 0
FROM (SELECT pr_id, cur, ARRAY_AGG(id) AS review_ids FROM sr GROUP BY pr_id, cur) g
CROSS JOIN LATERAL (
  SELECT f.review_id, f.id, f.file, f.start_line, f.severity, f.title, f.accepted_at, f.dismissed_at
  FROM findings f
  WHERE f.review_id = ANY(g.review_ids)
  ORDER BY ` + findingClass + `, f.file COLLATE "C", f.start_line, f.id
  LIMIT @top
) t
UNION ALL
SELECT '` + kindHot + `', NULL, NULL, hot.file, 0, NULL, NULL, NULL, NULL, hot.repo_id, hot.repo, hot.prs::text,
       0, 0, 0, 0, 0, 0, 0, hot.findings, hot.critical
FROM hot`

// windowRuns: the agent runs of a PR of the filter that ran in [from, to), whatever their status,
// summed per PR and window half (domain.Facts.Runs): the first run of the group and the sum of
// the known costs (NULL when none is known). Rows of the current half have ran_at >= split.
func windowRuns(db *gorm.DB, workspaceID string, repoIDs []string, from, to, split time.Time) *gorm.DB {
	return db.Table("agent_runs ar").
		Select("ar.pr_id, (ar.ran_at >= ?) AS cur, (EXTRACT(EPOCH FROM MIN(ar.ran_at)) * 1000000)::bigint AS ran_at, SUM(ar.cost_usd)::float8 AS cost_usd", split).
		Joins("JOIN pull_requests p ON p.id = ar.pr_id AND p.workspace_id = ar.workspace_id").
		Where("ar.workspace_id = ? AND p.repo_id IN ?", workspaceID, repoIDs).
		Where("ar.ran_at >= ? AND ar.ran_at < ?", from, to).
		Group("ar.pr_id, cur")
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

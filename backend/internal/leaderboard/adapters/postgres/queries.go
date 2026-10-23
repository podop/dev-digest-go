package postgres

// The SQL of the store: SELECT only, every statement scoped to one workspace on every table that
// has the column. They are raw because the builder cannot express the CTEs and the correlated
// aggregates (backend/AGENTS.md: Raw only for what the builder can't). A request runs at most
// eight of them in total: RepoScope 1, WindowFacts 5.

// pullColumns is the column list of the window pull query.
const pullColumns = `p.id, p.repo_id, r.name AS repo, p.number, p.title, p.author, p.head_sha, p.last_reviewed_sha,
	p.additions, p.deletions, p.files_count, p.status, p.draft,
	p.opened_at, p.updated_at, p.merged_at, p.closed_at`

// qRepoAll lists every repository of the workspace; qRepoOne selects one (zero rows = not found).
const qRepoAll = `
SELECT id, owner, name, full_name, last_polled_at
FROM repos
WHERE workspace_id = ?
ORDER BY full_name COLLATE "C", id`

const qRepoOne = `
SELECT id, owner, name, full_name, last_polled_at
FROM repos
WHERE workspace_id = ? AND id = ?::uuid`

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

// qWindowReviews: every review of kind review created in [from, to) (all of them belong to a PR
// of qWindowPulls). The agent name is NULL for a deleted agent.
const qWindowReviews = `
SELECT rv.id, rv.pr_id, rv.agent_id, a.name AS agent_name, rv.score, rv.created_at
FROM reviews rv
JOIN pull_requests p ON p.id = rv.pr_id AND p.workspace_id = rv.workspace_id
LEFT JOIN agents a ON a.id = rv.agent_id
WHERE rv.workspace_id = @ws AND p.repo_id IN @repos AND rv.kind = 'review'
  AND rv.created_at >= @from AND rv.created_at < @to`

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

// qWindowRuns: the agent runs of a PR of the filter that ran in [from, to), whatever their status.
const qWindowRuns = `
SELECT ar.pr_id, ar.ran_at, ar.cost_usd
FROM agent_runs ar
JOIN pull_requests p ON p.id = ar.pr_id AND p.workspace_id = ar.workspace_id
WHERE ar.workspace_id = @ws AND p.repo_id IN @repos
  AND ar.ran_at >= @from AND ar.ran_at < @to`

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

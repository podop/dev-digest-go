package postgres

// The SQL of the store: SELECT only, every statement scoped to one workspace on every table that
// has the column. They are raw because the builder cannot express the CTEs and the correlated
// aggregates (backend/AGENTS.md: Raw only for what the builder can't). A request runs at most
// eight of them in total: RepoScope 1, WindowFacts 4, OpenState 2.

// pullColumns is the column list shared by the window and the open-state pull queries.
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

// qWindowPulls: the PRs with a review of kind review created in [from, to), OR merged in it, OR
// opened in it. first_review_at is the first review ever (not limited to the range).
const qWindowPulls = `
SELECT ` + pullColumns + `,
	(SELECT MIN(rv.created_at) FROM reviews rv WHERE rv.pr_id = p.id AND rv.kind = 'review') AS first_review_at
FROM pull_requests p
JOIN repos r ON r.id = p.repo_id AND r.workspace_id = p.workspace_id
WHERE p.workspace_id = @ws AND p.repo_id IN @repos
  AND (
    EXISTS (SELECT 1 FROM reviews rv WHERE rv.pr_id = p.id AND rv.kind = 'review'
            AND rv.created_at >= @from AND rv.created_at < @to)
    OR (p.merged_at >= @from AND p.merged_at < @to)
    OR (p.opened_at >= @from AND p.opened_at < @to)
  )`

// qWindowReviews: every review of kind review created in [from, to) (all of them belong to a PR
// of qWindowPulls). The agent name is NULL for a deleted agent.
const qWindowReviews = `
SELECT rv.id, rv.pr_id, rv.agent_id, a.name AS agent_name, rv.score, rv.created_at
FROM reviews rv
JOIN pull_requests p ON p.id = rv.pr_id AND p.workspace_id = rv.workspace_id
LEFT JOIN agents a ON a.id = rv.agent_id
WHERE rv.workspace_id = @ws AND p.repo_id IN @repos AND rv.kind = 'review'
  AND rv.created_at >= @from AND rv.created_at < @to`

// qWindowFindings: the findings (every kind) of those reviews. The store only knows the whole
// range, not where the current window starts, so it cannot pick "the newest review per agent"
// of each window; it returns the findings of all reviews of the range, a superset of both
// snapshots (domain.Facts membership), and the folds pick the snapshots.
const qWindowFindings = `
SELECT f.review_id, f.file, f.severity, f.accepted_at, f.dismissed_at
FROM findings f
JOIN reviews rv ON rv.id = f.review_id
JOIN pull_requests p ON p.id = rv.pr_id AND p.workspace_id = rv.workspace_id
WHERE rv.workspace_id = @ws AND p.repo_id IN @repos AND rv.kind = 'review'
  AND rv.created_at >= @from AND rv.created_at < @to`

// qWindowRuns: the agent runs of a PR of the filter that ran in [from, to), whatever their status.
const qWindowRuns = `
SELECT ar.pr_id, ar.ran_at, ar.cost_usd
FROM agent_runs ar
JOIN pull_requests p ON p.id = ar.pr_id AND p.workspace_id = ar.workspace_id
WHERE ar.workspace_id = @ws AND p.repo_id IN @repos
  AND ar.ran_at >= @from AND ar.ran_at < @to`

// qOpenPulls: the PRs whose stored status is neither merged nor closed.
const qOpenPulls = `
SELECT ` + pullColumns + `,
	EXISTS (SELECT 1 FROM reviews rv WHERE rv.pr_id = p.id AND rv.kind = 'review') AS reviewed
FROM pull_requests p
JOIN repos r ON r.id = p.repo_id AND r.workspace_id = p.workspace_id
WHERE p.workspace_id = @ws AND p.repo_id IN @repos AND p.status NOT IN ('merged', 'closed')`

// qOpenCritical counts, per open PR, the CRITICAL findings (every kind) with neither accepted nor
// dismissed time in the current reviews: the newest review of kind review per agent, ever.
// PARTITION BY treats the NULL agent_id (deleted agents) as one partition, so all deleted agents
// are one agent.
const qOpenCritical = `
WITH ranked AS (
  SELECT rv.id, rv.pr_id,
         ROW_NUMBER() OVER (PARTITION BY rv.pr_id, rv.agent_id ORDER BY rv.created_at DESC, rv.id DESC) AS rn
  FROM reviews rv
  JOIN pull_requests p ON p.id = rv.pr_id AND p.workspace_id = rv.workspace_id
  WHERE rv.workspace_id = @ws AND p.repo_id IN @repos AND rv.kind = 'review'
    AND p.status NOT IN ('merged', 'closed')
)
SELECT ranked.pr_id, COUNT(*) AS n
FROM ranked
JOIN findings f ON f.review_id = ranked.id
WHERE ranked.rn = 1 AND f.severity = 'CRITICAL' AND f.accepted_at IS NULL AND f.dismissed_at IS NULL
GROUP BY ranked.pr_id`

package postgres

import (
	"context"
	"fmt"
	"time"

	"dev-digest-go/backend/internal/reviews/domain"
)

// headerRow is a multi-agent run joined with its pull request.
type headerRow struct {
	ID       string    `gorm:"column:id"`
	PRID     string    `gorm:"column:pr_id"`
	RepoID   string    `gorm:"column:repo_id"`
	PRNumber *int      `gorm:"column:pr_number"`
	PRTitle  *string   `gorm:"column:pr_title"`
	RanAt    time.Time `gorm:"column:ran_at"`
}

// FindMultiAgentRun returns the header of a multi-agent run of the workspace.
func (s *Store) FindMultiAgentRun(ctx context.Context, workspaceID, id string) (domain.MultiAgentHeader, error) {
	var row headerRow
	err := s.conn(ctx).Table("multi_agent_runs").
		Select("multi_agent_runs.id, multi_agent_runs.pr_id, pull_requests.repo_id, "+
			"pull_requests.number AS pr_number, pull_requests.title AS pr_title, multi_agent_runs.ran_at").
		Joins("JOIN pull_requests ON pull_requests.id = multi_agent_runs.pr_id").
		Where("multi_agent_runs.id = ? AND multi_agent_runs.workspace_id = ?", id, workspaceID).
		Take(&row).Error
	if isNoRows(err) {
		return domain.MultiAgentHeader{}, fmt.Errorf("multi-agent run %s: %w", id, domain.ErrNotFound)
	}
	if err != nil {
		return domain.MultiAgentHeader{}, fmt.Errorf("select multi-agent run: %w", err)
	}
	return domain.MultiAgentHeader{
		ID: row.ID, PRID: row.PRID, RepoID: row.RepoID, PRNumber: row.PRNumber, PRTitle: row.PRTitle, RanAt: utc(row.RanAt),
	}, nil
}

// columnRunRow is a run linked to a multi-agent run, with its agent's name.
type columnRunRow struct {
	ID         string   `gorm:"column:id"`
	AgentID    *string  `gorm:"column:agent_id"`
	AgentName  *string  `gorm:"column:agent_name"`
	Provider   *string  `gorm:"column:provider"`
	Model      *string  `gorm:"column:model"`
	Status     *string  `gorm:"column:status"`
	Error      *string  `gorm:"column:error"`
	Score      *int     `gorm:"column:score"`
	DurationMS *int     `gorm:"column:duration_ms"`
	CostUSD    *float64 `gorm:"column:cost_usd"`
}

// columnReviewRow is the part of a review a column shows.
type columnReviewRow struct {
	ID      string  `gorm:"column:id"`
	RunID   string  `gorm:"column:run_id"`
	Verdict *string `gorm:"column:verdict"`
	Summary *string `gorm:"column:summary"`
	Score   *int    `gorm:"column:score"`
}

// MultiAgentColumns returns one column per run linked to the multi-agent run, in no order. A
// column carries its run's newest review (kind review) and that review's findings ordered by
// file (UTF-16 order), start line, id, each with the Learn memory entry it produced.
func (s *Store) MultiAgentColumns(ctx context.Context, workspaceID, id string) ([]domain.AgentColumn, error) {
	var runs []columnRunRow
	err := s.conn(ctx).Table("agent_runs").
		Select("agent_runs.id, agent_runs.agent_id, agents.name AS agent_name, agent_runs.provider, agent_runs.model, "+
			"agent_runs.status, agent_runs.error, agent_runs.score, agent_runs.duration_ms, agent_runs.cost_usd").
		Joins("LEFT JOIN agents ON agents.id = agent_runs.agent_id").
		Where("agent_runs.multi_agent_run_id = ? AND agent_runs.workspace_id = ?", id, workspaceID).
		Scan(&runs).Error
	if err != nil {
		return nil, fmt.Errorf("select multi-agent columns: %w", err)
	}
	columns := make([]domain.AgentColumn, 0, len(runs))
	if len(runs) == 0 {
		return columns, nil
	}
	runIDs := make([]string, len(runs))
	for i, r := range runs {
		runIDs[i] = r.ID
	}

	var reviews []columnReviewRow
	err = s.conn(ctx).Table("reviews").Select("id, run_id, verdict, summary, score").
		Where("run_id IN ? AND kind = ?", runIDs, kindReview).
		Order("created_at DESC, id ASC").Scan(&reviews).Error
	if err != nil {
		return nil, fmt.Errorf("select column reviews: %w", err)
	}
	// A run has one review; should it ever have several, the newest wins.
	reviewOf := map[string]columnReviewRow{}
	for _, r := range reviews {
		if _, seen := reviewOf[r.RunID]; !seen {
			reviewOf[r.RunID] = r
		}
	}
	reviewIDs := make([]string, 0, len(reviewOf))
	for _, r := range reviewOf {
		reviewIDs = append(reviewIDs, r.ID)
	}
	byReview, err := s.findingsOfReviews(ctx, reviewIDs)
	if err != nil {
		return nil, err
	}
	var findingIDs []string
	for _, fs := range byReview {
		for _, f := range fs {
			findingIDs = append(findingIDs, f.ID)
		}
	}
	cases, err := s.evalCaseIDs(ctx, findingIDs)
	if err != nil {
		return nil, err
	}
	memories, err := s.memoryIDsByFinding(ctx, workspaceID, findingIDs)
	if err != nil {
		return nil, err
	}

	for _, r := range runs {
		col := domain.AgentColumn{
			RunID: r.ID, Provider: r.Provider, Model: r.Model, Status: domain.ColumnStatus(statusOf(r.Status)),
			Error: r.Error, Score: r.Score, DurationMS: r.DurationMS, CostUSD: r.CostUSD,
			Findings: []domain.FindingRecord{},
		}
		// A deleted agent leaves no name: the columns carry "" and the client labels them.
		if r.AgentID != nil {
			col.AgentID = *r.AgentID
		}
		if r.AgentName != nil {
			col.AgentName = *r.AgentName
		}
		if rv, ok := reviewOf[r.ID]; ok {
			col.Verdict, col.Summary = rv.Verdict, rv.Summary
			if rv.Score != nil {
				col.Score = rv.Score
			}
			for _, f := range byReview[rv.ID] {
				fr, err := f.toDomain(lookup(cases, f.ID), lookup(memories, f.ID))
				if err != nil {
					return nil, err
				}
				// The multi-agent API always carries the key: memory_id is null without Learn.
				col.Findings = append(col.Findings, fr)
			}
			domain.SortFindings(col.Findings)
		}
		columns = append(columns, col)
	}
	return columns, nil
}

// RepoInWorkspace reports whether the repository belongs to the workspace.
func (s *Store) RepoInWorkspace(ctx context.Context, workspaceID, repoID string) (bool, error) {
	var n int64
	err := s.conn(ctx).Model(&repoRow{}).Where("id = ? AND workspace_id = ?", repoID, workspaceID).Count(&n).Error
	if err != nil {
		return false, fmt.Errorf("select repo: %w", err)
	}
	return n > 0, nil
}

// summaryRow is a multi-agent run of a repo with its aggregates over the linked runs.
type summaryRow struct {
	ID           string    `gorm:"column:id"`
	PRID         string    `gorm:"column:pr_id"`
	PRNumber     *int      `gorm:"column:pr_number"`
	PRTitle      *string   `gorm:"column:pr_title"`
	RanAt        time.Time `gorm:"column:ran_at"`
	AgentCount   int       `gorm:"column:agent_count"`
	RunningCount int       `gorm:"column:running_count"`
	TotalCostUSD *float64  `gorm:"column:total_cost_usd"`
}

// ListMultiAgentRuns returns the repo's newest multi-agent runs (ran_at descending, then id), at
// most limit.
func (s *Store) ListMultiAgentRuns(ctx context.Context, workspaceID, repoID string, limit int) ([]domain.MultiAgentSummary, error) {
	var rows []summaryRow
	err := s.conn(ctx).Table("multi_agent_runs").
		Select("multi_agent_runs.id, multi_agent_runs.pr_id, pull_requests.number AS pr_number, "+
			"pull_requests.title AS pr_title, multi_agent_runs.ran_at, "+
			"count(agent_runs.id)::int AS agent_count, "+
			"(count(agent_runs.id) FILTER (WHERE agent_runs.status = 'running'))::int AS running_count, "+
			"sum(agent_runs.cost_usd)::float8 AS total_cost_usd").
		Joins("JOIN pull_requests ON pull_requests.id = multi_agent_runs.pr_id").
		Joins("LEFT JOIN agent_runs ON agent_runs.multi_agent_run_id = multi_agent_runs.id").
		Where("multi_agent_runs.workspace_id = ? AND pull_requests.repo_id = ?", workspaceID, repoID).
		Group("multi_agent_runs.id, pull_requests.number, pull_requests.title").
		Order("multi_agent_runs.ran_at DESC, multi_agent_runs.id ASC").
		Limit(limit).
		Scan(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("select multi-agent runs: %w", err)
	}
	out := make([]domain.MultiAgentSummary, len(rows))
	for i, r := range rows {
		out[i] = domain.MultiAgentSummary{
			ID: r.ID, PRID: r.PRID, PRNumber: r.PRNumber, PRTitle: r.PRTitle, RanAt: utc(r.RanAt),
			AgentCount: r.AgentCount, RunningCount: r.RunningCount, TotalCostUSD: r.TotalCostUSD,
		}
	}
	return out, nil
}

// estimateRow is one agent's averages over its sampled runs.
type estimateRow struct {
	AgentID       string   `gorm:"column:agent_id"`
	SampleSize    int      `gorm:"column:sample_size"`
	AvgDurationMS *float64 `gorm:"column:avg_duration_ms"`
	AvgCostUSD    *float64 `gorm:"column:avg_cost_usd"`
}

// AgentEstimates returns, per agent with a done run that has a duration or a cost, the averages
// over its newest domain.EstimateSampleSize such runs (any pull request, any source), by agent id.
func (s *Store) AgentEstimates(ctx context.Context, workspaceID string) ([]domain.AgentRunEstimate, error) {
	var rows []estimateRow
	// The per-agent window (row_number over a partition) has no builder form.
	err := s.conn(ctx).Raw(`
SELECT agent_id::text AS agent_id,
       count(*)::int AS sample_size,
       avg(duration_ms)::float8 AS avg_duration_ms,
       avg(cost_usd)::float8 AS avg_cost_usd
FROM (
  SELECT agent_id, duration_ms, cost_usd,
         row_number() OVER (PARTITION BY agent_id ORDER BY ran_at DESC, id) AS rn
  FROM agent_runs
  WHERE workspace_id = ? AND status = 'done' AND agent_id IS NOT NULL
        AND (duration_ms IS NOT NULL OR cost_usd IS NOT NULL)
) ranked
WHERE rn <= ?
GROUP BY agent_id
ORDER BY agent_id`, workspaceID, domain.EstimateSampleSize).Scan(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("select agent estimates: %w", err)
	}
	out := make([]domain.AgentRunEstimate, len(rows))
	for i, r := range rows {
		out[i] = domain.AgentRunEstimate{
			AgentID: r.AgentID, SampleSize: r.SampleSize, AvgDurationMS: r.AvgDurationMS, AvgCostUSD: r.AvgCostUSD,
		}
	}
	return out, nil
}

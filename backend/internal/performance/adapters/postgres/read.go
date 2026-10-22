package postgres

import (
	"context"
	"encoding/json"
	"fmt"

	"dev-digest-go/backend/internal/performance/domain"
)

// CountedRuns implements app.Reader.
func (s *Store) CountedRuns(ctx context.Context, workspaceID string, win domain.PeriodWindow, agentID string) ([]domain.RunFact, error) {
	var rows []runRow
	q := s.conn(ctx).Table("agent_runs AS r").
		Select("r.agent_id, r.status, r.source, r.model, r.duration_ms, r.cost_usd, r.ran_at")
	if err := counted(q, workspaceID, win, agentID).Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("select counted runs: %w", err)
	}
	out := make([]domain.RunFact, len(rows))
	for i, r := range rows {
		out[i] = domain.RunFact{
			AgentID: r.AgentID, Status: domain.RunStatus(r.Status), Source: domain.RunSource(r.Source),
			Model: r.Model, DurationMs: r.DurationMs, CostUSD: r.CostUSD, RanAt: r.RanAt,
		}
	}
	return out, nil
}

// FindingCounts implements app.Reader. An accepted finding counts as accepted even when it also
// has a dismissed_at (accepting clears the other, but the column may hold both).
func (s *Store) FindingCounts(ctx context.Context, workspaceID string, win domain.PeriodWindow, agentID string) ([]domain.FindingCounts, error) {
	var rows []findingCountsRow
	q := s.conn(ctx).Table("findings AS f").
		Select(`r.agent_id AS agent_id, count(*) AS total,
			count(*) FILTER (WHERE f.accepted_at IS NOT NULL) AS accepted,
			count(*) FILTER (WHERE f.dismissed_at IS NOT NULL AND f.accepted_at IS NULL) AS dismissed`).
		Joins("JOIN reviews rv ON rv.id = f.review_id").
		Joins("JOIN agent_runs r ON r.id = rv.run_id").
		Group("r.agent_id")
	if err := counted(q, workspaceID, win, agentID).Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("select finding counts: %w", err)
	}
	out := make([]domain.FindingCounts, len(rows))
	for i, r := range rows {
		out[i] = domain.FindingCounts{AgentID: r.AgentID, Total: r.Total, Accepted: r.Accepted, Dismissed: r.Dismissed}
	}
	return out, nil
}

// FindingFacts implements app.Reader.
func (s *Store) FindingFacts(ctx context.Context, workspaceID string, win domain.PeriodWindow, agentID string) ([]domain.FindingFact, error) {
	var rows []findingFactRow
	q := s.conn(ctx).Table("findings AS f").
		Select("r.ran_at AS ran_at, f.severity AS severity, f.category AS category, count(*) AS n").
		Joins("JOIN reviews rv ON rv.id = f.review_id").
		Joins("JOIN agent_runs r ON r.id = rv.run_id").
		Group("r.id, r.ran_at, f.severity, f.category")
	if err := counted(q, workspaceID, win, agentID).Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("select finding facts: %w", err)
	}
	out := make([]domain.FindingFact, len(rows))
	for i, r := range rows {
		out[i] = domain.FindingFact{RanAt: r.RanAt, Severity: r.Severity, Category: r.Category, Count: r.N}
	}
	return out, nil
}

// SkillUsage implements app.Reader.
func (s *Store) SkillUsage(ctx context.Context, workspaceID string, win domain.PeriodWindow, agentID string) ([]domain.SkillFact, error) {
	var rows []skillUsageRow
	q := s.conn(ctx).Table("agent_run_skills AS ars").
		Select("s.id AS skill_id, s.name AS name, count(DISTINCT ars.run_id) AS runs").
		Joins("JOIN agent_runs r ON r.id = ars.run_id").
		Joins("JOIN skills s ON s.id = ars.skill_id").
		Group("s.id, s.name")
	if err := counted(q, workspaceID, win, agentID).Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("select skill usage: %w", err)
	}
	out := make([]domain.SkillFact, len(rows))
	for i, r := range rows {
		out[i] = domain.SkillFact{SkillID: r.SkillID, Name: r.Name, Runs: r.Runs}
	}
	return out, nil
}

// MemoryPulled implements app.Reader. Only the small memory_pulled array leaves the database,
// not the whole trace document.
func (s *Store) MemoryPulled(ctx context.Context, workspaceID string, win domain.PeriodWindow, agentID string) ([]json.RawMessage, error) {
	var rows []memoryRow
	q := s.conn(ctx).Table("run_traces AS t").
		Select("t.trace -> 'memory_pulled' AS pulled").
		Joins("JOIN agent_runs r ON r.id = t.run_id")
	if err := counted(q, workspaceID, win, agentID).Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("select pulled memories: %w", err)
	}
	out := make([]json.RawMessage, len(rows))
	for i, r := range rows {
		out[i] = json.RawMessage(r.Pulled)
	}
	return out, nil
}

// RunHistory implements app.Reader.
func (s *Store) RunHistory(ctx context.Context, workspaceID string, win domain.PeriodWindow, agentID string, limit int) ([]domain.RunHistoryItem, error) {
	var rows []historyRow
	q := s.conn(ctx).Table("agent_runs AS r").
		Select(`r.id AS run_id, r.ran_at, r.status, r.source, p.id AS pr_id, p.repo_id AS pr_repo_id,
			p.number AS pr_number, r.tokens_in, r.tokens_out, r.cost_usd, r.findings_count, t.run_id AS trace_run_id`).
		Joins("LEFT JOIN pull_requests p ON p.id = r.pr_id").
		Joins("LEFT JOIN run_traces t ON t.run_id = r.id").
		Order("r.ran_at DESC, r.id DESC").
		Limit(limit)
	if err := counted(q, workspaceID, win, agentID).Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("select run history: %w", err)
	}
	out := make([]domain.RunHistoryItem, len(rows))
	for i, r := range rows {
		item := domain.RunHistoryItem{
			RunID: r.RunID, RanAt: r.RanAt, Status: domain.RunStatus(r.Status), Source: domain.RunSource(r.Source),
			CostUSD: r.CostUSD, FindingsCount: r.FindingsCount, HasTrace: r.TraceRunID != nil,
		}
		if r.PRID != nil && r.PRRepoID != nil && r.PRNumber != nil {
			item.PR = &domain.RunPR{ID: *r.PRID, RepoID: *r.PRRepoID, Number: *r.PRNumber}
		}
		if r.TokensIn != nil || r.TokensOut != nil {
			n := 0
			if r.TokensIn != nil {
				n += *r.TokensIn
			}
			if r.TokensOut != nil {
				n += *r.TokensOut
			}
			item.Tokens = &n
		}
		out[i] = item
	}
	return out, nil
}

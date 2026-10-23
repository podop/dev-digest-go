package app

import (
	"context"
	"errors"
	"fmt"

	"dev-digest-go/backend/internal/evals/domain"
	"dev-digest-go/backend/internal/platform/apperr"
)

// assertOwner is the 404 unless the agent or skill belongs to the workspace.
func (s *RunService) assertOwner(ctx context.Context, workspaceID string, kind domain.OwnerKind, ownerID string) error {
	ok, err := s.cases.OwnerExists(ctx, workspaceID, kind, ownerID)
	if err != nil {
		return fmt.Errorf("check %s owner: %w", kind, err)
	}
	if !ok {
		return ownerNotFound(kind)
	}
	return nil
}

// ListAgentRuns implements Runs. PeriodNone and PeriodAll are unbounded.
func (s *RunService) ListAgentRuns(ctx context.Context, workspaceID, agentID string, limit int, period domain.Period) ([]domain.Run, error) {
	if err := s.assertOwner(ctx, workspaceID, domain.OwnerAgent, agentID); err != nil {
		return nil, err
	}
	runs, err := s.runs.ListAgentRuns(ctx, workspaceID, agentID, limit, domain.PeriodStart(period, s.now()))
	if err != nil {
		return nil, fmt.Errorf("list agent runs: %w", err)
	}
	return nonNil(runs), nil
}

// ListSkillRuns implements Runs.
func (s *RunService) ListSkillRuns(ctx context.Context, workspaceID, skillID string, limit int) ([]domain.Run, error) {
	if err := s.assertOwner(ctx, workspaceID, domain.OwnerSkill, skillID); err != nil {
		return nil, err
	}
	runs, err := s.runs.ListSkillRuns(ctx, workspaceID, skillID, limit)
	if err != nil {
		return nil, fmt.Errorf("list skill runs: %w", err)
	}
	return nonNil(runs), nil
}

// nonNil turns a nil run list into an empty one (lists are [] on the wire, never null).
func nonNil(runs []domain.Run) []domain.Run {
	if runs == nil {
		return []domain.Run{}
	}
	return runs
}

// getRun reads a run of the workspace; the 404 of a missing one.
func (s *RunService) getRun(ctx context.Context, workspaceID, runID string) (domain.Run, error) {
	run, err := s.runs.GetRun(ctx, workspaceID, runID)
	if errors.Is(err, domain.ErrNotFound) {
		return domain.Run{}, apperr.NotFound(msgRunNotFound)
	}
	if err != nil {
		return domain.Run{}, fmt.Errorf("read run: %w", err)
	}
	return run, nil
}

// Detail implements Runs.
func (s *RunService) Detail(ctx context.Context, workspaceID, runID string) (domain.RunDetail, error) {
	run, err := s.getRun(ctx, workspaceID, runID)
	if err != nil {
		return domain.RunDetail{}, err
	}
	results, err := s.runs.RunResults(ctx, workspaceID, runID)
	if err != nil {
		return domain.RunDetail{}, fmt.Errorf("read run results: %w", err)
	}
	if results == nil {
		results = []domain.CaseResult{}
	}
	return domain.RunDetail{Run: run, Results: results}, nil
}

// Compare implements Runs: both runs are read (404 for either) before the comparability rules
// apply; the results are read only for comparable runs. The store must report an id that is not
// a UUID as domain.ErrNotFound.
func (s *RunService) Compare(ctx context.Context, workspaceID, a, b string) (domain.Compare, error) {
	runA, err := s.getRun(ctx, workspaceID, a)
	if err != nil {
		return domain.Compare{}, err
	}
	runB, err := s.getRun(ctx, workspaceID, b)
	if err != nil {
		return domain.Compare{}, err
	}
	if err := domain.AssertComparable(runA, runB); err != nil {
		return domain.Compare{}, ruleErr(err)
	}
	resA, err := s.runs.RunResults(ctx, workspaceID, a)
	if err != nil {
		return domain.Compare{}, fmt.Errorf("read results of run a: %w", err)
	}
	resB, err := s.runs.RunResults(ctx, workspaceID, b)
	if err != nil {
		return domain.Compare{}, fmt.Errorf("read results of run b: %w", err)
	}
	cmp, err := domain.CompareRuns(domain.CompareSide{Run: runA, Results: resA}, domain.CompareSide{Run: runB, Results: resB})
	if err != nil {
		return domain.Compare{}, ruleErr(err)
	}
	return cmp, nil
}

// Overview implements Runs (spec 7.5.10): per agent that owns a case or an agent-owned run, its
// newest finished run and sparkline; the newest runs of the workspace.
func (s *RunService) Overview(ctx context.Context, workspaceID string) (domain.Overview, error) {
	agents, err := s.runs.DashboardAgents(ctx, workspaceID)
	if err != nil {
		return domain.Overview{}, fmt.Errorf("list dashboard agents: %w", err)
	}
	counts, err := s.cases.AgentCaseCounts(ctx, workspaceID)
	if err != nil {
		return domain.Overview{}, fmt.Errorf("count agent cases: %w", err)
	}
	out := domain.Overview{Agents: make([]domain.AgentOverview, 0, len(agents))}
	for _, ag := range agents {
		finished, err := s.runs.FinishedAgentRuns(ctx, workspaceID, ag.ID, domain.SparklineMax, nil)
		if err != nil {
			return domain.Overview{}, fmt.Errorf("read finished runs of agent %s: %w", ag.ID, err)
		}
		row := domain.AgentOverview{AgentRef: ag, CasesTotal: counts[ag.ID], Sparkline: domain.Sparkline(finished)}
		if len(finished) > 0 {
			row.Latest = &finished[0]
		}
		out.Agents = append(out.Agents, row)
	}
	recent, err := s.runs.RecentAgentRuns(ctx, workspaceID, domain.RecentRunsMax)
	if err != nil {
		return domain.Overview{}, fmt.Errorf("read recent runs: %w", err)
	}
	if recent == nil {
		recent = []domain.RecentRun{}
	}
	out.RecentRuns = recent
	return out, nil
}

// AgentDashboard implements Runs (spec 7.5.10). Without a period the newest LatestWindow finished
// runs give latest, previous and the trend. With one, latest and previous are the newest two
// finished runs regardless of the window and the trend is the newest TrendMax inside it.
func (s *RunService) AgentDashboard(ctx context.Context, workspaceID, agentID string, period domain.Period) (domain.AgentDashboard, error) {
	if err := s.assertOwner(ctx, workspaceID, domain.OwnerAgent, agentID); err != nil {
		return domain.AgentDashboard{}, err
	}
	total, err := s.cases.CountCases(ctx, workspaceID, domain.OwnerAgent, agentID)
	if err != nil {
		return domain.AgentDashboard{}, fmt.Errorf("count cases: %w", err)
	}
	var newest, trendSource []domain.Run
	trendMax := domain.LatestWindow
	if period == domain.PeriodNone {
		newest, err = s.runs.FinishedAgentRuns(ctx, workspaceID, agentID, domain.LatestWindow, nil)
		trendSource = newest
	} else {
		trendMax = domain.TrendMax
		if newest, err = s.runs.FinishedAgentRuns(ctx, workspaceID, agentID, 2, nil); err == nil {
			trendSource, err = s.runs.FinishedAgentRuns(ctx, workspaceID, agentID, domain.TrendMax, domain.PeriodStart(period, s.now()))
		}
	}
	if err != nil {
		return domain.AgentDashboard{}, fmt.Errorf("read finished runs: %w", err)
	}
	d := domain.AgentDashboard{AgentID: agentID, CasesTotal: total, Trend: domain.Trend(trendSource, trendMax)}
	if len(newest) > 0 {
		d.Latest = &newest[0]
	}
	if len(newest) > 1 {
		d.Previous = &newest[1]
	}
	d.Alert = domain.RegressionAlert(d.Latest, d.Previous)
	return d, nil
}

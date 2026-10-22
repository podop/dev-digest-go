package app

import (
	"context"
	"errors"
	"fmt"

	"dev-digest-go/backend/internal/performance/domain"
	"dev-digest-go/backend/internal/platform/apperr"
)

// Service implements API over a Reader and a clock.
type Service struct {
	reader Reader
	now    Clock
}

var _ API = (*Service)(nil)

// NewService builds the performance use cases.
func NewService(reader Reader, now Clock) *Service {
	return &Service{reader: reader, now: now}
}

// windows resolves the query against the clock into the current and the previous window.
func (s *Service) windows(q domain.PeriodQuery) (cur, prev domain.PeriodWindow, err error) {
	cur, err = domain.ResolveWindow(q, s.now())
	if err != nil {
		return domain.PeriodWindow{}, domain.PeriodWindow{}, fmt.Errorf("resolve period window: %w", err)
	}
	return cur, cur.Previous(), nil
}

// windowFacts are all agents' counted runs of one window and the same runs grouped by agent.
type windowFacts struct {
	runs    []domain.RunFact
	grouped domain.Grouped
}

// readWindow reads every agent's counted runs and finding counts of one window.
func (s *Service) readWindow(ctx context.Context, ws string, win domain.PeriodWindow, known map[string]bool) (windowFacts, error) {
	runs, err := s.reader.CountedRuns(ctx, ws, win, "")
	if err != nil {
		return windowFacts{}, fmt.Errorf("read counted runs: %w", err)
	}
	counts, err := s.reader.FindingCounts(ctx, ws, win, "")
	if err != nil {
		return windowFacts{}, fmt.Errorf("read finding counts: %w", err)
	}
	return windowFacts{runs: runs, grouped: domain.GroupFacts(runs, counts, known)}, nil
}

// rows builds one row per agent in the order given, then the deleted-agents row when the window
// has such runs. previous supplies each row's previous-window facts (the zero Grouped: none).
func rows(agents []domain.AgentRef, cur, previous domain.Grouped, win domain.PeriodWindow) []domain.Row {
	out := make([]domain.Row, 0, len(agents)+1)
	for i := range agents {
		id := &agents[i].ID
		out = append(out, domain.BuildRow(&agents[i], cur.Facts(id), previous.Facts(id), win))
	}
	if cur.HasDeleted() {
		out = append(out, domain.BuildRow(nil, cur.Facts(nil), previous.Facts(nil), win))
	}
	return out
}

// Dashboard implements API.
func (s *Service) Dashboard(ctx context.Context, workspaceID string, q domain.PeriodQuery) (domain.AgentPerf, error) {
	win, prevWin, err := s.windows(q)
	if err != nil {
		return domain.AgentPerf{}, err
	}
	listed, err := s.reader.ListAgents(ctx, workspaceID)
	if err != nil {
		return domain.AgentPerf{}, fmt.Errorf("list agents: %w", err)
	}
	agents := domain.SortAgents(listed)
	known := make(map[string]bool, len(agents))
	for _, a := range agents {
		known[a.ID] = true
	}
	cur, err := s.readWindow(ctx, workspaceID, win, known)
	if err != nil {
		return domain.AgentPerf{}, err
	}
	prev, err := s.readWindow(ctx, workspaceID, prevWin, known)
	if err != nil {
		return domain.AgentPerf{}, err
	}

	current := rows(agents, cur.grouped, prev.grouped, win)
	// The previous totals fold every agent's previous-window facts, deleted agents included.
	previous := rows(agents, prev.grouped, domain.Grouped{}, prevWin)
	return domain.AgentPerf{
		Period:          win.Info(),
		Summary:         domain.Summarize(current),
		PreviousSummary: domain.Summarize(previous),
		RunsTrend:       domain.RunsTrend(cur.runs, win),
		Agents:          current,
		CostByAgent:     domain.CostByAgent(current),
		CostByModel:     domain.CostByModel(cur.runs),
	}, nil
}

// Stats implements API.
func (s *Service) Stats(ctx context.Context, workspaceID, agentID string, q domain.PeriodQuery) (domain.AgentStats, error) {
	win, prevWin, err := s.windows(q)
	if err != nil {
		return domain.AgentStats{}, err
	}
	agent, err := s.reader.FindAgent(ctx, workspaceID, agentID)
	if errors.Is(err, domain.ErrNotFound) {
		return domain.AgentStats{}, apperr.NotFound("Agent not found")
	}
	if err != nil {
		return domain.AgentStats{}, fmt.Errorf("find agent: %w", err)
	}

	facts := func(win domain.PeriodWindow) (domain.AgentFacts, error) {
		runs, err := s.reader.CountedRuns(ctx, workspaceID, win, agent.ID)
		if err != nil {
			return domain.AgentFacts{}, fmt.Errorf("read counted runs: %w", err)
		}
		counts, err := s.reader.FindingCounts(ctx, workspaceID, win, agent.ID)
		if err != nil {
			return domain.AgentFacts{}, fmt.Errorf("read finding counts: %w", err)
		}
		f := domain.AgentFacts{Runs: runs}
		if len(counts) > 0 {
			f.Findings = &counts[0]
		}
		return f, nil
	}
	cur, err := facts(win)
	if err != nil {
		return domain.AgentStats{}, err
	}
	prev, err := facts(prevWin)
	if err != nil {
		return domain.AgentStats{}, err
	}
	findings, err := s.reader.FindingFacts(ctx, workspaceID, win, agent.ID)
	if err != nil {
		return domain.AgentStats{}, fmt.Errorf("read finding facts: %w", err)
	}
	skills, err := s.reader.SkillUsage(ctx, workspaceID, win, agent.ID)
	if err != nil {
		return domain.AgentStats{}, fmt.Errorf("read skill usage: %w", err)
	}
	memory, err := s.reader.MemoryPulled(ctx, workspaceID, win, agent.ID)
	if err != nil {
		return domain.AgentStats{}, fmt.Errorf("read pulled memories: %w", err)
	}
	history, err := s.reader.RunHistory(ctx, workspaceID, win, agent.ID, domain.RunHistoryLimit)
	if err != nil {
		return domain.AgentStats{}, fmt.Errorf("read run history: %w", err)
	}

	row := domain.BuildRow(&agent, cur, prev, win)
	return domain.AgentStats{
		AgentID:            agent.ID,
		AgentName:          agent.Name,
		Period:             win.Info(),
		Metrics:            row.Metrics,
		Previous:           row.Previous,
		RunsTrend:          row.RunsTrend,
		CostByModel:        domain.CostByModel(cur.Runs),
		SkillsUsed:         domain.TopSkills(skills),
		MemoryPulled:       domain.TopMemories(memory),
		FindingsBySeverity: domain.FindingsBySeverity(findings, win),
		FindingsByCategory: domain.FindingsByCategory(findings),
		RunHistory:         history,
	}, nil
}

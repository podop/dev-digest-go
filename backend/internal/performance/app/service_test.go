package app_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/performance/app"
	"dev-digest-go/backend/internal/performance/domain"
	"dev-digest-go/backend/internal/platform/apperr"
)

func ptr[T any](v T) *T { return &v }

var now = time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)

// fakeReader serves runs filtered by window and agent, the way the SQL does.
type fakeReader struct {
	agents   []domain.AgentRef
	runs     []domain.RunFact
	counts   map[string]domain.FindingCounts // by run agent id, "" = no agent
	memory   []json.RawMessage
	err      error
	failOnly string // when set, only this method fails
}

var _ app.Reader = (*fakeReader)(nil)

func (f *fakeReader) fails(method string) error {
	if f.err != nil && (f.failOnly == "" || f.failOnly == method) {
		return f.err
	}
	return nil
}

func (f *fakeReader) ListAgents(context.Context, string) ([]domain.AgentRef, error) {
	return f.agents, f.fails("ListAgents")
}

func (f *fakeReader) FindAgent(_ context.Context, _, id string) (domain.AgentRef, error) {
	if err := f.fails("FindAgent"); err != nil {
		return domain.AgentRef{}, err
	}
	for _, a := range f.agents {
		if a.ID == id {
			return a, nil
		}
	}
	return domain.AgentRef{}, domain.ErrNotFound
}

func (f *fakeReader) CountedRuns(_ context.Context, _ string, win domain.PeriodWindow, agentID string) ([]domain.RunFact, error) {
	var out []domain.RunFact
	for _, r := range f.runs {
		if win.Contains(r.RanAt) && (agentID == "" || (r.AgentID != nil && *r.AgentID == agentID)) {
			out = append(out, r)
		}
	}
	return out, f.fails("CountedRuns")
}

func (f *fakeReader) FindingCounts(_ context.Context, _ string, win domain.PeriodWindow, agentID string) ([]domain.FindingCounts, error) {
	// The counts belong to the current window of a preset period (the only inclusive one); the
	// previous window has none.
	var out []domain.FindingCounts
	if win.ToInclusive {
		for id, c := range f.counts {
			if agentID != "" && id != agentID {
				continue
			}
			c := c
			if id != "" {
				c.AgentID = ptr(id)
			}
			out = append(out, c)
		}
	}
	return out, f.fails("FindingCounts")
}

func (f *fakeReader) FindingFacts(context.Context, string, domain.PeriodWindow, string) ([]domain.FindingFact, error) {
	return nil, f.fails("FindingFacts")
}

func (f *fakeReader) SkillUsage(context.Context, string, domain.PeriodWindow, string) ([]domain.SkillFact, error) {
	return nil, f.fails("SkillUsage")
}

func (f *fakeReader) MemoryPulled(context.Context, string, domain.PeriodWindow, string) ([]json.RawMessage, error) {
	return f.memory, f.fails("MemoryPulled")
}

func (f *fakeReader) RunHistory(_ context.Context, _ string, _ domain.PeriodWindow, _ string, limit int) ([]domain.RunHistoryItem, error) {
	if limit != domain.RunHistoryLimit {
		return nil, errors.New("run history limit is not 20")
	}
	return nil, f.fails("RunHistory")
}

func service(r *fakeReader) *app.Service {
	return app.NewService(r, func() time.Time { return now })
}

func run(agent *string, at time.Time, cost float64) domain.RunFact {
	return domain.RunFact{AgentID: agent, Status: domain.RunDone, Source: domain.SourceLocal, CostUSD: ptr(cost), RanAt: at}
}

func agentIDs(rows []domain.Row) []string {
	var out []string
	for _, r := range rows {
		if r.AgentID == nil {
			out = append(out, "<deleted>")
		} else {
			out = append(out, *r.AgentID)
		}
	}
	return out
}

func TestDashboard_RowsAreByNameAndDeletedAgentsComeLast(t *testing.T) {
	a, b := ptr("a"), ptr("b")
	r := &fakeReader{
		agents: []domain.AgentRef{{ID: "b", Name: "beta"}, {ID: "a", Name: "alpha"}, {ID: "c", Name: "Zeta"}},
		runs: []domain.RunFact{
			run(a, now.Add(-time.Hour), 0.1), run(b, now.Add(-time.Hour), 0.2),
			run(ptr("gone"), now.Add(-time.Hour), 0.4), run(nil, now.Add(-2*time.Hour), 0.3),
		},
	}
	perf, err := service(r).Dashboard(t.Context(), "ws", domain.PeriodQuery{Key: domain.Period30d})
	if err != nil {
		t.Fatal(err)
	}
	// "Zeta" sorts before "alpha" in code-unit order.
	if diff := cmp.Diff([]string{"c", "a", "b", "<deleted>"}, agentIDs(perf.Agents)); diff != "" {
		t.Errorf("Dashboard rows mismatch (-want +got):\n%s", diff)
	}
	last := perf.Agents[3]
	if last.AgentName != domain.DeletedAgentsName || last.Metrics.Runs != 2 {
		t.Errorf("deleted-agents row = %+v, want %q with the 2 runs of no and unknown agents", last, domain.DeletedAgentsName)
	}
	if perf.Summary.Runs != 4 || perf.RunsTrend == nil || len(perf.CostByAgent) != 3 {
		t.Errorf("summary runs %d, cost_by_agent %d segments; want 4 runs and 3 segments (c has no cost)", perf.Summary.Runs, len(perf.CostByAgent))
	}
	if perf.Summary.MostActive == nil || perf.Summary.MostActive.AgentID != "a" {
		t.Errorf("most active = %+v, want a (earliest name wins the tie on the latest run)", perf.Summary.MostActive)
	}
}

func TestDashboard_WorkspaceWithoutRunsHasZeroRows(t *testing.T) {
	r := &fakeReader{agents: []domain.AgentRef{{ID: "a", Name: "alpha"}, {ID: "b", Name: "beta"}}}
	perf, err := service(r).Dashboard(t.Context(), "ws", domain.PeriodQuery{Key: domain.Period1d})
	if err != nil {
		t.Fatal(err)
	}
	if len(perf.Agents) != 2 {
		t.Fatalf("rows = %d, want one per agent and no deleted-agents row", len(perf.Agents))
	}
	for _, row := range perf.Agents {
		if row.Metrics.Runs != 0 || row.Metrics.TotalCostUSD != nil || row.Metrics.AcceptRate != nil || row.Metrics.SmallSample {
			t.Errorf("row %s = %+v, want zeros and nulls", row.AgentName, row.Metrics)
		}
		if len(row.RunsTrend) != 24 {
			t.Errorf("row %s trend = %d points, want 24 hourly slices", row.AgentName, len(row.RunsTrend))
		}
	}
	if perf.Summary.TotalCostUSD != nil || perf.Summary.AvgAcceptRate != nil || perf.Summary.MostActive != nil {
		t.Errorf("summary = %+v, want null cost, rate and most active", perf.Summary)
	}
	if perf.CostByAgent == nil || perf.CostByModel == nil {
		t.Error("cost segments are nil, want empty slices")
	}
}

func TestDashboard_DeletedAgentsOnlyInThePreviousWindow(t *testing.T) {
	r := &fakeReader{
		agents: []domain.AgentRef{{ID: "a", Name: "alpha"}},
		runs: []domain.RunFact{
			run(ptr("gone"), now.Add(-31*24*time.Hour), 0.5), // previous window of 30d
			run(ptr("a"), now.Add(-time.Hour), 0.1),
		},
	}
	perf, err := service(r).Dashboard(t.Context(), "ws", domain.PeriodQuery{Key: domain.Period30d})
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff([]string{"a"}, agentIDs(perf.Agents)); diff != "" {
		t.Errorf("rows mismatch (-want +got):\n%s", diff)
	}
	if perf.Summary.Runs != 1 || perf.PreviousSummary.Runs != 1 || perf.PreviousSummary.TotalCostUSD == nil || *perf.PreviousSummary.TotalCostUSD != 0.5 {
		t.Errorf("summary %+v, previous summary %+v; want the deleted agents' previous run only in the previous summary", perf.Summary, perf.PreviousSummary)
	}
}

func TestDashboard_WindowEdges(t *testing.T) {
	from := now.Add(-48 * time.Hour)
	to := now.Add(-24 * time.Hour)
	a := ptr("a")
	r := &fakeReader{
		agents: []domain.AgentRef{{ID: "a", Name: "alpha"}},
		runs:   []domain.RunFact{run(a, from, 0.1), run(a, to, 0.1), run(a, to.Add(-time.Millisecond), 0.1), run(a, from.Add(-time.Millisecond), 0.1)},
	}
	t.Run("custom: from is in, to is out, from-1ms is the previous window", func(t *testing.T) {
		perf, err := service(r).Dashboard(t.Context(), "ws", domain.PeriodQuery{Key: domain.PeriodCustom, From: &from, To: &to})
		if err != nil {
			t.Fatal(err)
		}
		row := perf.Agents[0]
		if row.Metrics.Runs != 2 || row.Previous.Runs != 1 {
			t.Errorf("runs %d, previous %d; want 2 and 1", row.Metrics.Runs, row.Previous.Runs)
		}
	})
	t.Run("preset: a run at now is counted", func(t *testing.T) {
		r := &fakeReader{agents: r.agents, runs: []domain.RunFact{run(a, now, 0.1)}}
		perf, err := service(r).Dashboard(t.Context(), "ws", domain.PeriodQuery{Key: domain.Period1d})
		if err != nil {
			t.Fatal(err)
		}
		if got := perf.Agents[0].Metrics.Runs; got != 1 {
			t.Errorf("runs at exactly now = %d, want 1", got)
		}
	})
}

func TestStats_EqualsTheDashboardRow(t *testing.T) {
	a := ptr("a")
	r := &fakeReader{
		agents: []domain.AgentRef{{ID: "a", Name: "alpha"}, {ID: "b", Name: "beta"}},
		runs: []domain.RunFact{
			run(a, now.Add(-time.Hour), 0.1), run(a, now.Add(-2*time.Hour), 0.2), run(ptr("b"), now.Add(-time.Hour), 0.3),
		},
		counts: map[string]domain.FindingCounts{"a": {Total: 4, Accepted: 1, Dismissed: 1}},
	}
	q := domain.PeriodQuery{Key: domain.Period30d}
	svc := service(r)
	perf, err := svc.Dashboard(t.Context(), "ws", q)
	if err != nil {
		t.Fatal(err)
	}
	stats, err := svc.Stats(t.Context(), "ws", "a", q)
	if err != nil {
		t.Fatal(err)
	}
	row := perf.Agents[0]
	if diff := cmp.Diff(row.Metrics, stats.Metrics); diff != "" {
		t.Errorf("Stats metrics differ from the dashboard row (-row +stats):\n%s", diff)
	}
	if diff := cmp.Diff(row.Previous, stats.Previous); diff != "" {
		t.Errorf("Stats previous differ from the dashboard row (-row +stats):\n%s", diff)
	}
	if diff := cmp.Diff(row.RunsTrend, stats.RunsTrend); diff != "" {
		t.Errorf("Stats trend differs from the dashboard row (-row +stats):\n%s", diff)
	}
	if stats.AgentID != "a" || stats.AgentName != "alpha" || stats.Metrics.Runs != 2 || stats.Metrics.Pending != 2 {
		t.Errorf("Stats = %+v", stats)
	}
}

func TestStats_UnknownAgentIsNotFound(t *testing.T) {
	_, err := service(&fakeReader{}).Stats(t.Context(), "ws", "nope", domain.PeriodQuery{Key: domain.Period1d})
	ae, ok := apperr.As(err)
	if !ok || ae.Kind != apperr.KindNotFound || ae.Message != "Agent not found" {
		t.Errorf("Stats(unknown) error = %v, want not_found \"Agent not found\"", err)
	}
}

func TestService_ReaderFailuresAreReturnedWrapped(t *testing.T) {
	boom := errors.New("boom")
	for _, method := range []string{"ListAgents", "CountedRuns", "FindingCounts"} {
		t.Run("Dashboard "+method, func(t *testing.T) {
			r := &fakeReader{agents: []domain.AgentRef{{ID: "a", Name: "a"}}, err: boom, failOnly: method}
			if _, err := service(r).Dashboard(t.Context(), "ws", domain.PeriodQuery{Key: domain.Period1d}); !errors.Is(err, boom) {
				t.Errorf("Dashboard error = %v, want it to wrap %v", err, boom)
			}
		})
	}
	for _, method := range []string{"FindAgent", "CountedRuns", "FindingCounts", "FindingFacts", "SkillUsage", "MemoryPulled", "RunHistory"} {
		t.Run("Stats "+method, func(t *testing.T) {
			r := &fakeReader{agents: []domain.AgentRef{{ID: "a", Name: "a"}}, err: boom, failOnly: method}
			_, err := service(r).Stats(t.Context(), "ws", "a", domain.PeriodQuery{Key: domain.Period1d})
			if !errors.Is(err, boom) {
				t.Errorf("Stats error = %v, want it to wrap %v", err, boom)
			}
			if _, isApp := apperr.As(err); isApp {
				t.Errorf("Stats error %v is an apperr, want a plain error (a 500 without details)", err)
			}
		})
	}
}

func TestService_UnresolvableWindow(t *testing.T) {
	_, err := service(&fakeReader{}).Dashboard(t.Context(), "ws", domain.PeriodQuery{Key: domain.PeriodCustom})
	if !errors.Is(err, domain.ErrInvalidWindow) {
		t.Errorf("Dashboard(custom without instants) error = %v, want ErrInvalidWindow", err)
	}
}

func TestStats_MemoryAndHistoryLimitReachTheReader(t *testing.T) {
	r := &fakeReader{
		agents: []domain.AgentRef{{ID: "a", Name: "alpha"}},
		memory: []json.RawMessage{json.RawMessage(`[{"text":"x"}]`), json.RawMessage(`[{"text":"x"},{"text":"y"}]`)},
	}
	stats, err := service(r).Stats(t.Context(), "ws", "a", domain.PeriodQuery{Key: domain.Period30d})
	if err != nil {
		t.Fatal(err)
	}
	want := []domain.UsageCount{{Key: "x", Label: "x", Runs: 2}, {Key: "y", Label: "y", Runs: 1}}
	if diff := cmp.Diff(want, stats.MemoryPulled); diff != "" {
		t.Errorf("MemoryPulled mismatch (-want +got):\n%s", diff)
	}
}

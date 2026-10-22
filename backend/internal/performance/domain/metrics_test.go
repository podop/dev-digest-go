package domain_test

import (
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/performance/domain"
)

func ptr[T any](v T) *T { return &v }

func TestRoundMoney(t *testing.T) {
	// Expected values are Math.round(n * 10 ** 6) / 10 ** 6 in Node 22.
	tests := []struct{ in, want float64 }{
		{0.0000005, 0.000001},
		{0.0000015, 0.000002},
		{0.0000025, 0.000003},
		{1e-7, 0},
		{0.0000004999999, 0},
		{0.1 + 0.2, 0.3},
		{1234.5678915, 1234.567892},
		{12.3456785, 12.345679},
		{0.049999995, 0.05},
		{0.4999999999999999, 0.5},
		{1.005, 1.005},
	}
	for _, tc := range tests {
		if got := domain.RoundMoney(tc.in); got != tc.want {
			t.Errorf("RoundMoney(%v) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func run(agent string, status domain.RunStatus, source domain.RunSource, cost *float64, dur *int, at time.Time) domain.RunFact {
	return domain.RunFact{AgentID: &agent, Status: status, Source: source, CostUSD: cost, DurationMs: dur, RanAt: at}
}

func TestAgentMetrics(t *testing.T) {
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	runs := []domain.RunFact{
		run("a", domain.RunDone, domain.SourceLocal, ptr(0.1), ptr(1), at),
		run("a", domain.RunDone, domain.SourceCI, ptr(0.2), ptr(2), at.Add(time.Hour)),
		run("a", domain.RunFailed, domain.SourceCI, nil, ptr(1000), at.Add(30*time.Minute)),
		run("a", domain.RunCancelled, domain.SourceLocal, ptr(0.0000005), nil, at.Add(-time.Hour)),
	}
	t.Run("folds", func(t *testing.T) {
		got := domain.AgentMetrics(runs, &domain.FindingCounts{Total: 9, Accepted: 2, Dismissed: 1})
		want := domain.Metrics{
			Runs: 4, RunsDone: 2, RunsFailed: 1, RunsCancelled: 1, RunsLocal: 2, RunsCI: 2,
			PricedRuns: 3, UnpricedRuns: 1,
			TotalCostUSD: ptr(0.300001), AvgCostUSD: ptr(0.1), AvgDurationMs: ptr(2), // round(1.5) = 2: ties go up
			LastRunAt:     ptr(at.Add(time.Hour)),
			FindingsTotal: 9, Accepted: 2, Dismissed: 1, Pending: 6, Decisions: 3,
			AcceptRate: ptr(2.0 / 3.0), SmallSample: true,
		}
		if diff := cmp.Diff(want, got); diff != "" {
			t.Errorf("AgentMetrics mismatch (-want +got):\n%s", diff)
		}
	})
	t.Run("no runs, no findings", func(t *testing.T) {
		if diff := cmp.Diff(domain.Metrics{}, domain.AgentMetrics(nil, nil)); diff != "" {
			t.Errorf("AgentMetrics(nil, nil) mismatch (-want +got):\n%s", diff)
		}
	})
	t.Run("small sample bounds", func(t *testing.T) {
		for _, tc := range []struct {
			accepted, dismissed int
			want                bool
		}{{0, 0, false}, {1, 0, true}, {2, 2, true}, {3, 2, false}, {0, 5, false}} {
			m := domain.AgentMetrics(nil, &domain.FindingCounts{Total: 10, Accepted: tc.accepted, Dismissed: tc.dismissed})
			if m.SmallSample != tc.want {
				t.Errorf("SmallSample with %d accepted, %d dismissed = %v, want %v", tc.accepted, tc.dismissed, m.SmallSample, tc.want)
			}
		}
	})
}

func TestGroupFacts_DeletedAgents(t *testing.T) {
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	gone := "gone"
	runs := []domain.RunFact{
		run("a", domain.RunDone, domain.SourceLocal, nil, nil, at),
		{AgentID: nil, Status: domain.RunDone, Source: domain.SourceLocal, RanAt: at},
		{AgentID: &gone, Status: domain.RunFailed, Source: domain.SourceCI, RanAt: at},
	}
	counts := []domain.FindingCounts{
		{AgentID: &gone, Total: 2, Accepted: 1}, {AgentID: nil, Total: 3, Dismissed: 2}, {AgentID: ptr("a"), Total: 1},
	}
	g := domain.GroupFacts(runs, counts, map[string]bool{"a": true})
	if !g.HasDeleted() {
		t.Fatal("HasDeleted = false, want true")
	}
	if got := len(g.Facts(nil).Runs); got != 2 {
		t.Errorf("deleted-agents runs = %d, want 2 (no agent and unknown agent)", got)
	}
	if diff := cmp.Diff(&domain.FindingCounts{Total: 5, Accepted: 1, Dismissed: 2}, g.Facts(nil).Findings); diff != "" {
		t.Errorf("deleted-agents finding counts mismatch (-want +got):\n%s", diff)
	}
	if got := len(g.Facts(ptr("a")).Runs); got != 1 {
		t.Errorf("agent a runs = %d, want 1", got)
	}
	if domain.GroupFacts(nil, counts, map[string]bool{"a": true, "gone": true}).HasDeleted() {
		t.Error("HasDeleted = true for finding counts alone, want false (only runs create the row)")
	}
}

func row(id, name string, runs int, last time.Time, cost *float64, priced int) domain.Row {
	r := domain.Row{AgentName: name, Metrics: domain.Metrics{Runs: runs, TotalCostUSD: cost, PricedRuns: priced}}
	if id != "" {
		r.AgentID = &id
	}
	if runs > 0 {
		r.Metrics.LastRunAt = &last
	}
	return r
}

func TestFindMostActive(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	tests := []struct {
		name string
		rows []domain.Row
		want string // agent id, "" for nil
	}{
		{"most runs", []domain.Row{row("a", "a", 1, t0, nil, 0), row("b", "b", 2, t0, nil, 0)}, "b"},
		{"later last run wins a tie", []domain.Row{row("a", "a", 2, t0, nil, 0), row("b", "b", 2, t0.Add(time.Second), nil, 0)}, "b"},
		{"name in code-unit order decides", []domain.Row{row("b", "b", 2, t0, nil, 0), row("a", "a", 2, t0, nil, 0), row("c", "A", 2, t0, nil, 0)}, "c"},
		{"deleted agents never qualify", []domain.Row{row("", "Deleted agents", 9, t0, nil, 0), row("a", "a", 1, t0, nil, 0)}, "a"},
		{"agents without runs never qualify", []domain.Row{row("a", "a", 0, t0, nil, 0)}, ""},
		{"no rows", nil, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := domain.FindMostActive(tc.rows)
			switch {
			case tc.want == "" && got != nil:
				t.Errorf("FindMostActive = %+v, want nil", got)
			case tc.want != "" && (got == nil || got.AgentID != tc.want):
				t.Errorf("FindMostActive = %+v, want agent %q", got, tc.want)
			}
		})
	}
}

func TestSummarize(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	a := row("a", "a", 2, t0, ptr(0.1), 1)
	a.Metrics.Accepted, a.Metrics.Dismissed, a.Metrics.Pending = 3, 1, 2
	b := row("b", "b", 1, t0, ptr(0.2), 1)
	b.Metrics.Accepted, b.Metrics.Pending = 1, 4
	c := row("c", "c", 3, t0, nil, 0) // unpriced: contributes no cost
	got := domain.Summarize([]domain.Row{a, b, c})
	if got.Runs != 6 || got.Accepted != 4 || got.Dismissed != 1 || got.Pending != 6 || got.Decisions != 5 {
		t.Errorf("Summarize sums = %+v", got)
	}
	if got.TotalCostUSD == nil || *got.TotalCostUSD != 0.3 {
		t.Errorf("Summarize total cost = %v, want 0.3 (rounded running sum)", got.TotalCostUSD)
	}
	if got.AvgAcceptRate == nil || *got.AvgAcceptRate != 0.8 {
		t.Errorf("Summarize accept rate = %v, want the pooled 4/5", got.AvgAcceptRate)
	}
	if got.MostActive == nil || got.MostActive.AgentID != "c" {
		t.Errorf("Summarize most active = %+v, want c", got.MostActive)
	}
	if empty := domain.Summarize(nil); empty.TotalCostUSD != nil || empty.AvgAcceptRate != nil || empty.MostActive != nil {
		t.Errorf("Summarize(nil) = %+v, want null cost, rate and most active", empty)
	}
}

func TestCostSegments(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	t.Run("by agent: value descending, zero dropped, deleted agents keyless", func(t *testing.T) {
		rows := []domain.Row{
			row("a", "alpha", 1, t0, ptr(0.5), 1), row("b", "beta", 1, t0, ptr(0.5), 2),
			row("c", "free", 1, t0, nil, 0), row("", "Deleted agents", 1, t0, ptr(0.9), 3),
		}
		var keys []string
		for _, s := range domain.CostByAgent(rows) {
			if s.Key == nil {
				keys = append(keys, "<nil>")
			} else {
				keys = append(keys, *s.Key)
			}
		}
		if diff := cmp.Diff([]string{"<nil>", "a", "b"}, keys); diff != "" {
			t.Errorf("CostByAgent keys mismatch (-want +got):\n%s", diff)
		}
	})
	t.Run("by model: priced runs grouped by model, null model last on a tie", func(t *testing.T) {
		m1, m2 := "m1", "m2"
		runs := []domain.RunFact{
			{Model: &m2, CostUSD: ptr(0.1)}, {Model: &m1, CostUSD: ptr(0.1)}, {Model: nil, CostUSD: ptr(0.1)},
			{Model: &m1, CostUSD: ptr(0.2)}, {Model: &m2}, {Model: ptr("free"), CostUSD: ptr(0.0)},
		}
		got := domain.CostByModel(runs)
		want := []domain.CostSegment{
			{Key: &m1, Label: &m1, Value: 0.3, Runs: 2},
			{Key: &m2, Label: &m2, Value: 0.1, Runs: 1},
			{Value: 0.1, Runs: 1},
		}
		if diff := cmp.Diff(want, got); diff != "" {
			t.Errorf("CostByModel mismatch (-want +got):\n%s", diff)
		}
	})
}

func TestSortAgents_CodeUnitOrder(t *testing.T) {
	in := []domain.AgentRef{{ID: "4", Name: "no-then"}, {ID: "3", Name: "n-plus"}, {ID: "2", Name: "Zeta"}, {ID: "1", Name: "same"}, {ID: "0", Name: "same"}, {ID: "5", Name: "\U0001F600"}, {ID: "6", Name: "～"}}
	var ids []string
	for _, a := range domain.SortAgents(in) {
		ids = append(ids, a.ID)
	}
	// "-" sorts before "o" (a collation would ignore it); U+1F600 (surrogate pair) before U+FF5E.
	if diff := cmp.Diff([]string{"2", "3", "4", "0", "1", "5", "6"}, ids); diff != "" {
		t.Errorf("SortAgents order mismatch (-want +got):\n%s", diff)
	}
}

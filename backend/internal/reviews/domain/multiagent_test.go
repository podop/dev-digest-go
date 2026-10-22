package domain_test

import (
	"errors"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	reviewer "dev-digest-go/backend/internal/reviewer/domain"
	"dev-digest-go/backend/internal/reviews/domain"
)

func TestOrderColumns_EnglishCollationLikeLocaleCompare(t *testing.T) {
	// The order is what Node's names.sort((a, b) => a.localeCompare(b, 'en')) gives (Node 22, ICU).
	nodeOrder := []string{
		"_x", "10", "9", "alpha", "Alpha", "beta 1", "beta-2", "beta1", "eclair", "éclair", "Éclair",
		"n-plus-one-gate", "no-then-chains", "security reviewer", "Security Reviewer", "security-reviewer", "Zed",
	}
	shuffled := []string{
		"Zed", "beta1", "no-then-chains", "Éclair", "alpha", "security-reviewer", "9", "eclair", "Security Reviewer",
		"_x", "n-plus-one-gate", "beta-2", "10", "éclair", "Alpha", "security reviewer", "beta 1",
	}
	cols := make([]domain.AgentColumn, len(shuffled))
	for i, n := range shuffled {
		cols[i] = domain.AgentColumn{RunID: "r" + n, AgentName: n}
	}
	got := domain.OrderColumns(cols)
	names := make([]string, len(got))
	for i, c := range got {
		names[i] = c.AgentName
	}
	if diff := cmp.Diff(nodeOrder, names); diff != "" {
		t.Errorf("OrderColumns() names mismatch (-want +got):\n%s", diff)
	}
	if cols[0].AgentName != "Zed" {
		t.Errorf("OrderColumns() modified its input: first = %q, want Zed", cols[0].AgentName)
	}
}

func TestOrderColumns_TiesGoByRunIDAndGoneAgentsComeFirst(t *testing.T) {
	cols := []domain.AgentColumn{
		{RunID: "r3", AgentName: "same"},
		{RunID: "r9", AgentName: ""},
		{RunID: "r1", AgentName: "same"},
		{RunID: "r2", AgentName: "same"},
		{RunID: "r0", AgentName: ""},
	}
	var ids []string
	for _, c := range domain.OrderColumns(cols) {
		ids = append(ids, c.RunID)
	}
	if diff := cmp.Diff([]string{"r0", "r9", "r1", "r2", "r3"}, ids); diff != "" {
		t.Errorf("OrderColumns() run ids mismatch (-want +got):\n%s", diff)
	}
}

func TestRunTotals(t *testing.T) {
	tests := []struct {
		name     string
		cols     []domain.AgentColumn
		wantDur  int
		wantCost *float64
	}{
		{"none", nil, 0, nil},
		{"longest wins, costs add", []domain.AgentColumn{
			{DurationMS: ptr(1000), CostUSD: ptr(0.25)}, {DurationMS: ptr(3000), CostUSD: ptr(0.5)}, {DurationMS: ptr(2000), CostUSD: ptr(0.125)},
		}, 3000, ptr(0.875)},
		{"nulls count as zero duration and are skipped in the cost", []domain.AgentColumn{
			{}, {DurationMS: ptr(10)}, {CostUSD: ptr(0.5)},
		}, 10, ptr(0.5)},
		{"no cost at all is null", []domain.AgentColumn{{DurationMS: ptr(5)}, {DurationMS: ptr(7)}}, 7, nil},
		{"a zero cost is a cost", []domain.AgentColumn{{CostUSD: ptr(0.0)}}, 0, ptr(0.0)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dur, cost := domain.RunTotals(tc.cols)
			if dur != tc.wantDur {
				t.Errorf("RunTotals() duration = %d, want %d", dur, tc.wantDur)
			}
			if diff := cmp.Diff(tc.wantCost, cost); diff != "" {
				t.Errorf("RunTotals() cost mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func finding(id, file string, start, end int, sev reviewer.Severity, title string) domain.FindingRecord {
	return domain.FindingRecord{ID: id, File: file, StartLine: start, EndLine: end, Severity: sev, Title: title}
}

func col(runID, agentID, name string, status domain.RunStatus, fs ...domain.FindingRecord) domain.AgentColumn {
	return domain.AgentColumn{RunID: runID, AgentID: agentID, AgentName: name, Status: status, Findings: fs}
}

func TestGroupFindings_AC4063(t *testing.T) {
	c1 := col("r1", "a1", "Alpha", domain.StatusDone,
		finding("A", "f.go", 10, 12, reviewer.SeverityWarning, "title A"),
		finding("B", "f.go", 12, 20, reviewer.SeverityWarning, "title B"))
	c2 := col("r2", "a2", "Beta", domain.StatusDone,
		finding("C", "f.go", 19, 25, reviewer.SeverityCritical, "title C"))
	want := []domain.Conflict{{
		File: "f.go", Line: 10, EndLine: 25, Title: "title C", IsConflict: true,
		Takes: []domain.ConflictTake{
			{AgentID: "a1", Persona: "Alpha", RunID: "r1", Verdict: "WARNING", Note: "title A", FindingIDs: []string{"A", "B"}},
			{AgentID: "a2", Persona: "Beta", RunID: "r2", Verdict: "CRITICAL", Note: "title C", FindingIDs: []string{"C"}},
		},
	}}
	if diff := cmp.Diff(want, domain.GroupFindings([]domain.AgentColumn{c1, c2})); diff != "" {
		t.Errorf("GroupFindings() mismatch (-want +got):\n%s", diff)
	}
}

func TestGroupFindings_AgreementIsNoConflict(t *testing.T) {
	c1 := col("r1", "a1", "Alpha", domain.StatusDone, finding("A", "f.go", 5, 9, reviewer.SeverityWarning, "first"))
	c2 := col("r2", "a2", "Beta", domain.StatusDone, finding("B", "f.go", 5, 9, reviewer.SeverityWarning, "second"))
	got := domain.GroupFindings([]domain.AgentColumn{c1, c2})
	if len(got) != 1 || got[0].IsConflict {
		t.Fatalf("GroupFindings() = %+v, want one group that is not a conflict", got)
	}
	if got[0].Title != "first" {
		t.Errorf("GroupFindings()[0].Title = %q, want the earlier column's title on a tie", got[0].Title)
	}
}

func TestGroupFindings(t *testing.T) {
	warn, crit, sug := reviewer.SeverityWarning, reviewer.SeverityCritical, reviewer.SeveritySuggestion
	t.Run("an agent that flagged nothing ignored the group", func(t *testing.T) {
		c1 := col("r1", "a1", "Alpha", domain.StatusDone, finding("A", "f.go", 1, 1, sug, "only one"))
		c2 := col("r2", "a2", "Beta", domain.StatusDone)
		got := domain.GroupFindings([]domain.AgentColumn{c1, c2})
		want := []domain.Conflict{{
			File: "f.go", Line: 1, EndLine: 1, Title: "only one", IsConflict: true,
			Takes: []domain.ConflictTake{
				{AgentID: "a1", Persona: "Alpha", RunID: "r1", Verdict: "SUGGESTION", Note: "only one", FindingIDs: []string{"A"}},
				{AgentID: "a2", Persona: "Beta", RunID: "r2", Verdict: "ignored", Note: "", FindingIDs: []string{}},
			},
		}}
		if diff := cmp.Diff(want, got); diff != "" {
			t.Errorf("GroupFindings() mismatch (-want +got):\n%s", diff)
		}
	})
	t.Run("only done columns count, takes are per done column", func(t *testing.T) {
		c1 := col("r1", "a1", "Alpha", domain.StatusFailed, finding("A", "f.go", 1, 1, crit, "failed run"))
		c2 := col("r2", "a2", "Beta", domain.StatusDone, finding("B", "f.go", 1, 1, warn, "done run"))
		c3 := col("r3", "a3", "Gamma", domain.StatusRunning, finding("C", "f.go", 1, 1, crit, "running"))
		got := domain.GroupFindings([]domain.AgentColumn{c1, c2, c3})
		if len(got) != 1 || len(got[0].Takes) != 1 || got[0].Takes[0].RunID != "r2" || got[0].IsConflict {
			t.Errorf("GroupFindings() = %+v, want one group, one take (r2), no conflict", got)
		}
	})
	t.Run("adjacent but not overlapping ranges are separate groups; groups are ordered by file then line", func(t *testing.T) {
		c1 := col("r1", "a1", "Alpha", domain.StatusDone,
			finding("A", "b.go", 1, 3, warn, "b1"),
			finding("B", "a.go", 4, 8, warn, "a2"),
			finding("C", "a.go", 9, 9, warn, "a3"),
			finding("D", "a.go", 1, 3, warn, "a1"))
		got := domain.GroupFindings([]domain.AgentColumn{c1})
		var titles []string
		for _, g := range got {
			titles = append(titles, g.File+":"+g.Title)
		}
		if diff := cmp.Diff([]string{"a.go:a1", "a.go:a2", "a.go:a3", "b.go:b1"}, titles); diff != "" {
			t.Errorf("GroupFindings() groups mismatch (-want +got):\n%s", diff)
		}
	})
	t.Run("a long finding absorbs later starts (running maximum end line)", func(t *testing.T) {
		c1 := col("r1", "a1", "Alpha", domain.StatusDone, finding("A", "f.go", 1, 50, warn, "wide"))
		c2 := col("r2", "a2", "Beta", domain.StatusDone,
			finding("B", "f.go", 10, 11, warn, "n1"),
			finding("C", "f.go", 40, 41, warn, "n2"))
		got := domain.GroupFindings([]domain.AgentColumn{c1, c2})
		if len(got) != 1 || got[0].Line != 1 || got[0].EndLine != 50 {
			t.Errorf("GroupFindings() = %+v, want one group 1-50", got)
		}
	})
	t.Run("the best finding of a take is the highest severity, ties to the earlier one", func(t *testing.T) {
		c1 := col("r1", "a1", "Alpha", domain.StatusDone,
			finding("A", "f.go", 1, 5, warn, "early warning"),
			finding("B", "f.go", 2, 5, crit, "first critical"),
			finding("C", "f.go", 3, 5, crit, "second critical"))
		got := domain.GroupFindings([]domain.AgentColumn{c1})
		if len(got) != 1 || got[0].Takes[0].Note != "first critical" || got[0].Takes[0].Verdict != "CRITICAL" {
			t.Errorf("GroupFindings() = %+v, want the first critical as the take", got)
		}
		if diff := cmp.Diff([]string{"A", "B", "C"}, got[0].Takes[0].FindingIDs); diff != "" {
			t.Errorf("GroupFindings() finding ids mismatch (-want +got):\n%s", diff)
		}
	})
	t.Run("files compare in UTF-16 order", func(t *testing.T) {
		emoji, private := string(rune(0x1F600))+".go", string(rune(0xE000))+".go"
		c1 := col("r1", "a1", "Alpha", domain.StatusDone,
			finding("A", private, 1, 1, warn, "private"),
			finding("B", emoji, 1, 1, warn, "emoji"))
		got := domain.GroupFindings([]domain.AgentColumn{c1})
		if len(got) != 2 || got[0].Title != "emoji" {
			t.Errorf("GroupFindings() = %+v, want the supplementary file first", got)
		}
	})
	t.Run("nothing to group is an empty non-nil slice", func(t *testing.T) {
		for _, cols := range [][]domain.AgentColumn{nil, {col("r1", "a1", "A", domain.StatusDone)}, {col("r1", "a1", "A", domain.StatusFailed, finding("A", "f", 1, 1, crit, "x"))}} {
			if got := domain.GroupFindings(cols); got == nil || len(got) != 0 {
				t.Errorf("GroupFindings(%d columns) = %#v, want empty non-nil", len(cols), got)
			}
		}
	})
}

func TestSortFindings(t *testing.T) {
	emoji, private := string(rune(0x1F600)), string(rune(0xE000))
	fs := []domain.FindingRecord{
		finding("z", "b.go", 1, 1, "", ""), finding("y", "a.go", 9, 9, "", ""), finding("b", "a.go", 2, 2, "", ""),
		finding("a", "a.go", 2, 2, "", ""), finding("p", private, 1, 1, "", ""), finding("e", emoji, 1, 1, "", ""),
	}
	domain.SortFindings(fs)
	var ids []string
	for _, f := range fs {
		ids = append(ids, f.ID)
	}
	if diff := cmp.Diff([]string{"a", "b", "y", "z", "e", "p"}, ids); diff != "" {
		t.Errorf("SortFindings() order mismatch (-want +got):\n%s", diff)
	}
}

func TestBuildMultiAgentRun(t *testing.T) {
	header := domain.MultiAgentHeader{ID: "m1", PRID: "p1", RepoID: "repo1", PRNumber: ptr(482), PRTitle: ptr("T"), RanAt: time.Date(2026, 10, 22, 1, 2, 3, 0, time.UTC)}
	b := col("r2", "a2", "Beta", domain.StatusDone, finding("B", "f.go", 3, 4, reviewer.SeverityWarning, "tb"))
	b.DurationMS, b.CostUSD = ptr(4000), ptr(0.5)
	a := col("r1", "a1", "alpha", domain.StatusFailed)
	a.DurationMS = ptr(9000)
	got := domain.BuildMultiAgentRun(header, []domain.AgentColumn{b, a})
	if got.MultiAgentHeader != header || got.AgentCount != 2 || got.TotalDurationMS != 9000 {
		t.Errorf("BuildMultiAgentRun() = %+v, want the header, 2 agents, 9000 ms", got)
	}
	if diff := cmp.Diff(ptr(0.5), got.TotalCostUSD); diff != "" {
		t.Errorf("BuildMultiAgentRun().TotalCostUSD mismatch (-want +got):\n%s", diff)
	}
	if got.Columns[0].RunID != "r1" || got.Columns[1].RunID != "r2" {
		t.Errorf("BuildMultiAgentRun() column order = %s, %s, want r1, r2", got.Columns[0].RunID, got.Columns[1].RunID)
	}
	if len(got.Conflicts) != 1 || len(got.Conflicts[0].Takes) != 1 {
		t.Errorf("BuildMultiAgentRun().Conflicts = %+v, want one group over the done column", got.Conflicts)
	}
	empty := domain.BuildMultiAgentRun(header, nil)
	if empty.Columns == nil || empty.Conflicts == nil || empty.AgentCount != 0 || empty.TotalCostUSD != nil {
		t.Errorf("BuildMultiAgentRun(nil) = %+v, want empty non-nil columns and conflicts", empty)
	}
}

func TestListLimit(t *testing.T) {
	tests := []struct {
		name    string
		in      *int
		want    int
		wantErr bool
	}{
		{"default", nil, 20, false},
		{"min", ptr(1), 1, false},
		{"max", ptr(20), 20, false},
		{"zero (EC4031)", ptr(0), 0, true},
		{"too big (EC4031)", ptr(21), 0, true},
		{"negative", ptr(-1), 0, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := domain.ListLimit(tc.in)
			if (err != nil) != tc.wantErr || got != tc.want {
				t.Errorf("ListLimit() = %d, %v, want %d, error %v", got, err, tc.want, tc.wantErr)
			}
			if tc.wantErr && (!errors.Is(err, domain.ErrInvalidLimit) || err.Error() != "limit must be between 1 and 20") {
				t.Errorf("ListLimit() error = %v, want ErrInvalidLimit \"limit must be between 1 and 20\"", err)
			}
		})
	}
}

func TestEstimates_AC4065(t *testing.T) {
	base := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	var runs []domain.EstimateRun
	// 12 done runs of agent "a": durations 1..12 s, the newest (12 s) first by ran_at; run 7 has no cost.
	for i := 1; i <= 12; i++ {
		r := domain.EstimateRun{ID: "run", AgentID: "a", RanAt: base.Add(time.Duration(i) * time.Minute), DurationMS: ptr(i * 1000), CostUSD: ptr(0.01 * float64(i))}
		if i == 7 {
			r.CostUSD = nil
		}
		runs = append(runs, r)
	}
	// Runs that must not count: no duration and no cost, and no agent.
	runs = append(runs,
		domain.EstimateRun{ID: "x", AgentID: "a", RanAt: base.Add(time.Hour)},
		domain.EstimateRun{ID: "y", RanAt: base, DurationMS: ptr(99)},
		domain.EstimateRun{ID: "z", AgentID: "b", RanAt: base, CostUSD: ptr(0.000123)},
	)
	got := domain.Estimates(runs)
	if len(got) != 2 || got[0].AgentID != "a" || got[1].AgentID != "b" {
		t.Fatalf("Estimates() agents = %+v, want a, b in order", got)
	}
	// The newest 10 are durations 3..12 s: mean 7.5 s. Costs of 3..12 without run 7:
	// (0.03+0.04+0.05+0.06+0.08+0.09+0.10+0.11+0.12) / 9.
	if a := got[0]; a.SampleSize != 10 || a.AvgDurationMS == nil || *a.AvgDurationMS != 7500 {
		t.Errorf("Estimates() agent a = %+v, want sample 10, average duration 7500", a)
	}
	wantCost := 0.68 / 9
	if a := got[0]; a.AvgCostUSD == nil || *a.AvgCostUSD != wantCost {
		t.Errorf("Estimates() agent a average cost = %v, want %v", a.AvgCostUSD, wantCost)
	}
	if b := got[1]; b.SampleSize != 1 || b.AvgDurationMS != nil || b.AvgCostUSD == nil || *b.AvgCostUSD != 0.000123 {
		t.Errorf("Estimates() agent b = %+v, want sample 1, no duration average, cost 0.000123", b)
	}
}

func TestEstimates_TiesGoByRunIDAndNothingIsEmpty(t *testing.T) {
	same := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	var runs []domain.EstimateRun
	for i := range 11 {
		runs = append(runs, domain.EstimateRun{ID: string(rune('a' + i)), AgentID: "a", RanAt: same, DurationMS: ptr(i)})
	}
	got := domain.Estimates(runs)
	// The last run id ("k", duration 10) is the one left out: the mean of 0..9.
	if len(got) != 1 || got[0].SampleSize != 10 || *got[0].AvgDurationMS != 4.5 {
		t.Errorf("Estimates() = %+v, want sample 10, average 4.5", got)
	}
	if got := domain.Estimates(nil); got == nil || len(got) != 0 {
		t.Errorf("Estimates(nil) = %#v, want empty non-nil", got)
	}
}

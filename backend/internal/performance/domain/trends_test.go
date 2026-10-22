package domain_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/performance/domain"
)

func win(from, to time.Time, inclusive bool) domain.PeriodWindow {
	return domain.PeriodWindow{Key: domain.PeriodCustom, From: from, To: to, ToInclusive: inclusive}
}

func TestRunsTrend_SliceCount(t *testing.T) {
	from := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	tests := []struct {
		name string
		span time.Duration
		want int
	}{
		{"1 ms: at least two slices", time.Millisecond, 2},
		{"90 minutes: two hours", 90 * time.Minute, 2},
		{"3 hours", 3 * time.Hour, 3},
		{"24 hours", 24 * time.Hour, 24},
		{"exactly 48 hours is hourly", 48 * time.Hour, 48},
		{"48 hours and 1 ms is daily", 48*time.Hour + time.Millisecond, 3},
		{"7 days", 7 * 24 * time.Hour, 7},
		{"366 days", 366 * 24 * time.Hour, 366},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := len(domain.RunsTrend(nil, win(from, from.Add(tc.span), false))); got != tc.want {
				t.Errorf("slices for %v = %d, want %d", tc.span, got, tc.want)
			}
		})
	}
}

func TestRunsTrend_Placement(t *testing.T) {
	from := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	to := from.Add(3 * time.Hour)
	at := func(d time.Duration) domain.RunFact { return domain.RunFact{RanAt: from.Add(d)} }
	runs := []domain.RunFact{at(0), at(time.Hour - time.Millisecond), at(time.Hour), at(to.Sub(from)), at(-time.Minute), at(4 * time.Hour)}
	got := domain.RunsTrend(runs, win(from, to, true))
	var values []int
	for _, p := range got {
		values = append(values, p.Value)
	}
	// from and just before 1h in slice 0; 1h in slice 1; to (inclusive preset), and anything
	// outside, clamp to the first or last slice.
	if diff := cmp.Diff([]int{3, 1, 2}, values); diff != "" {
		t.Errorf("RunsTrend values mismatch (-want +got):\n%s", diff)
	}
	if got[1].Label != "2026-01-01T01:00:00.000Z" {
		t.Errorf("RunsTrend label = %q, want the slice start", got[1].Label)
	}
}

func TestRunsTrend_FractionalWidth(t *testing.T) {
	from := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	// 3 ms in two slices: the second starts at 1.5 ms, truncated to the millisecond.
	got := domain.RunsTrend(nil, win(from, from.Add(3*time.Millisecond), false))
	if diff := cmp.Diff([]string{"2026-01-01T00:00:00.000Z", "2026-01-01T00:00:00.001Z"}, []string{got[0].Label, got[1].Label}); diff != "" {
		t.Errorf("labels mismatch (-want +got):\n%s", diff)
	}
}

func TestFindingsBySeverity(t *testing.T) {
	from := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	w := win(from, from.Add(6*time.Hour), false)
	facts := []domain.FindingFact{
		{RanAt: from, Severity: "CRITICAL", Count: 2},
		{RanAt: from.Add(time.Hour), Severity: "WARNING", Count: 3},
		{RanAt: from.Add(time.Hour), Severity: "SUGGESTION", Count: 4},
		{RanAt: from.Add(6 * time.Hour), Severity: "CRITICAL", Count: 5}, // clamped into the last slice
		{RanAt: from, Severity: "INFO", Count: 9},                        // not a severity of the chart
	}
	got := domain.FindingsBySeverity(facts, w)
	if len(got) != domain.SeveritySlices {
		t.Fatalf("buckets = %d, want %d", len(got), domain.SeveritySlices)
	}
	want := map[int]domain.SeverityBucket{
		0: {From: "2026-01-01T00:00:00.000Z", Critical: 2},
		1: {From: "2026-01-01T01:00:00.000Z", Warning: 3, Suggestion: 4},
		5: {From: "2026-01-01T05:00:00.000Z", Critical: 5},
	}
	for i, b := range got {
		w, ok := want[i]
		if !ok {
			w = domain.SeverityBucket{From: b.From}
		}
		if b != w {
			t.Errorf("bucket %d = %+v, want %+v", i, b, w)
		}
	}
}

func TestFindingsByCategory(t *testing.T) {
	facts := []domain.FindingFact{
		{Category: "style", Count: 1}, {Category: "bug", Count: 2}, {Category: "style", Count: 1},
		{Category: "Bug", Count: 2}, {Category: "none", Count: 0}, {Category: "é", Count: 2},
	}
	want := []domain.CategoryCount{{"Bug", 2}, {"bug", 2}, {"style", 2}, {"é", 2}}
	if diff := cmp.Diff(want, domain.FindingsByCategory(facts)); diff != "" {
		t.Errorf("FindingsByCategory mismatch (-want +got):\n%s", diff)
	}
}

func TestTopSkills(t *testing.T) {
	var facts []domain.SkillFact
	for i, n := range []string{"g", "f", "e", "d", "c", "b", "a"} {
		facts = append(facts, domain.SkillFact{SkillID: n + "-id", Name: n, Runs: 1 + i%2})
	}
	facts = append(facts, domain.SkillFact{SkillID: "z", Name: "zero", Runs: 0})
	var got []string
	for _, u := range domain.TopSkills(facts) {
		got = append(got, u.Label)
	}
	// 2 runs: f, d, b; then 1 run: a, c, e, g by label; five rows at most.
	if diff := cmp.Diff([]string{"b", "d", "f", "a", "c"}, got); diff != "" {
		t.Errorf("TopSkills labels mismatch (-want +got):\n%s", diff)
	}
}

func TestTopMemories(t *testing.T) {
	raw := func(s string) json.RawMessage { return json.RawMessage(s) }
	runs := []json.RawMessage{
		raw(`[{"text":"use const"},{"text":"use const"},{"text":"  "},{"text":7},{"x":1},null,5,"s",[{"text":"in array"}]]`),
		raw(`[{"text":"use const","extra":1},{"text":"Use const"},{"TEXT":"loud"}]`),
		raw(`null`), nil, raw(`"not an array"`), raw(`{"text":"object"}`), raw(`[]`),
	}
	want := []domain.UsageCount{
		{Key: "use const", Label: "use const", Runs: 2}, // once per run, however often it is in one
		{Key: "Use const", Label: "Use const", Runs: 1},
	}
	if diff := cmp.Diff(want, domain.TopMemories(runs)); diff != "" {
		t.Errorf("TopMemories mismatch (-want +got):\n%s", diff)
	}
}

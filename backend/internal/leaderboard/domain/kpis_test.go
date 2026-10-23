package domain_test

import (
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/leaderboard/domain"
)

// Shared fixtures of the aggregate tests (kpis, awards, attention, files, activity). Names carry an
// "agg" prefix: the other test files of this package are written in parallel.

var aggBase = time.Date(2026, time.October, 5, 0, 0, 0, 0, time.UTC) // a Monday

func aggAt(days int, hours ...int) time.Time {
	t := aggBase.AddDate(0, 0, days)
	for _, h := range hours {
		t = t.Add(time.Duration(h) * time.Hour)
	}
	return t
}

func aggPtr[T any](v T) *T { return &v }

func aggSpan() domain.Span { return domain.Span{From: aggAt(0), To: aggAt(7)} }

func aggWindow() domain.Window {
	return domain.Window{
		Key: domain.PeriodWeek, Location: time.UTC,
		From: aggAt(0), To: aggAt(7), HasPrev: true, PrevFrom: aggAt(-7), PrevTo: aggAt(0), Days: 7,
	}
}

func TestKpis_Counts(t *testing.T) {
	rows := []domain.Row{
		{PRID: "a", Health: aggPtr(70), Findings: domain.FindingCounts{Critical: 2}, OpenCritical: 1},
		{PRID: "b", Health: aggPtr(71), Findings: domain.FindingCounts{Critical: 1, Warning: 4}, OpenCritical: 1},
		{PRID: "c"},
	}
	got := domain.Kpis(aggSpan(), rows, domain.Facts{})
	want := domain.KpiSet{PRsReviewed: 3, AvgHealth: aggPtr(71), CriticalFound: 3, OpenCritical: 2} // mean 70.5 rounds up
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Kpis(rows) mismatch (-want +got):\n%s", diff)
	}
}

func TestKpis_Empty(t *testing.T) {
	got := domain.Kpis(aggSpan(), nil, domain.Facts{})
	if diff := cmp.Diff(domain.KpiSet{}, got); diff != "" {
		t.Errorf("Kpis(no rows, no facts) mismatch (-want +got):\n%s", diff)
	}
}

func TestKpisMedianTimeToFirstReview(t *testing.T) {
	row := func(first time.Time, s *int64) domain.Row {
		return domain.Row{FirstReviewAt: aggPtr(first), TimeToFirstReviewS: s}
	}
	tests := []struct {
		name string
		rows []domain.Row
		want *int64
	}{
		{"none", nil, nil},
		{"odd count", []domain.Row{row(aggAt(1), aggPtr[int64](100)), row(aggAt(2), aggPtr[int64](5)), row(aggAt(3), aggPtr[int64](7))}, aggPtr[int64](7)},
		{"even count averages the middle two", []domain.Row{
			row(aggAt(1), aggPtr[int64](10)), row(aggAt(1), aggPtr[int64](20)),
			row(aggAt(1), aggPtr[int64](40)), row(aggAt(1), aggPtr[int64](30)),
		}, aggPtr[int64](25)},
		{"even count truncates to whole seconds", []domain.Row{
			row(aggAt(1), aggPtr[int64](10)), row(aggAt(1), aggPtr[int64](15)),
		}, aggPtr[int64](12)},
		{"first review outside the span is left out", []domain.Row{
			row(aggAt(1), aggPtr[int64](10)), row(aggAt(-1), aggPtr[int64](9000)), row(aggAt(7), aggPtr[int64](9000)),
		}, aggPtr[int64](10)},
		{"unknown time is left out (EC1)", []domain.Row{
			row(aggAt(1), aggPtr[int64](10)), row(aggAt(1), nil),
		}, aggPtr[int64](10)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := domain.Kpis(aggSpan(), tc.rows, domain.Facts{}).TimeToFirstReviewS
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("Kpis(rows).TimeToFirstReviewS mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestKpisMergedAndMedianTimeToMerge(t *testing.T) {
	pull := func(id string, opened *time.Time, merged *time.Time, status string) domain.PullFact {
		return domain.PullFact{PRID: id, OpenedAt: opened, MergedAt: merged, Status: status}
	}
	tests := []struct {
		name       string
		pulls      []domain.PullFact
		wantMerged int
		wantMedian *int64
	}{
		{"no pulls", nil, 0, nil},
		{
			"odd count",
			[]domain.PullFact{
				pull("a", aggPtr(aggAt(1)), aggPtr(aggAt(1, 1)), domain.StatusMerged),  // 3600
				pull("b", aggPtr(aggAt(2)), aggPtr(aggAt(2, 2)), domain.StatusMerged),  // 7200
				pull("c", aggPtr(aggAt(3)), aggPtr(aggAt(3, 10)), domain.StatusMerged), // 36000
			},
			3, aggPtr[int64](7200),
		},
		{
			"even count averages the middle two",
			[]domain.PullFact{
				pull("a", aggPtr(aggAt(1)), aggPtr(aggAt(1, 1)), domain.StatusMerged), // 3600
				pull("b", aggPtr(aggAt(2)), aggPtr(aggAt(2, 3)), domain.StatusMerged), // 10800
			},
			2, aggPtr[int64](7200),
		},
		{
			"merged unreviewed counts, merged without time counts nowhere (EC2, EC3)",
			[]domain.PullFact{
				pull("unreviewed", aggPtr(aggAt(1)), aggPtr(aggAt(2)), domain.StatusMerged),
				pull("no-merge-time", aggPtr(aggAt(1)), nil, domain.StatusMerged),
			},
			1, aggPtr[int64](86400),
		},
		{
			"merge outside the span or exactly at its end is another window",
			[]domain.PullFact{
				pull("prev", aggPtr(aggAt(-3)), aggPtr(aggAt(-2)), domain.StatusMerged),
				pull("next", aggPtr(aggAt(6)), aggPtr(aggAt(7)), domain.StatusMerged),
				pull("at-from", aggPtr(aggAt(-1)), aggPtr(aggAt(0)), domain.StatusMerged),
			},
			1, aggPtr[int64](86400),
		},
		{
			"merged without opened time counts but has no duration (EC1)",
			[]domain.PullFact{pull("seeded", nil, aggPtr(aggAt(2)), domain.StatusMerged)},
			1, nil,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := domain.Kpis(aggSpan(), nil, domain.Facts{Pulls: tc.pulls})
			if got.Merged != tc.wantMerged {
				t.Errorf("Kpis(pulls).Merged = %d, want %d", got.Merged, tc.wantMerged)
			}
			if diff := cmp.Diff(tc.wantMedian, got.TimeToMergeS); diff != "" {
				t.Errorf("Kpis(pulls).TimeToMergeS mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestKpisPreviousWindowUsesItsOwnSpan(t *testing.T) {
	facts := domain.Facts{Pulls: []domain.PullFact{
		{PRID: "cur", OpenedAt: aggPtr(aggAt(1)), MergedAt: aggPtr(aggAt(2))},
		{PRID: "prev", OpenedAt: aggPtr(aggAt(-6)), MergedAt: aggPtr(aggAt(-5))},
	}}
	cur := domain.Kpis(aggSpan(), nil, facts)
	prev := domain.Kpis(domain.Span{From: aggAt(-7), To: aggAt(0)}, nil, facts)
	if cur.Merged != 1 || prev.Merged != 1 {
		t.Errorf("Kpis(cur).Merged, Kpis(prev).Merged = %d, %d, want 1, 1", cur.Merged, prev.Merged)
	}
}

func TestKpisReviewCost(t *testing.T) {
	tests := []struct {
		name string
		runs []domain.RunFact
		want float64
	}{
		{"none", nil, 0},
		{"unknown cost counts as 0", []domain.RunFact{{PRID: "a", RanAt: aggAt(1)}, {PRID: "a", RanAt: aggAt(1), CostUSD: aggPtr(0.25)}}, 0.25},
		{"float noise is rounded away", []domain.RunFact{
			{PRID: "a", RanAt: aggAt(1), CostUSD: aggPtr(0.1)}, {PRID: "b", RanAt: aggAt(2), CostUSD: aggPtr(0.2)},
		}, 0.3},
		{"rounded to 6 decimals", []domain.RunFact{{PRID: "a", RanAt: aggAt(1), CostUSD: aggPtr(0.0000004)}, {PRID: "b", RanAt: aggAt(1), CostUSD: aggPtr(0.0123456)}}, 0.012346},
		{"runs outside the span and at its end are left out", []domain.RunFact{
			{PRID: "a", RanAt: aggAt(-1), CostUSD: aggPtr(5.0)}, {PRID: "b", RanAt: aggAt(7), CostUSD: aggPtr(5.0)},
			{PRID: "c", RanAt: aggAt(0), CostUSD: aggPtr(1.0)},
		}, 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := domain.Kpis(aggSpan(), nil, domain.Facts{Runs: tc.runs}).ReviewCostUSD
			if got != tc.want {
				t.Errorf("Kpis(runs).ReviewCostUSD = %v, want %v", got, tc.want)
			}
		})
	}
}

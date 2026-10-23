package domain_test

import (
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/leaderboard/domain"
)

func aggRowReviews(reviews ...domain.RowReview) domain.Row {
	var r domain.Row
	r.Reviews = reviews
	for _, rv := range reviews {
		r.Findings.Critical += rv.Findings.Critical
	}
	return r
}

func TestDailyOnePointPerDay(t *testing.T) {
	got := domain.Daily(aggWindow(), nil, nil)
	if len(got) != 7 {
		t.Fatalf("Daily(week) returned %d days, want 7", len(got))
	}
	if got[0].Date != "2026-10-05" || got[6].Date != "2026-10-11" {
		t.Errorf("Daily(week) dates = %s..%s, want 2026-10-05..2026-10-11", got[0].Date, got[6].Date)
	}
	for _, d := range got {
		if d.Opened != 0 || d.Merged != 0 || d.Critical != 0 {
			t.Errorf("Daily(no data) day %s = %+v, want zeros", d.Date, d)
		}
	}
}

func TestDaily30Days(t *testing.T) {
	win := domain.Window{Key: domain.Period30d, Location: time.UTC, From: aggAt(-29), To: aggAt(1), Days: 30}
	got := domain.Daily(win, nil, nil)
	if len(got) != 30 || got[0].Date != "2026-09-06" || got[29].Date != "2026-10-05" {
		t.Errorf("Daily(30d) = %d days %s..%s, want 30 days 2026-09-06..2026-10-05", len(got), got[0].Date, got[len(got)-1].Date)
	}
}

func TestDailyBucketsOpenedMergedCritical(t *testing.T) {
	pulls := []domain.PullFact{
		{PRID: "a", OpenedAt: aggPtr(aggAt(0, 23)), MergedAt: aggPtr(aggAt(2, 1))},
		{PRID: "b", OpenedAt: aggPtr(aggAt(0, 1))}, // opened but never reviewed: counts (Q7)
		{PRID: "c", MergedAt: aggPtr(aggAt(2, 5))}, // merged unreviewed (EC2)
		{PRID: "seeded"}, // no instants: nowhere
		{PRID: "old", OpenedAt: aggPtr(aggAt(-1, 23)), MergedAt: aggPtr(aggAt(7))}, // outside the window
		{PRID: "edge", OpenedAt: aggPtr(aggAt(7)), MergedAt: aggPtr(aggAt(0))},     // opened at To is out, merged at From is in
	}
	rows := []domain.Row{
		aggRowReviews(
			domain.RowReview{CreatedAt: aggAt(1, 3), Findings: domain.FindingCounts{Critical: 2, Warning: 5}},
			domain.RowReview{CreatedAt: aggAt(3), Findings: domain.FindingCounts{Critical: 1}},
		),
		aggRowReviews(domain.RowReview{CreatedAt: aggAt(1, 20), Findings: domain.FindingCounts{Critical: 4}}),
	}
	got := domain.Daily(aggWindow(), rows, pulls)
	want := []domain.Day{
		{Date: "2026-10-05", Opened: 2, Merged: 1},
		{Date: "2026-10-06", Critical: 6},
		{Date: "2026-10-07", Merged: 2},
		{Date: "2026-10-08", Critical: 1},
		{Date: "2026-10-09"},
		{Date: "2026-10-10"},
		{Date: "2026-10-11"},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Daily(week) mismatch (-want +got):\n%s", diff)
	}
}

func TestDailyCriticalSumEqualsCriticalFound(t *testing.T) {
	rows := []domain.Row{
		aggRowReviews(
			domain.RowReview{CreatedAt: aggAt(0), Findings: domain.FindingCounts{Critical: 3}}, // exactly From: in
			domain.RowReview{CreatedAt: aggAt(6, 23), Findings: domain.FindingCounts{Critical: 2}},
		),
		aggRowReviews(domain.RowReview{CreatedAt: aggAt(4, 12), Findings: domain.FindingCounts{Critical: 7, Suggestion: 3}}),
	}
	var sum int
	for _, d := range domain.Daily(aggWindow(), rows, nil) {
		sum += d.Critical
	}
	kpi := domain.Kpis(aggSpan(), rows, domain.Facts{})
	if sum != kpi.CriticalFound || sum != 12 {
		t.Errorf("Σ Daily(rows).Critical = %d, Kpis(rows).CriticalFound = %d, want both 12", sum, kpi.CriticalFound)
	}
}

func TestDailyReviewAtExactlyToBelongsToTheNextWindow(t *testing.T) {
	rows := []domain.Row{aggRowReviews(domain.RowReview{CreatedAt: aggAt(7), Findings: domain.FindingCounts{Critical: 5}})}
	for _, d := range domain.Daily(aggWindow(), rows, nil) {
		if d.Critical != 0 {
			t.Errorf("Daily(review at To) day %s critical = %d, want 0", d.Date, d.Critical)
		}
	}
}

func TestDailyFollowsLocalMidnightsAcrossDST(t *testing.T) {
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skipf("no zone database: %v", err)
	}
	// DST ends on Sun 2026-11-01 02:00 local: that day lasts 25 hours.
	from := time.Date(2026, time.October, 26, 0, 0, 0, 0, ny)
	to := time.Date(2026, time.November, 2, 0, 0, 0, 0, ny)
	win := domain.Window{Key: domain.PeriodWeek, Location: ny, From: from, To: to, Days: 7}
	lateSunday := time.Date(2026, time.November, 1, 23, 30, 0, 0, ny) // 04:30 UTC on Nov 2
	earlySunday := time.Date(2026, time.November, 1, 0, 30, 0, 0, ny)
	pulls := []domain.PullFact{{PRID: "a", OpenedAt: &lateSunday, MergedAt: &earlySunday}}

	got := domain.Daily(win, nil, pulls)
	wantDates := []string{"2026-10-26", "2026-10-27", "2026-10-28", "2026-10-29", "2026-10-30", "2026-10-31", "2026-11-01"}
	var dates []string
	for _, d := range got {
		dates = append(dates, d.Date)
	}
	if diff := cmp.Diff(wantDates, dates); diff != "" {
		t.Fatalf("Daily(DST week) dates mismatch (-want +got):\n%s", diff)
	}
	if last := got[6]; last.Opened != 1 || last.Merged != 1 {
		t.Errorf("Daily(DST week) last day = %+v, want opened 1 and merged 1", last)
	}
}

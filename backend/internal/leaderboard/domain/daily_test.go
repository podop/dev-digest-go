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

func aggDayPoint(date string, opened, merged, critical int) domain.ActivityPoint {
	return domain.ActivityPoint{From: date, To: date, Opened: opened, Merged: merged, Critical: critical}
}

func aggPoints(points []domain.ActivityPoint) []string {
	var out []string
	for _, p := range points {
		out = append(out, p.From+".."+p.To)
	}
	return out
}

func TestActivityWeekHasOnePointPerDay(t *testing.T) {
	got := domain.Activity(aggWindow(), nil, nil)
	if got.Granularity != domain.GranularityDay {
		t.Errorf("Activity(week) granularity = %q, want %q", got.Granularity, domain.GranularityDay)
	}
	want := []domain.ActivityPoint{
		aggDayPoint("2026-10-05", 0, 0, 0), aggDayPoint("2026-10-06", 0, 0, 0), aggDayPoint("2026-10-07", 0, 0, 0),
		aggDayPoint("2026-10-08", 0, 0, 0), aggDayPoint("2026-10-09", 0, 0, 0), aggDayPoint("2026-10-10", 0, 0, 0),
		aggDayPoint("2026-10-11", 0, 0, 0),
	}
	if diff := cmp.Diff(want, got.Points); diff != "" {
		t.Errorf("Activity(week, no data) points mismatch (-want +got):\n%s", diff)
	}
}

func TestActivity30dHasMondayWeeksClippedToTheWindow(t *testing.T) {
	tests := []struct {
		name string
		win  domain.Window
		want []string
	}{
		{
			// Sun 2026-09-06 .. Mon 2026-10-05: a one-day first and a one-day last week.
			"starts on a Sunday",
			domain.Window{Key: domain.Period30d, Location: time.UTC, From: aggAt(-29), To: aggAt(1), Days: 30},
			[]string{"2026-09-06..2026-09-06", "2026-09-07..2026-09-13", "2026-09-14..2026-09-20", "2026-09-21..2026-09-27", "2026-09-28..2026-10-04", "2026-10-05..2026-10-05"},
		},
		{
			// Mon 2026-09-07 .. Tue 2026-10-06: five weeks, the last one clipped.
			"starts on a Monday",
			domain.Window{Key: domain.Period30d, Location: time.UTC, From: aggAt(-28), To: aggAt(2), Days: 30},
			[]string{"2026-09-07..2026-09-13", "2026-09-14..2026-09-20", "2026-09-21..2026-09-27", "2026-09-28..2026-10-04", "2026-10-05..2026-10-06"},
		},
		{
			// Tue 2026-09-08 .. Wed 2026-10-07.
			"starts on a Tuesday",
			domain.Window{Key: domain.Period30d, Location: time.UTC, From: aggAt(-27), To: aggAt(3), Days: 30},
			[]string{"2026-09-08..2026-09-13", "2026-09-14..2026-09-20", "2026-09-21..2026-09-27", "2026-09-28..2026-10-04", "2026-10-05..2026-10-07"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := domain.Activity(tc.win, nil, nil)
			if got.Granularity != domain.GranularityWeek {
				t.Errorf("Activity(30d) granularity = %q, want %q", got.Granularity, domain.GranularityWeek)
			}
			if diff := cmp.Diff(tc.want, aggPoints(got.Points)); diff != "" {
				t.Errorf("Activity(30d) points mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestActivity30dBucketsIntoWeeks(t *testing.T) {
	win := domain.Window{Key: domain.Period30d, Location: time.UTC, From: aggAt(-29), To: aggAt(1), Days: 30}
	pulls := []domain.PullFact{
		{PRID: "a", OpenedAt: aggPtr(aggAt(-29, 23)), MergedAt: aggPtr(aggAt(-26))}, // Sun Sep 6 / Wed Sep 9
		{PRID: "b", OpenedAt: aggPtr(aggAt(-1)), MergedAt: aggPtr(aggAt(0, 5))},     // Sun Oct 4 / Mon Oct 5
		{PRID: "before", OpenedAt: aggPtr(aggAt(-29).Add(-time.Second))},            // before the window
		{PRID: "after", OpenedAt: aggPtr(aggAt(1))},                                 // exactly To
	}
	rows := []domain.Row{
		aggRowReviews(domain.RowReview{CreatedAt: aggAt(-8, 12), Findings: domain.FindingCounts{Critical: 3}}), // Sun Sep 27
		aggRowReviews(domain.RowReview{CreatedAt: aggAt(-7), Findings: domain.FindingCounts{Critical: 2}}),     // Mon Sep 28
	}
	got := domain.Activity(win, rows, pulls)
	want := []domain.ActivityPoint{
		{From: "2026-09-06", To: "2026-09-06", Opened: 1},
		{From: "2026-09-07", To: "2026-09-13", Merged: 1},
		{From: "2026-09-14", To: "2026-09-20"},
		{From: "2026-09-21", To: "2026-09-27", Critical: 3},
		{From: "2026-09-28", To: "2026-10-04", Opened: 1, Critical: 2},
		{From: "2026-10-05", To: "2026-10-05", Merged: 1},
	}
	if diff := cmp.Diff(want, got.Points); diff != "" {
		t.Errorf("Activity(30d) points mismatch (-want +got):\n%s", diff)
	}
	var sum int
	for _, p := range got.Points {
		sum += p.Critical
	}
	if kpi := domain.Kpis(domain.Span{From: win.From, To: win.To}, rows, domain.Facts{}); sum != kpi.CriticalFound {
		t.Errorf("Σ Activity(30d).Critical = %d, Kpis.CriticalFound = %d, want equal", sum, kpi.CriticalFound)
	}
}

func TestActivityBucketsOpenedMergedCritical(t *testing.T) {
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
	got := domain.Activity(aggWindow(), rows, pulls)
	want := []domain.ActivityPoint{
		aggDayPoint("2026-10-05", 2, 1, 0),
		aggDayPoint("2026-10-06", 0, 0, 6),
		aggDayPoint("2026-10-07", 0, 2, 0),
		aggDayPoint("2026-10-08", 0, 0, 1),
		aggDayPoint("2026-10-09", 0, 0, 0),
		aggDayPoint("2026-10-10", 0, 0, 0),
		aggDayPoint("2026-10-11", 0, 0, 0),
	}
	if diff := cmp.Diff(want, got.Points); diff != "" {
		t.Errorf("Activity(week) points mismatch (-want +got):\n%s", diff)
	}
}

func TestActivityCriticalSumEqualsCriticalFound(t *testing.T) {
	rows := []domain.Row{
		aggRowReviews(
			domain.RowReview{CreatedAt: aggAt(0), Findings: domain.FindingCounts{Critical: 3}}, // exactly From: in
			domain.RowReview{CreatedAt: aggAt(6, 23), Findings: domain.FindingCounts{Critical: 2}},
		),
		aggRowReviews(domain.RowReview{CreatedAt: aggAt(4, 12), Findings: domain.FindingCounts{Critical: 7, Suggestion: 3}}),
	}
	var sum int
	for _, p := range domain.Activity(aggWindow(), rows, nil).Points {
		sum += p.Critical
	}
	kpi := domain.Kpis(aggSpan(), rows, domain.Facts{})
	if sum != kpi.CriticalFound || sum != 12 {
		t.Errorf("Σ Activity(rows).Critical = %d, Kpis(rows).CriticalFound = %d, want both 12", sum, kpi.CriticalFound)
	}
}

func TestActivityReviewAtExactlyToBelongsToTheNextWindow(t *testing.T) {
	rows := []domain.Row{aggRowReviews(domain.RowReview{CreatedAt: aggAt(7), Findings: domain.FindingCounts{Critical: 5}})}
	for _, p := range domain.Activity(aggWindow(), rows, nil).Points {
		if p.Critical != 0 {
			t.Errorf("Activity(review at To) day %s critical = %d, want 0", p.From, p.Critical)
		}
	}
}

func TestActivityFollowsLocalMidnightsAcrossDST(t *testing.T) {
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

	got := domain.Activity(win, nil, pulls).Points
	wantDates := []string{"2026-10-26", "2026-10-27", "2026-10-28", "2026-10-29", "2026-10-30", "2026-10-31", "2026-11-01"}
	var dates []string
	for _, p := range got {
		dates = append(dates, p.From)
	}
	if diff := cmp.Diff(wantDates, dates); diff != "" {
		t.Fatalf("Activity(DST week) dates mismatch (-want +got):\n%s", diff)
	}
	if last := got[6]; last.Opened != 1 || last.Merged != 1 {
		t.Errorf("Activity(DST week) last day = %+v, want opened 1 and merged 1", last)
	}
}

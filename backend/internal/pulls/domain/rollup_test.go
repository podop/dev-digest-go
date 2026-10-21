package domain_test

import (
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/pulls/domain"
)

func TestCurrentReviewsAndRollup(t *testing.T) {
	base := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	reviews := []domain.Review{
		{ID: "a1", AgentID: "A", Score: ptr(50), CreatedAt: base},
		{ID: "a2", AgentID: "A", Score: ptr(70), CreatedAt: base.Add(time.Hour)},
		{ID: "b1", AgentID: "B", Score: ptr(90), CreatedAt: base.Add(2 * time.Hour)},
	}
	current := domain.CurrentReviews(reviews)
	var ids []string
	for _, r := range current {
		ids = append(ids, r.ID)
	}
	if diff := cmp.Diff([]string{"b1", "a2"}, ids); diff != "" {
		t.Fatalf("CurrentReviews() ids mismatch (-want +got):\n%s", diff)
	}

	cost := 0.75
	got := domain.NewRollup(current, map[string]domain.FindingsCounts{
		"a2": {Critical: 1, Warning: 2},
		"b1": {Suggestion: 1},
	}, &cost)
	latestAt := base.Add(2 * time.Hour)
	want := domain.Rollup{
		Score:           ptr(90),
		CostUSD:         &cost,
		LatestReviewID:  ptr("b1"),
		LastReviewedAt:  &latestAt,
		LatestReviewIDs: []string{"b1", "a2"},
		FindingsCounts:  &domain.FindingsCounts{Critical: 1, Warning: 2, Suggestion: 1},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("NewRollup() mismatch (-want +got):\n%s", diff)
	}
}

func TestCurrentReviews_agentlessCollapseAndTies(t *testing.T) {
	at := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	got := domain.CurrentReviews([]domain.Review{
		{ID: "x1", CreatedAt: at},
		{ID: "x2", CreatedAt: at},
		{ID: "x0", CreatedAt: at.Add(-time.Hour)},
	})
	if len(got) != 1 || got[0].ID != "x2" {
		t.Errorf("CurrentReviews(agentless) = %+v, want only x2 (same time, higher id wins)", got)
	}
}

func TestNewRollup_noReviews(t *testing.T) {
	got := domain.NewRollup(nil, nil, nil)
	want := domain.Rollup{LatestReviewIDs: []string{}}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("NewRollup(none) mismatch (-want +got):\n%s", diff)
	}
}

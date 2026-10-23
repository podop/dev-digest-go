package domain_test

import (
	"strconv"
	"testing"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/evals/domain"
)

func metricRun(id string, recall, precision, citation *float64) domain.Run {
	return domain.Run{ID: id, Metrics: domain.Metrics{Recall: recall, Precision: precision, CitationAccuracy: citation}}
}

func TestRegressionAlert(t *testing.T) {
	prev := metricRun("p", fp(0.92), fp(0.80), fp(1))
	tests := []struct {
		name             string
		latest, previous *domain.Run
		want             []domain.MetricDelta
	}{
		{"no latest", nil, &prev, nil},
		{"no previous", ptr(metricRun("l", fp(0), fp(0), fp(0))), nil, nil},
		{"nothing dropped", ptr(metricRun("l", fp(0.95), fp(0.80), fp(1))), &prev, nil},
		{"drop of 0.02 after rounding counts", ptr(metricRun("l", fp(0.90), fp(0.80), fp(1))), &prev, []domain.MetricDelta{{Metric: domain.MetricRecall, Delta: -0.02}}},
		{"drop of 0.0199 is no alert", ptr(metricRun("l", fp(0.9001), fp(0.80), fp(1))), &prev, nil},
		{"drop of 0.0149 is no alert either", ptr(metricRun("l", fp(0.9051), fp(0.80), fp(1))), &prev, nil},
		{
			"all three in order",
			ptr(metricRun("l", fp(0.5), fp(0.1), fp(0.9))), &prev,
			[]domain.MetricDelta{{Metric: domain.MetricRecall, Delta: -0.42}, {Metric: domain.MetricPrecision, Delta: -0.7}, {Metric: domain.MetricCitationAccuracy, Delta: -0.1}},
		},
		{"nil metric cannot regress", ptr(metricRun("l", nil, fp(0.8), nil)), &prev, nil},
		{"nil previous metric cannot regress", ptr(metricRun("l", fp(0), fp(0), fp(0))), ptr(metricRun("p", nil, nil, nil)), nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := domain.RegressionAlert(tc.latest, tc.previous)
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("RegressionAlert mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func runs(n int) []domain.Run {
	out := make([]domain.Run, n)
	for i := range out { // newest first: r0 is the newest
		out[i] = metricRun("r"+strconv.Itoa(i), fp(float64(i)/100), nil, nil)
	}
	return out
}

func ids(rs []domain.Run) []string {
	out := []string{}
	for _, r := range rs {
		out = append(out, r.ID)
	}
	return out
}

func TestTrend(t *testing.T) {
	tests := []struct {
		name  string
		in    []domain.Run
		limit int
		want  []string
	}{
		{"empty", nil, 20, []string{}},
		{"fewer than the limit, oldest first", runs(3), 20, []string{"r2", "r1", "r0"}},
		{"cut to the newest", runs(5), 2, []string{"r1", "r0"}},
		{"exactly the limit", runs(3), 3, []string{"r2", "r1", "r0"}},
		{"zero limit", runs(3), 0, []string{}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := domain.Trend(tc.in, tc.limit)
			if diff := cmp.Diff(tc.want, ids(got)); diff != "" {
				t.Errorf("Trend mismatch (-want +got):\n%s", diff)
			}
			if got == nil {
				t.Errorf("Trend = nil, want non-nil")
			}
		})
	}
}

func TestSparkline(t *testing.T) {
	got := domain.Sparkline(runs(12))
	if len(got) != 10 || got[0].RunID != "r9" || got[9].RunID != "r0" {
		t.Fatalf("Sparkline ids = first %s last %s len %d, want r9 .. r0 and 10 points", got[0].RunID, got[9].RunID, len(got))
	}
	if r := got[9]; r.Recall == nil || *r.Recall != 0 || r.Precision != nil || r.CitationAccuracy != nil {
		t.Errorf("Sparkline newest point = %+v, want recall 0 and nil precision, citation accuracy", r)
	}
	if got := domain.Sparkline(nil); got == nil || len(got) != 0 {
		t.Errorf("Sparkline(nil) = %#v, want empty non-nil", got)
	}
}

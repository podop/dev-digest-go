package domain_test

import (
	"testing"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/reviewer/domain"
)

func finding(id string, sev domain.Severity, file string, start, end int) domain.Finding {
	return domain.Finding{ID: id, Severity: sev, Title: "t-" + id, File: file, StartLine: start, EndLine: end}
}

// AC4044
func TestReduceReviews(t *testing.T) {
	a := finding("a", domain.SeverityWarning, "a.ts", 1, 1)
	b := finding("b", domain.SeverityCritical, "b.ts", 2, 2)
	c := finding("c", domain.SeveritySuggestion, "c.ts", 3, 3)
	got := domain.ReduceReviews([]domain.Review{
		{Verdict: domain.VerdictApprove, Summary: "a", Score: 90, Findings: []domain.Finding{a}},
		{Verdict: domain.VerdictRequestChanges, Summary: "", Score: 40, Findings: []domain.Finding{b, c}},
		{Verdict: domain.VerdictComment, Summary: "c", Score: 71},
	})
	want := domain.Review{
		Verdict: domain.VerdictRequestChanges, Summary: "a c", Score: 67,
		Findings: []domain.Finding{a, b, c},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("ReduceReviews mismatch (-want +got):\n%s", diff)
	}
}

func TestReduceReviews_SingleIsUnchanged(t *testing.T) {
	in := domain.Review{Verdict: domain.VerdictComment, Summary: " s ", Score: 33, Findings: []domain.Finding{finding("a", domain.SeverityWarning, "a", 1, 1)}}
	if diff := cmp.Diff(in, domain.ReduceReviews([]domain.Review{in})); diff != "" {
		t.Errorf("ReduceReviews(one) mismatch (-want +got):\n%s", diff)
	}
}

func TestReduceReviews_Rules(t *testing.T) {
	tests := []struct {
		name string
		in   []domain.Review
		want domain.Review
	}{
		{
			"the mean rounds half up like Math.round",
			[]domain.Review{{Score: 1}, {Score: 2}},
			domain.Review{Verdict: domain.VerdictApprove, Score: 2},
		},
		{
			"the mean rounds down below a half",
			[]domain.Review{{Score: 1}, {Score: 1}, {Score: 2}},
			domain.Review{Verdict: domain.VerdictApprove, Score: 1},
		},
		{
			"comment beats approve, request_changes beats comment",
			[]domain.Review{{Verdict: domain.VerdictApprove}, {Verdict: domain.VerdictComment}},
			domain.Review{Verdict: domain.VerdictComment},
		},
		{
			"a whitespace-only summary is kept",
			[]domain.Review{{Summary: " "}, {Summary: "x"}},
			domain.Review{Verdict: domain.VerdictApprove, Summary: "  x"},
		},
		{
			"an unknown verdict ranks like approve",
			[]domain.Review{{Verdict: "bogus"}, {Verdict: domain.VerdictComment}},
			domain.Review{Verdict: domain.VerdictComment},
		},
		{"no partial", nil, domain.Review{Verdict: domain.VerdictApprove}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if diff := cmp.Diff(tc.want, domain.ReduceReviews(tc.in)); diff != "" {
				t.Errorf("ReduceReviews mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

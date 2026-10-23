package domain_test

import (
	"math"
	"testing"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/evals/domain"
)

func fp(f float64) *float64 { return &f }

func TestRoundTo(t *testing.T) {
	tests := []struct {
		name     string
		in       float64
		decimals int
		want     float64
	}{
		{"half rounds up", 0.00005, 4, 0.0001},
		{"just below half", 0.000049999, 4, 0},
		{"ties go toward +Infinity", -0.00005, 4, 0},
		{"negative half-way above", -0.00015, 4, -0.0001},
		{"two thirds", 2.0 / 3.0, 4, 0.6667},
		{"float noise of a difference", 0.90 - 0.92, 4, -0.02},
		{"negative zero is zero", math.Copysign(0, -1), 4, 0},
		{"tiny negative is zero", -0.00001, 4, 0},
		{"cost decimals", 0.0000005, 6, 0.000001},
		{"cost sum", 0.1 + 0.2, 6, 0.3},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := domain.RoundTo(tc.in, tc.decimals)
			if got != tc.want || (tc.want == 0 && math.Signbit(got)) {
				t.Errorf("RoundTo(%v, %d) = %v (signbit %v), want %v", tc.in, tc.decimals, got, math.Signbit(got), tc.want)
			}
		})
	}
}

func TestMetricChange(t *testing.T) {
	tests := []struct {
		name       string
		prev, next *float64
		want       *float64
	}{
		{"both", fp(0.92), fp(0.90), fp(-0.02)},
		{"rise", fp(0.5), fp(0.75), fp(0.25)},
		{"prev nil", nil, fp(0.5), nil},
		{"next nil", fp(0.5), nil, nil},
		{"equal is positive zero", fp(0.3), fp(0.3), fp(0)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := domain.MetricChange(tc.prev, tc.next, domain.MetricDecimals)
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("MetricChange mismatch (-want +got):\n%s", diff)
			}
		})
	}
	if got := domain.MetricChange(fp(0.000001), fp(0.000003), domain.CostDecimals); got == nil || *got != 0.000002 {
		t.Errorf("MetricChange(cost decimals) = %v, want 0.000002", got)
	}
}

func TestRangesIntersect(t *testing.T) {
	tests := []struct {
		name                       string
		aStart, aEnd, bStart, bEnd int
		want                       bool
	}{
		{"overlap", 1, 5, 4, 9, true},
		{"touch at a point", 1, 5, 5, 9, true},
		{"disjoint", 1, 4, 5, 9, false},
		{"reversed bounds", 5, 1, 4, 9, true},
		{"both reversed, disjoint", 9, 6, 4, 1, false},
		{"single lines equal", 7, 7, 7, 7, true},
		{"contained", 1, 100, 50, 51, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := domain.RangesIntersect(tc.aStart, tc.aEnd, tc.bStart, tc.bEnd); got != tc.want {
				t.Errorf("RangesIntersect(%d, %d, %d, %d) = %v, want %v", tc.aStart, tc.aEnd, tc.bStart, tc.bEnd, got, tc.want)
			}
		})
	}
}

func kept(file string, start, end int) domain.KeptFinding {
	return domain.KeptFinding{File: file, StartLine: start, EndLine: end, Severity: sp("high"), Category: sp("bug"), Title: sp("t")}
}

func labeled(f domain.KeptFinding, m domain.FindingMatch) domain.ResultFinding {
	return domain.ResultFinding{
		File: f.File, StartLine: f.StartLine, EndLine: f.EndLine,
		Severity: f.Severity, Category: f.Category, Title: f.Title, Match: m,
	}
}

func TestScoreCase(t *testing.T) {
	hit := kept("a.go", 5, 6)
	otherFile := kept("b.go", 5, 6)
	otherLines := kept("a.go", 20, 21)
	reversed := kept("a.go", 9, 7)

	must := domain.Expectation{Kind: domain.MustFind, File: "a.go", StartLine: 6, EndLine: 8}
	mustNot := domain.Expectation{Kind: domain.MustNotFlag, File: "a.go", StartLine: 6, EndLine: 8}

	tests := []struct {
		name string
		e    domain.Expectation
		in   []domain.KeptFinding
		want domain.CaseScore
	}{
		{
			"must_find hit passes", must, []domain.KeptFinding{hit, otherFile, otherLines},
			domain.CaseScore{
				Status:   domain.CasePassed,
				Findings: []domain.ResultFinding{labeled(hit, domain.MatchHit), labeled(otherFile, domain.MatchUnlabeled), labeled(otherLines, domain.MatchUnlabeled)},
				HitCount: 1, UnlabeledCount: 2,
			},
		},
		{
			"must_find without a hit fails", must, []domain.KeptFinding{otherFile, otherLines},
			domain.CaseScore{
				Status:         domain.CaseFailed,
				Findings:       []domain.ResultFinding{labeled(otherFile, domain.MatchUnlabeled), labeled(otherLines, domain.MatchUnlabeled)},
				UnlabeledCount: 2,
			},
		},
		{
			"must_find with no findings fails", must, nil,
			domain.CaseScore{Status: domain.CaseFailed, Findings: []domain.ResultFinding{}},
		},
		{
			"must_not_flag with no findings passes", mustNot, nil,
			domain.CaseScore{Status: domain.CasePassed, Findings: []domain.ResultFinding{}},
		},
		{
			"must_not_flag violation fails", mustNot, []domain.KeptFinding{reversed, otherLines},
			domain.CaseScore{
				Status:         domain.CaseFailed,
				Findings:       []domain.ResultFinding{labeled(reversed, domain.MatchViolation), labeled(otherLines, domain.MatchUnlabeled)},
				ViolationCount: 1, UnlabeledCount: 1,
			},
		},
		{
			"must_not_flag with only unrelated findings passes", mustNot, []domain.KeptFinding{otherFile},
			domain.CaseScore{
				Status: domain.CasePassed, Findings: []domain.ResultFinding{labeled(otherFile, domain.MatchUnlabeled)}, UnlabeledCount: 1,
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := domain.ScoreCase(tc.e, tc.in)
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("ScoreCase mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func outcome(kind domain.ExpectationKind, st domain.CaseStatus, hit, viol, unl, dropped int, cost *float64) domain.CaseOutcome {
	return domain.CaseOutcome{Kind: kind, Status: st, HitCount: hit, ViolationCount: viol, UnlabeledCount: unl, DroppedCount: dropped, CostUSD: cost}
}

func TestAggregateRun(t *testing.T) {
	const mf, mn = domain.MustFind, domain.MustNotFlag
	const p, f, e = domain.CasePassed, domain.CaseFailed, domain.CaseError
	tests := []struct {
		name string
		in   []domain.CaseOutcome
		want domain.RunTotals
	}{
		{"no outcomes", nil, domain.RunTotals{}},
		{
			"mixed",
			[]domain.CaseOutcome{
				outcome(mf, p, 2, 0, 1, 0, fp(0.1)),
				outcome(mf, f, 0, 0, 2, 1, fp(0.2)),
				outcome(mn, f, 0, 1, 0, 0, nil),
				outcome(mn, p, 0, 0, 0, 0, fp(0.05)),
				outcome(mf, e, 9, 9, 9, 9, fp(0.3)),
			},
			domain.RunTotals{
				Metrics: domain.Metrics{
					Recall:            fp(0.5),    // 1 of 2 must_find non-error cases passed
					Precision:         fp(0.6667), // 2 / (2 + 1)
					CitationAccuracy:  fp(0.8571), // kept 6 / (6 + 1)
					UnlabeledFindings: 3,
					CostUSD:           fp(0.65), // the errored case's spend counts
				},
				CasesPassed: 2, CasesErrored: 1,
			},
		},
		{
			"only must_not_flag cases: recall nil",
			[]domain.CaseOutcome{outcome(mn, p, 0, 0, 0, 0, nil)},
			domain.RunTotals{
				Metrics:     domain.Metrics{CitationAccuracy: nil, Precision: nil},
				CasesPassed: 1,
			},
		},
		{
			"all errored: every ratio nil, cost still summed",
			[]domain.CaseOutcome{outcome(mf, e, 0, 0, 0, 0, fp(0.25)), outcome(mn, e, 0, 0, 0, 0, nil)},
			domain.RunTotals{Metrics: domain.Metrics{CostUSD: fp(0.25)}, CasesErrored: 2},
		},
		{
			"citation accuracy with only drops is zero",
			[]domain.CaseOutcome{outcome(mf, f, 0, 0, 0, 3, nil)},
			domain.RunTotals{Metrics: domain.Metrics{Recall: fp(0), CitationAccuracy: fp(0)}},
		},
		{
			"cost rounded to 6 decimals",
			[]domain.CaseOutcome{outcome(mn, p, 0, 0, 0, 0, fp(0.1)), outcome(mn, p, 0, 0, 0, 0, fp(0.2))},
			domain.RunTotals{Metrics: domain.Metrics{CostUSD: fp(0.3)}, CasesPassed: 2},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := domain.AggregateRun(tc.in)
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("AggregateRun mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestVerdict(t *testing.T) {
	failedWith := func(msg string) domain.RunVerdict {
		return domain.RunVerdict{Status: domain.RunFailed, Error: &msg}
	}
	errOutcome := func(msg *string) domain.CaseOutcome {
		return domain.CaseOutcome{Status: domain.CaseError, Error: msg}
	}
	tests := []struct {
		name string
		in   []domain.CaseOutcome
		want domain.RunVerdict
	}{
		{"no outcomes is completed", nil, domain.RunVerdict{Status: domain.RunCompleted}},
		{"one failed case is completed", []domain.CaseOutcome{errOutcome(sp("x")), {Status: domain.CaseFailed}}, domain.RunVerdict{Status: domain.RunCompleted}},
		{"all errored takes the first message", []domain.CaseOutcome{errOutcome(nil), errOutcome(sp("")), errOutcome(sp("boom")), errOutcome(sp("later"))}, failedWith("boom")},
		{"all errored without messages", []domain.CaseOutcome{errOutcome(nil), errOutcome(sp(""))}, failedWith("every case errored")},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if diff := cmp.Diff(tc.want, domain.Verdict(tc.in)); diff != "" {
				t.Errorf("Verdict mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

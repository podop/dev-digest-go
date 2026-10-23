package domain_test

import (
	"testing"

	"dev-digest-go/backend/internal/ci/runner/domain"
	reviewer "dev-digest-go/backend/internal/reviewer/domain"
)

func findingsOf(sevs ...reviewer.Severity) []reviewer.Finding {
	out := make([]reviewer.Finding, len(sevs))
	for i, s := range sevs {
		out[i] = reviewer.Finding{Severity: s}
	}
	return out
}

func TestGate(t *testing.T) {
	const (
		c = reviewer.SeverityCritical
		w = reviewer.SeverityWarning
		s = reviewer.SeveritySuggestion
	)
	tests := []struct {
		name     string
		failOn   reviewer.FailOn
		findings []reviewer.Finding
		blockers int
	}{
		{"never counts none", reviewer.FailOnNever, findingsOf(c, w, s), 0},
		{"critical counts critical only", reviewer.FailOnCritical, findingsOf(c, w, s, c), 2},
		{"warning counts warning and critical", reviewer.FailOnWarning, findingsOf(c, w, s, w), 3},
		{"any counts all", reviewer.FailOnAny, findingsOf(c, w, s), 3},
		{"critical with only suggestions", reviewer.FailOnCritical, findingsOf(s, s), 0},
		{"no findings", reviewer.FailOnAny, nil, 0},
		{"unknown gate counts none", reviewer.FailOn("sometimes"), findingsOf(c), 0},
		{"unknown severity ranks 0", reviewer.FailOnAny, findingsOf(reviewer.Severity("INFO")), 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := domain.CountBlockers(tc.findings, tc.failOn); got != tc.blockers {
				t.Errorf("CountBlockers(%d findings, %q) = %d, want %d", len(tc.findings), tc.failOn, got, tc.blockers)
			}
			if got, want := domain.GateTriggered(tc.findings, tc.failOn), tc.blockers > 0; got != want {
				t.Errorf("GateTriggered(%d findings, %q) = %v, want %v", len(tc.findings), tc.failOn, got, want)
			}
		})
	}
}

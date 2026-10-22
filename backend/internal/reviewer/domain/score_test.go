package domain_test

import (
	"testing"

	"dev-digest-go/backend/internal/reviewer/domain"
)

func severities(sev ...domain.Severity) []domain.Finding {
	var out []domain.Finding
	for _, s := range sev {
		out = append(out, domain.Finding{Severity: s})
	}
	return out
}

// AC4047
func TestScoreFromFindings(t *testing.T) {
	const c, w, s = domain.SeverityCritical, domain.SeverityWarning, domain.SeveritySuggestion
	tests := []struct {
		name string
		in   []domain.Finding
		want int
	}{
		{"no finding", nil, 100},
		{"one suggestion", severities(s), 97},
		{"one warning", severities(w), 88},
		{"one critical", severities(c), 65},
		{"critical, warning and two suggestions", severities(c, w, s, s), 47},
		{"three criticals clamp to 0", severities(c, c, c), 0},
		{"an unknown severity costs nothing", severities("BOGUS"), 100},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := domain.ScoreFromFindings(tc.in); got != tc.want {
				t.Errorf("ScoreFromFindings = %d, want %d", got, tc.want)
			}
		})
	}
}

// AC4047
func TestCountBlockers(t *testing.T) {
	in := severities(domain.SeverityCritical, domain.SeverityWarning, domain.SeveritySuggestion, domain.SeveritySuggestion)
	tests := []struct {
		failOn domain.FailOn
		want   int
	}{
		{domain.FailOnCritical, 1},
		{domain.FailOnWarning, 2},
		{domain.FailOnAny, 4},
		{domain.FailOnNever, 0},
		{domain.FailOn("bogus"), 0},
	}
	for _, tc := range tests {
		if got := domain.CountBlockers(in, tc.failOn); got != tc.want {
			t.Errorf("CountBlockers(4 findings, %q) = %d, want %d", tc.failOn, got, tc.want)
		}
	}
	if got := domain.CountBlockers(severities("BOGUS"), domain.FailOnAny); got != 0 {
		t.Errorf("CountBlockers(unknown severity, any) = %d, want 0", got)
	}
}

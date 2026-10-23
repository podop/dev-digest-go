package domain_test

import (
	"testing"
	"time"

	"dev-digest-go/backend/internal/pulls/domain"
)

func TestClampPollInterval(t *testing.T) {
	tests := []struct {
		name string
		in   time.Duration
		want time.Duration
	}{
		{"missing", 0, 5 * time.Minute},
		{"negative", -time.Minute, 5 * time.Minute},
		{"below minimum", 10 * time.Second, time.Minute},
		{"minimum", time.Minute, time.Minute},
		{"normal", 15 * time.Minute, 15 * time.Minute},
		{"maximum", 1440 * time.Minute, 1440 * time.Minute},
		{"above maximum", 2000 * time.Minute, 1440 * time.Minute},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := domain.ClampPollInterval(tc.in); got != tc.want {
				t.Errorf("ClampPollInterval(%v) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestReachedCutoff(t *testing.T) {
	cutoff := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	at := func(d time.Duration) *time.Time { v := cutoff.Add(d); return &v }
	tests := []struct {
		name string
		in   *time.Time
		want bool
	}{
		{"older", at(-time.Second), true},
		{"exactly at cutoff", at(0), false},
		{"newer", at(time.Second), false},
		{"no update time", nil, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := domain.ReachedCutoff(domain.Listed{UpdatedAt: tc.in}, cutoff); got != tc.want {
				t.Errorf("ReachedCutoff(updated %v, cutoff %v) = %v, want %v", tc.in, cutoff, got, tc.want)
			}
		})
	}
}

func TestResumeAfterRateLimit(t *testing.T) {
	start := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	due := start.Add(5 * time.Minute)
	tests := []struct {
		name  string
		reset time.Time
		want  time.Time
	}{
		{"unknown reset", time.Time{}, due},
		{"reset before due", start.Add(time.Minute), due},
		{"reset after due", start.Add(30 * time.Minute), start.Add(30 * time.Minute)},
		{"bogus far reset", start.Add(1000 * time.Hour), start.Add(domain.MaxRateLimitWait)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := domain.ResumeAfterRateLimit(start, due, tc.reset); !got.Equal(tc.want) {
				t.Errorf("ResumeAfterRateLimit(reset %v) = %v, want %v", tc.reset, got, tc.want)
			}
		})
	}
}

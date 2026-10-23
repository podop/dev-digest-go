package domain_test

import (
	"testing"
	"time"

	"dev-digest-go/backend/internal/evals/domain"
)

func TestParsePeriod(t *testing.T) {
	tests := []struct {
		in     string
		want   domain.Period
		wantOK bool
	}{
		{"7d", domain.Period7d, true},
		{"30d", domain.Period30d, true},
		{"90d", domain.Period90d, true},
		{"all", domain.PeriodAll, true},
		{"", domain.PeriodNone, false},
		{"1d", domain.PeriodNone, false},
		{"7D", domain.PeriodNone, false},
		{" 7d", domain.PeriodNone, false},
	}
	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			got, ok := domain.ParsePeriod(tc.in)
			if got != tc.want || ok != tc.wantOK {
				t.Errorf("ParsePeriod(%q) = %q, %v, want %q, %v", tc.in, got, ok, tc.want, tc.wantOK)
			}
		})
	}
}

func TestPeriodStart(t *testing.T) {
	now := time.Date(2026, 10, 23, 12, 30, 0, 123_000_000, time.UTC)
	tests := []struct {
		period domain.Period
		want   *time.Time
	}{
		{domain.Period7d, ptr(now.Add(-7 * 24 * time.Hour))},
		{domain.Period30d, ptr(time.Date(2026, 9, 23, 12, 30, 0, 123_000_000, time.UTC))},
		{domain.Period90d, ptr(time.Date(2026, 7, 25, 12, 30, 0, 123_000_000, time.UTC))},
		{domain.PeriodAll, nil},
		{domain.PeriodNone, nil},
	}
	for _, tc := range tests {
		t.Run(string(tc.period), func(t *testing.T) {
			got := domain.PeriodStart(tc.period, now)
			switch {
			case tc.want == nil && got != nil, tc.want != nil && (got == nil || !got.Equal(*tc.want)):
				t.Errorf("PeriodStart(%q) = %v, want %v", tc.period, got, tc.want)
			}
		})
	}
}

func ptr[T any](v T) *T { return &v }

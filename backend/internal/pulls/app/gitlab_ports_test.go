package app_test

import (
	"errors"
	"testing"
	"time"

	"dev-digest-go/backend/internal/pulls/app"
)

func TestGitLabError_RateLimit(t *testing.T) {
	retry := time.Date(2026, 10, 1, 12, 0, 30, 0, time.UTC)
	reset := time.Date(2026, 10, 1, 12, 1, 0, 0, time.UTC)
	tests := []struct {
		name        string
		err         *app.GitLabError
		wantReset   time.Time
		wantLimited bool
	}{
		{"429 with retry-after", &app.GitLabError{Status: 429, RetryAfter: retry, RateLimitReset: reset}, retry, true},
		{"429 with reset only", &app.GitLabError{Status: 429, RateLimitReset: reset}, reset, true},
		{"429 without hints", &app.GitLabError{Status: 429}, time.Time{}, true},
		{"flagged limited", &app.GitLabError{Status: 403, RateLimited: true, RateLimitReset: reset}, reset, true},
		{"server error", &app.GitLabError{Status: 500, RateLimitReset: reset}, time.Time{}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, limited := tc.err.RateLimit()
			if limited != tc.wantLimited || !got.Equal(tc.wantReset) {
				t.Errorf("RateLimit() = (%v, %v), want (%v, %v)", got, limited, tc.wantReset, tc.wantLimited)
			}
		})
	}
}

func TestNoTokenError_message(t *testing.T) {
	var err error = &app.NoTokenError{Conn: "corp"}
	if got, want := err.Error(), "gitlab connection corp has no token"; got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
	var target *app.NoTokenError
	if !errors.As(err, &target) || target.Conn != "corp" {
		t.Errorf("errors.As(NoTokenError) failed: %v", target)
	}
}

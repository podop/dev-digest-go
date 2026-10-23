package app_test

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"dev-digest-go/backend/internal/ci/app"
)

func TestGitHubError(t *testing.T) {
	cause := errors.New("dial tcp: secret-token-in-url")
	tests := []struct {
		name string
		err  *app.GitHubError
		want string
	}{
		{"status", &app.GitHubError{Status: 403, RateLimited: true}, "github: HTTP 403"},
		{"transport", &app.GitHubError{Transport: true, Cause: cause}, "github: transport error"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.err.Error(); got != tc.want {
				t.Errorf("Error() = %q, want %q", got, tc.want)
			}
			if strings.Contains(tc.err.Error(), "secret") {
				t.Errorf("Error() = %q leaks the cause", tc.err.Error())
			}
		})
	}

	wrapped := fmt.Errorf("list runs: %w", &app.GitHubError{Status: 404, Cause: cause})
	var ghErr *app.GitHubError
	if !errors.As(wrapped, &ghErr) || ghErr.Status != 404 {
		t.Errorf("errors.As(%v) = %+v, want Status 404", wrapped, ghErr)
	}
	if !errors.Is(wrapped, cause) {
		t.Errorf("errors.Is(%v, cause) = false, want true", wrapped)
	}
}

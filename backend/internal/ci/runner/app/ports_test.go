package app_test

import (
	"errors"
	"fmt"
	"testing"

	"dev-digest-go/backend/internal/ci/runner/app"
)

func TestAPIError(t *testing.T) {
	msg := "GitHub API error fetching PR diff (https://api.github.com/repos/o/r/pulls/1): 404 Not Found"
	wrapped := fmt.Errorf("run: %w", &app.APIError{Status: 404, Message: msg})

	var apiErr *app.APIError
	if !errors.As(wrapped, &apiErr) {
		t.Fatalf("errors.As(%v) = false, want true", wrapped)
	}
	if got := apiErr.Error(); got != msg {
		t.Errorf("Error() = %q, want %q", got, msg)
	}
	if apiErr.Status != 404 {
		t.Errorf("Status = %d, want 404", apiErr.Status)
	}
}

package domain_test

import (
	"testing"

	"dev-digest-go/backend/internal/ci/domain"
)

func TestGitHubErrorCode(t *testing.T) {
	tests := []struct {
		name        string
		status      int
		rateLimited bool
		want        domain.ErrorCode
	}{
		{"429", 429, false, domain.CodeRateLimited},
		{"429 with header", 429, true, domain.CodeRateLimited},
		{"403 with remaining 0", 403, true, domain.CodeRateLimited},
		{"401 with remaining 0", 401, true, domain.CodeRateLimited},
		{"403", 403, false, domain.CodePermissionDenied},
		{"401", 401, false, domain.CodePermissionDenied},
		{"404", 404, false, domain.CodeUnavailable},
		{"500", 500, false, domain.CodeUnavailable},
		{"500 with header", 500, true, domain.CodeUnavailable},
		{"no response", 0, false, domain.CodeUnavailable},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := domain.GitHubErrorCode(tc.status, tc.rateLimited); got != tc.want {
				t.Errorf("GitHubErrorCode(%d, %v) = %q, want %q", tc.status, tc.rateLimited, got, tc.want)
			}
		})
	}
}

func TestPublishErrorCode(t *testing.T) {
	tests := []struct {
		status int
		want   domain.ErrorCode
	}{
		{403, domain.CodePermissionDenied},
		{404, domain.CodePermissionDenied},
		{401, domain.CodeUnavailable},
		{422, domain.CodeUnavailable},
		{429, domain.CodeUnavailable},
		{500, domain.CodeUnavailable},
		{0, domain.CodeUnavailable},
	}
	for _, tc := range tests {
		if got := domain.PublishErrorCode(tc.status); got != tc.want {
			t.Errorf("PublishErrorCode(%d) = %q, want %q", tc.status, got, tc.want)
		}
	}
}

package apperr_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/platform/apperr"
)

func TestConstructors(t *testing.T) {
	tests := []struct {
		name     string
		err      *apperr.Error
		wantKind apperr.Kind
		wantCode string
	}{
		{"invalid input", apperr.InvalidInput("m"), apperr.KindInvalidInput, "invalid_input"},
		{"not found", apperr.NotFound("m"), apperr.KindNotFound, "not_found"},
		{"conflict", apperr.Conflict("m"), apperr.KindConflict, "conflict"},
		{"payload too large", apperr.PayloadTooLarge("m"), apperr.KindPayloadTooLarge, "payload_too_large"},
		{"validation", apperr.Validation("m"), apperr.KindValidation, "validation_error"},
		{"internal", apperr.Internal("m"), apperr.KindInternal, "internal_error"},
		{"config", apperr.Config("m"), apperr.KindConfig, "config_error"},
		{"external service", apperr.ExternalService("m"), apperr.KindExternalService, "external_service"},
		{"forbidden", apperr.Forbidden("m"), apperr.KindForbidden, "forbidden"},
		{"payment required", apperr.PaymentRequired("m"), apperr.KindPaymentRequired, "payment_required"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.err.Kind != tc.wantKind {
				t.Errorf("Kind = %v, want %v", tc.err.Kind, tc.wantKind)
			}
			if tc.err.Code != tc.wantCode {
				t.Errorf("Code = %q, want %q", tc.err.Code, tc.wantCode)
			}
			if tc.err.Message != "m" {
				t.Errorf("Message = %q, want %q", tc.err.Message, "m")
			}
			if tc.err.Details != nil {
				t.Errorf("Details = %v, want nil", tc.err.Details)
			}
		})
	}
}

func TestWithers(t *testing.T) {
	cause := errors.New("db down")
	err := apperr.ExternalService("GitHub unavailable").
		WithCode("github_unavailable").
		WithDetails(map[string]int{"status": 503}).
		WithCause(cause)

	if err.Code != "github_unavailable" {
		t.Errorf("Code = %q, want github_unavailable", err.Code)
	}
	if diff := cmp.Diff(map[string]int{"status": 503}, err.Details); diff != "" {
		t.Errorf("Details mismatch (-want +got):\n%s", diff)
	}
	if !errors.Is(err, cause) {
		t.Errorf("errors.Is(err, cause) = false, want true")
	}
	if got, want := err.Error(), "GitHub unavailable: db down"; got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
	if err.Message != "GitHub unavailable" {
		t.Errorf("Message = %q, must not include the cause", err.Message)
	}
}

func TestAs(t *testing.T) {
	inner := apperr.NotFound("repo missing")
	tests := []struct {
		name   string
		err    error
		wantOK bool
	}{
		{"direct", inner, true},
		{"wrapped", fmt.Errorf("load repo: %w", inner), true},
		{"plain error", errors.New("boom"), false},
		{"nil", nil, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := apperr.As(tc.err)
			if ok != tc.wantOK {
				t.Fatalf("As(%v) ok = %t, want %t", tc.err, ok, tc.wantOK)
			}
			if ok && got != inner {
				t.Errorf("As(%v) = %v, want the wrapped *Error", tc.err, got)
			}
		})
	}
}

func TestKindString(t *testing.T) {
	if got, want := apperr.Kind(0).String(), "unknown"; got != want {
		t.Errorf("Kind(0).String() = %q, want %q", got, want)
	}
	if got, want := apperr.KindExternalService.String(), "external_service"; got != want {
		t.Errorf("KindExternalService.String() = %q, want %q", got, want)
	}
}

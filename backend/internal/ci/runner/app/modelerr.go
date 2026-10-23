package app

import (
	"context"
	"errors"
	"regexp"
	"strconv"
	"strings"

	"dev-digest-go/backend/internal/platform/apperr"
)

// ModelCallError is a failed review-engine call. Its text is content-free by design (spec
// 7.8.9): the error chain of the model layer can hold a cut of the model's raw output, which
// quotes the diff, the prompt or a skill, and none of that may reach the public job log. Only the
// class (and the provider's HTTP status) is printed; Unwrap keeps the cause for errors.Is/As.
type ModelCallError struct {
	// Class is "cancelled", "timeout", "not_configured", "schema_failure", "http <status>", "provider_error" or
	// "error".
	Class string
	cause error
}

func (e *ModelCallError) Error() string { return "the model call failed (" + e.Class + ")" }

// Unwrap returns the model layer's own error.
func (e *ModelCallError) Unwrap() error { return e.cause }

// providerStatus matches "<provider> <operation> returned <status>" at the start of an error
// text, before any provider-supplied part (which follows the first colon).
var providerStatus = regexp.MustCompile(`^[^:]*\breturned (\d{3})\b`)

// modelCallClass names what went wrong without quoting anything the model or the provider said.
func modelCallClass(err error) string {
	switch {
	case errors.Is(err, context.Canceled):
		return "cancelled"
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	}
	if ae, ok := apperr.As(err); ok && ae.Kind == apperr.KindConfig {
		return "not_configured"
	}
	var status interface{ StatusCode() int }
	if errors.As(err, &status) && status.StatusCode() > 0 {
		return "http " + strconv.Itoa(status.StatusCode())
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "failed schema validation"):
		return "schema_failure"
	case strings.Contains(msg, "exceeded its total budget"), strings.Contains(msg, "timed out after"):
		return "timeout"
	}
	if m := providerStatus.FindStringSubmatch(msg); m != nil {
		return "http " + m[1]
	}
	if strings.Contains(msg, "OpenRouter") {
		return "provider_error"
	}
	return "error"
}

package app

import (
	"context"
	"errors"
	"regexp"
	"strconv"
	"strings"

	"dev-digest-go/backend/internal/llm/custom"
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
	// Hint is a fixed sentence that follows the class (the strict-mode hint of FR33); it holds
	// nothing the model or the provider said.
	Hint  string
	cause error
}

func (e *ModelCallError) Error() string {
	msg := "the model call failed (" + e.Class + ")"
	if e.Hint != "" {
		msg += ". " + e.Hint
	}
	return msg
}

// Unwrap returns the model layer's own error.
func (e *ModelCallError) Unwrap() error { return e.cause }

// providerStatus matches "<provider> <operation> returned <status>" at the start of an error
// text, before any provider-supplied part (which follows the first ": "). The provider name is a
// built-in display name or a custom provider id, whose own colon ("custom:<slug>") is part of the
// name; any other colon ends the part that is trusted.
var providerStatus = regexp.MustCompile(`^(?:custom:[a-z0-9-]+)?[^:]*\breturned (\d{3})\b`)

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

// strictModeHint is the FR33 hint for a custom provider in strict mode whose gateway answered a
// generation with HTTP 400 or 422 (class "http 400" / "http 422"): it names the provider id
// (SCR4) and the modes the protocol offers. Empty for every other failure.
func strictModeHint(m Manifest, class string) string {
	cp := m.CustomProvider
	if cp == nil || cp.StructuredOutput != custom.ModeStrict || (class != "http 400" && class != "http 422") {
		return ""
	}
	alternatives := "JSON object or Prompt only"
	if cp.Protocol == custom.ProtocolAnthropic {
		alternatives = "Prompt only"
	}
	return "If this gateway does not support strict structured output, set Structured output of " + m.Provider + " to " + alternatives + "."
}

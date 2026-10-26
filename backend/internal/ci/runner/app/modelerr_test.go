package app

import (
	"errors"
	"testing"
)

// The class is read from the start of the adapters' error text ("<name> <operation> returned
// <status>[: <body>]"); the provider's own words after the first ": " must never count.
func TestModelCallClass_providerStatusText(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		msg  string
		want string
	}{
		{"openrouter", "OpenRouter chat completions returned 429: slow down", "http 429"},
		{"openai", "OpenAI chat completions returned 401: bad key", "http 401"},
		{"anthropic", "Anthropic messages returned 400: invalid tool", "http 400"},
		{"custom provider", "custom:my-gw chat completions returned 400: response_format unsupported", "http 400"},
		{"custom provider, anthropic protocol", "custom:my-gw messages returned 422", "http 422"},
		{"custom provider, no body", "custom:a1-b2 chat completions returned 401", "http 401"},
		{"body quotes a status", "OpenAI chat completions returned 400: upstream returned 500", "http 400"},
		{"custom body quotes a status", "custom:my-gw chat completions returned 400: gateway returned 500", "http 400"},
		{"custom failure without a status, body quotes one", "custom:my-gw failed: upstream returned 500", "error"},
		{"other failure, body quotes one", "something broke: backend returned 502", "error"},
		{"custom id only", "custom:my-gw returned an unreadable response", "error"},
		{"openrouter without a status", "OpenRouter returned no choices for Review", "provider_error"},
		{"schema", "answer failed schema validation: custom:x returned 500", "schema_failure"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := modelCallClass(errors.New(tc.msg)); got != tc.want {
				t.Errorf("modelCallClass(%q) = %q, want %q", tc.msg, got, tc.want)
			}
		})
	}
}

package llm_test

import (
	"testing"

	"dev-digest-go/backend/internal/llm"
)

func TestProviderIsCustom(t *testing.T) {
	tests := []struct {
		p    llm.Provider
		want bool
	}{
		{"custom:synapse", true},
		{"custom:a-1", true},
		{llm.OpenAI, false},
		{llm.Anthropic, false},
		{llm.OpenRouter, false},
		{"custom:", false},
		{"custom:Syn", false},
		{"custom:a_b", false},
		{"", false},
	}
	for _, tc := range tests {
		if got := tc.p.IsCustom(); got != tc.want {
			t.Errorf("Provider(%q).IsCustom() = %v, want %v", tc.p, got, tc.want)
		}
	}
	// A custom id is not a built-in: Valid, KeyName and ParseProvider stay closed.
	if _, ok := llm.ParseProvider("custom:synapse"); ok || llm.Provider("custom:synapse").Valid() || llm.Provider("custom:synapse").KeyName() != "" {
		t.Error("custom ids must not be built-in providers")
	}
}

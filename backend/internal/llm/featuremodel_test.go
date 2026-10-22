package llm_test

import (
	"encoding/json"
	"testing"

	"dev-digest-go/backend/internal/llm"
)

func TestResolveFeatureModel(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		bag          map[string]json.RawMessage
		feature      string
		wantProvider llm.Provider
		wantModel    string
	}{
		{"stored choice", map[string]json.RawMessage{"feature_models": json.RawMessage(`{"conventions":{"provider":"anthropic","model":"claude-haiku-4-5"}}`)}, "conventions", llm.Anthropic, "claude-haiku-4-5"},
		{"absent bag", nil, "conventions", llm.OpenAI, "gpt-5.4"},
		{"other feature stored", map[string]json.RawMessage{"feature_models": json.RawMessage(`{"risk_brief":{"provider":"anthropic","model":"m"}}`)}, "conventions", llm.OpenAI, "gpt-5.4"},
		{"bad provider", map[string]json.RawMessage{"feature_models": json.RawMessage(`{"conventions":{"provider":"bad","model":"m"}}`)}, "conventions", llm.OpenAI, "gpt-5.4"},
		{"empty model", map[string]json.RawMessage{"feature_models": json.RawMessage(`{"conventions":{"provider":"openai","model":""}}`)}, "conventions", llm.OpenAI, "gpt-5.4"},
		{"not an object", map[string]json.RawMessage{"feature_models": json.RawMessage(`[]`)}, "conventions", llm.OpenAI, "gpt-5.4"},
		{"openrouter default", nil, "onboarding", llm.OpenRouter, "deepseek/deepseek-v4-flash"},
		{"unknown feature", nil, "nope", "", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p, m := llm.ResolveFeatureModel(tc.bag, tc.feature)
			if p != tc.wantProvider || m != tc.wantModel {
				t.Errorf("ResolveFeatureModel(%s) = %q, %q; want %q, %q", tc.feature, p, m, tc.wantProvider, tc.wantModel)
			}
		})
	}
}

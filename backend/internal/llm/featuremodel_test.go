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
		{"custom id accepted", map[string]json.RawMessage{"feature_models": json.RawMessage(`{"conventions":{"provider":"custom:synapse","model":"gpt-x"}}`)}, "conventions", "custom:synapse", "gpt-x"},
		{"undefined custom id still accepted", map[string]json.RawMessage{"default_model": json.RawMessage(`{"provider":"custom:ghost","model":"m"}`)}, "risk_brief", "custom:ghost", "m"},
		{"malformed custom id skipped", map[string]json.RawMessage{"feature_models": json.RawMessage(`{"conventions":{"provider":"custom:Bad!","model":"m"}}`)}, "conventions", llm.OpenAI, "gpt-5.4"},
		{"bare custom prefix skipped", map[string]json.RawMessage{"default_model": json.RawMessage(`{"provider":"custom:","model":"m"}`)}, "conventions", llm.OpenAI, "gpt-5.4"},
		{"custom id with empty model skipped", map[string]json.RawMessage{"default_model": json.RawMessage(`{"provider":"custom:synapse","model":""}`)}, "conventions", llm.OpenAI, "gpt-5.4"},
		{"empty model", map[string]json.RawMessage{"feature_models": json.RawMessage(`{"conventions":{"provider":"openai","model":""}}`)}, "conventions", llm.OpenAI, "gpt-5.4"},
		{"not an object", map[string]json.RawMessage{"feature_models": json.RawMessage(`[]`)}, "conventions", llm.OpenAI, "gpt-5.4"},
		{"openrouter default", nil, "onboarding", llm.OpenRouter, "deepseek/deepseek-v4-flash"},
		{"unknown feature", nil, "nope", "", ""},
		{"default_model used without choice", map[string]json.RawMessage{"default_model": json.RawMessage(`{"provider":"openrouter","model":"a/x"}`)}, "conventions", llm.OpenRouter, "a/x"},
		{"default_model used when feature_models lacks feature", map[string]json.RawMessage{
			"feature_models": json.RawMessage(`{"risk_brief":{"provider":"anthropic","model":"m"}}`),
			"default_model":  json.RawMessage(`{"provider":"openrouter","model":"a/x"}`)}, "onboarding", llm.OpenRouter, "a/x"},
		{"own choice beats default_model", map[string]json.RawMessage{
			"feature_models": json.RawMessage(`{"conventions":{"provider":"anthropic","model":"claude-haiku-4-5"}}`),
			"default_model":  json.RawMessage(`{"provider":"openrouter","model":"a/x"}`)}, "conventions", llm.Anthropic, "claude-haiku-4-5"},
		{"invalid own choice falls to default_model", map[string]json.RawMessage{
			"feature_models": json.RawMessage(`{"conventions":{"provider":"bad","model":"m"}}`),
			"default_model":  json.RawMessage(`{"provider":"openrouter","model":"a/x"}`)}, "conventions", llm.OpenRouter, "a/x"},
		{"default_model applies to unknown feature", map[string]json.RawMessage{"default_model": json.RawMessage(`{"provider":"openai","model":"gpt-x"}`)}, "nope", llm.OpenAI, "gpt-x"},
		{"default_model null falls to registry", map[string]json.RawMessage{"default_model": json.RawMessage(`null`)}, "conventions", llm.OpenAI, "gpt-5.4"},
		{"default_model bad provider falls to registry", map[string]json.RawMessage{"default_model": json.RawMessage(`{"provider":"foo","model":"m"}`)}, "risk_brief", llm.OpenAI, "gpt-4.1"},
		{"default_model empty model falls to registry", map[string]json.RawMessage{"default_model": json.RawMessage(`{"provider":"openai","model":""}`)}, "risk_brief", llm.OpenAI, "gpt-4.1"},
		{"default_model not an object falls to registry", map[string]json.RawMessage{"default_model": json.RawMessage(`"gpt"`)}, "risk_brief", llm.OpenAI, "gpt-4.1"},
		{"feature_models malformed uses default_model", map[string]json.RawMessage{
			"feature_models": json.RawMessage(`[]`),
			"default_model":  json.RawMessage(`{"provider":"anthropic","model":"m"}`)}, "conventions", llm.Anthropic, "m"},
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

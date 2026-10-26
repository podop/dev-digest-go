package llm

import "encoding/json"

// defaultFeatureModels is the registry default per feature (Node constants/feature-models.ts).
var defaultFeatureModels = map[string]struct {
	provider Provider
	model    string
}{
	"onboarding":    {OpenRouter, "deepseek/deepseek-v4-flash"},
	"review_intent": {OpenRouter, "deepseek/deepseek-v4-flash"},
	"risk_brief":    {OpenAI, "gpt-4.1"},
	"conformance":   {OpenAI, "gpt-4.1"},
	"conventions":   {OpenAI, "gpt-5.4"},
}

// ResolveFeatureModel returns the provider and model a feature uses right now, in this order:
// the stored choice feature_models.<feature>, the workspace default_model, the registry default.
// A stored value only counts when it is a valid {provider, model} choice (known provider or a
// well-formed custom id, non-empty model); an invalid one is skipped, not an error. bag is the settings bag
// (settings.Bag); an unknown feature without any valid stored value yields ("", "").
func ResolveFeatureModel(bag map[string]json.RawMessage, feature string) (Provider, string) {
	if raw, ok := bag["feature_models"]; ok {
		var models map[string]json.RawMessage
		if json.Unmarshal(raw, &models) == nil {
			if p, m, ok := parseChoice(models[feature]); ok {
				return p, m
			}
		}
	}
	if p, m, ok := parseChoice(bag["default_model"]); ok {
		return p, m
	}
	d := defaultFeatureModels[feature]
	return d.provider, d.model
}

// parseChoice decodes a {provider, model} setting value; ok is false for absent, null,
// malformed, unknown-provider or empty-model values.
func parseChoice(raw json.RawMessage) (Provider, string, bool) {
	var choice struct {
		Provider string `json:"provider"`
		Model    string `json:"model"`
	}
	if json.Unmarshal(raw, &choice) != nil {
		return "", "", false
	}
	p, ok := ParseProvider(choice.Provider)
	if !ok {
		// A well-formed custom id counts even when no provider of that id is defined: whether it is
		// callable is decided per call (FR9), not by the registry.
		p = Provider(choice.Provider)
		ok = p.IsCustom()
	}
	if !ok || choice.Model == "" {
		return "", "", false
	}
	return p, choice.Model, true
}

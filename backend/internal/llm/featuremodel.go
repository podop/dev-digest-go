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

// ResolveFeatureModel returns the provider and model a feature uses right now: the stored
// setting feature_models.<feature> when it is a valid {provider, model} choice, else the
// registry default. bag is the settings bag (settings.Bag); an unknown feature without a valid
// stored choice yields ("", "").
func ResolveFeatureModel(bag map[string]json.RawMessage, feature string) (Provider, string) {
	if raw, ok := bag["feature_models"]; ok {
		var models map[string]json.RawMessage
		if json.Unmarshal(raw, &models) == nil {
			var choice struct {
				Provider string `json:"provider"`
				Model    string `json:"model"`
			}
			if json.Unmarshal(models[feature], &choice) == nil {
				if p, ok := ParseProvider(choice.Provider); ok && choice.Model != "" {
					return p, choice.Model
				}
			}
		}
	}
	d := defaultFeatureModels[feature]
	return d.provider, d.model
}

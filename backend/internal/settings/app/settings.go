// Package app holds the settings use cases: the non-secret preference bag of a workspace,
// which provider keys are configured, and the provider connection test.
package app

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"dev-digest-go/backend/internal/platform/logging"
	"dev-digest-go/backend/internal/platform/secrets"
)

// Provider is a connection-test target.
type Provider string

// The connection-test providers.
const (
	OpenAI     Provider = "openai"
	Anthropic  Provider = "anthropic"
	OpenRouter Provider = "openrouter"
	GitHub     Provider = "github"
)

// Providers lists every connection-test provider, in the order of SecretsStatus.
var Providers = []Provider{OpenAI, Anthropic, OpenRouter, GitHub}

// ProviderNames returns the wire names of Providers.
func ProviderNames() []string {
	names := make([]string, len(Providers))
	for i, p := range Providers {
		names[i] = string(p)
	}
	return names
}

// SecretName returns the secrets-store name the provider's key is saved under.
func (p Provider) SecretName() string {
	switch p {
	case OpenAI:
		return secrets.OpenAIKey
	case Anthropic:
		return secrets.AnthropicKey
	case OpenRouter:
		return secrets.OpenRouterKey
	case GitHub:
		return secrets.GitHubToken
	default:
		return ""
	}
}

// Bag is the stored settings of a workspace: key to raw JSON value. A SQL NULL or JSON null
// value is the JSON literal null.
type Bag = map[string]json.RawMessage

// SecretsStatus says which keys are configured. It never carries a value.
type SecretsStatus struct {
	OpenAI     bool `json:"openai"`
	Anthropic  bool `json:"anthropic"`
	OpenRouter bool `json:"openrouter"`
	GitHub     bool `json:"github"`
}

// ConnTestResult is the outcome of a connection test; a failed test is data, not an error.
type ConnTestResult struct {
	Provider Provider `json:"provider"`
	OK       bool     `json:"ok"`
	Message  string   `json:"message"`
}

// store persists the settings bag.
type store interface {
	// List returns every stored setting of the workspace.
	List(ctx context.Context, workspaceID string) (Bag, error)
	// Upsert writes all entries for the user, or none of them.
	Upsert(ctx context.Context, workspaceID, userID string, entries Bag) error
}

// secretStore is the secrets file with environment fallback (platform/secrets.Store).
type secretStore interface {
	Get(name string) string
	Set(name, value string) error
}

// githubClient checks the GitHub token (platform/github.Client).
type githubClient interface {
	CurrentLogin(ctx context.Context) (string, error)
}

// modelLister counts the models a provider lists for a key (adapters/providers).
type modelLister interface {
	CountModels(ctx context.Context, provider Provider, key string) (int, error)
}

// Deps are the ports a Service needs.
type Deps struct {
	Store     store
	Secrets   secretStore
	GitHub    githubClient
	Providers modelLister
}

// Service implements the settings use cases.
type Service struct {
	d Deps
}

// NewService builds a Service. A nil dependency is a wiring bug and panics.
func NewService(d Deps) *Service {
	if d.Store == nil || d.Secrets == nil || d.GitHub == nil || d.Providers == nil {
		panic("settings/app.NewService: every dependency is required")
	}
	return &Service{d: d}
}

// Get returns every stored setting of the workspace; no defaults are injected.
func (s *Service) Get(ctx context.Context, workspaceID string) (Bag, error) {
	bag, err := s.d.Store.List(ctx, workspaceID)
	if err != nil {
		return nil, fmt.Errorf("list settings: %w", err)
	}
	if bag == nil {
		bag = Bag{}
	}
	return bag, nil
}

// Update upserts every entry for the user and returns the full stored bag after the write.
func (s *Service) Update(ctx context.Context, workspaceID, userID string, entries Bag) (Bag, error) {
	if len(entries) > 0 {
		if err := s.d.Store.Upsert(ctx, workspaceID, userID, entries); err != nil {
			return nil, fmt.Errorf("upsert settings: %w", err)
		}
	}
	return s.Get(ctx, workspaceID)
}

// SecretsStatus reports, per provider, whether a non-empty key exists in the secrets file
// or the environment.
func (s *Service) SecretsStatus() SecretsStatus {
	has := func(p Provider) bool { return s.d.Secrets.Get(p.SecretName()) != "" }
	return SecretsStatus{OpenAI: has(OpenAI), Anthropic: has(Anthropic), OpenRouter: has(OpenRouter), GitHub: has(GitHub)}
}

// TestConnection saves key (when given) under the provider's secret name, then checks the
// provider with the stored key. The key stays saved when the check fails. Every failure comes
// back as ok=false with the key value scrubbed from the message.
func (s *Service) TestConnection(ctx context.Context, provider Provider, key string) ConnTestResult {
	fail := func(msg string) ConnTestResult { return ConnTestResult{Provider: provider, Message: msg} }
	name := provider.SecretName()
	if name == "" {
		return fail("Unknown provider")
	}

	if key != "" {
		if err := s.d.Secrets.Set(name, key); err != nil {
			return fail(scrub("Could not save the key: "+err.Error(), key))
		}
	}
	stored := s.d.Secrets.Get(name)
	if stored == "" {
		return fail(name + " is not configured")
	}

	if provider == GitHub {
		login, err := s.d.GitHub.CurrentLogin(ctx)
		if err != nil {
			return fail(scrub(err.Error(), stored))
		}
		return ConnTestResult{Provider: provider, OK: true, Message: "Connected as @" + login}
	}

	n, err := s.d.Providers.CountModels(ctx, provider, stored)
	if err != nil {
		return fail(scrub(err.Error(), stored))
	}
	return ConnTestResult{Provider: provider, OK: true, Message: fmt.Sprintf("OK — %d models available", n)}
}

// scrub replaces every occurrence of the secret in msg.
func scrub(msg, secret string) string {
	if secret == "" {
		return msg
	}
	return strings.ReplaceAll(msg, secret, logging.Redacted)
}

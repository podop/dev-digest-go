// Package app holds the settings use cases: the non-secret preference bag of a workspace,
// which provider keys are configured, and the provider connection test.
package app

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"dev-digest-go/backend/internal/platform/github"
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
// The optional fields are set only on success, and only the ones that belong to the provider:
// LatencyMS for all, Model and ModelReachable for the LLM providers, Login and Scopes for GitHub.
type ConnTestResult struct {
	Provider Provider
	OK       bool
	Message  string
	// LatencyMS is the wall time of the provider call.
	LatencyMS *int
	// Model is the checked model id; nil when none could be chosen (an LLM provider that listed
	// no model and has no default).
	Model *string
	// ModelReachable says whether Model is in the provider's full model list.
	ModelReachable *bool
	// Login is the owner of the GitHub token.
	Login *string
	// Scopes is the X-OAuth-Scopes list of the token: empty when the header was empty, nil
	// when GitHub did not report scopes (fine-grained token).
	Scopes []string
}

// referenceModels is the model a test checks for a provider without a workspace default_model;
// Anthropic has none, its first listed model is checked.
var referenceModels = map[Provider]string{
	OpenRouter: "deepseek/deepseek-v4-flash",
	OpenAI:     "gpt-4.1",
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

// githubClient checks the GitHub token (platform/github.Client); it never follows redirects.
type githubClient interface {
	CurrentUser(ctx context.Context) (github.User, error)
}

// modelLister lists the models a provider offers for a key (adapters/providers).
type modelLister interface {
	ListModels(ctx context.Context, provider Provider, key string) (ModelList, error)
}

// Deps are the ports a Service needs. Clock and Logger are optional: the clock defaults to
// time.Now and a missing logger discards its output.
type Deps struct {
	Store     store
	Secrets   secretStore
	GitHub    githubClient
	Providers modelLister
	Clock     func() time.Time
	Logger    *slog.Logger
}

// Service implements the settings use cases.
type Service struct {
	d Deps
}

// NewService builds a Service. A nil required dependency is a wiring bug and panics.
func NewService(d Deps) *Service {
	if d.Store == nil || d.Secrets == nil || d.GitHub == nil || d.Providers == nil {
		panic("settings/app.NewService: every dependency is required")
	}
	if d.Clock == nil {
		d.Clock = time.Now
	}
	if d.Logger == nil {
		d.Logger = slog.New(slog.DiscardHandler)
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
// back as ok=false with the key value scrubbed from the message and no optional fields.
//
// workspaceID picks the default_model whose model an LLM provider's check reports; it may be
// empty (the route has no workspace on a database that was never seeded), then the provider's
// reference model is checked. Each test logs one INFO line with provider, ok and latency; the
// key never reaches a log line.
func (s *Service) TestConnection(ctx context.Context, workspaceID string, provider Provider, key string) ConnTestResult {
	start := s.d.Clock()
	res := s.testConnection(ctx, workspaceID, provider, key)
	s.d.Logger.InfoContext(ctx, "settings test connection",
		slog.String("provider", string(provider)),
		slog.Bool("ok", res.OK),
		slog.Int64("latency_ms", s.d.Clock().Sub(start).Milliseconds()))
	return res
}

func (s *Service) testConnection(ctx context.Context, workspaceID string, provider Provider, key string) ConnTestResult {
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
		started := s.d.Clock()
		user, err := s.d.GitHub.CurrentUser(ctx)
		latency := s.latencyMS(started)
		if err != nil {
			return fail(scrub(err.Error(), stored))
		}
		res := ConnTestResult{Provider: provider, OK: true, Message: "Connected as @" + user.Login, LatencyMS: &latency, Login: &user.Login}
		if user.ScopesReported {
			res.Scopes = slices.Clone(user.Scopes)
			if res.Scopes == nil {
				res.Scopes = []string{}
			}
		}
		return res
	}

	started := s.d.Clock()
	list, err := s.d.Providers.ListModels(ctx, provider, stored)
	latency := s.latencyMS(started)
	if err != nil {
		return fail(scrub(err.Error(), stored))
	}
	res := ConnTestResult{
		Provider: provider, OK: true, LatencyMS: &latency,
		Message: fmt.Sprintf("OK — %d models available", list.Count),
	}
	if model := s.checkedModel(ctx, workspaceID, provider, list.IDs); model != "" {
		reachable := slices.Contains(list.IDs, model)
		res.Model, res.ModelReachable = &model, &reachable
	}
	return res
}

// checkedModel is the model id whose presence in ids a test reports: the workspace default_model
// when it names this provider, else the provider's reference model (Anthropic: the first listed
// id). "" means there is none. A workspace that cannot be read is not an error: the reference
// model is used and a warning logged.
func (s *Service) checkedModel(ctx context.Context, workspaceID string, provider Provider, ids []string) string {
	if model, ok := s.defaultModelFor(ctx, workspaceID, provider); ok {
		return model
	}
	if model, ok := referenceModels[provider]; ok {
		return model
	}
	if len(ids) > 0 {
		return ids[0]
	}
	return ""
}

// defaultModelFor returns the model of the workspace's default_model when it is set for provider.
func (s *Service) defaultModelFor(ctx context.Context, workspaceID string, provider Provider) (string, bool) {
	if workspaceID == "" {
		s.d.Logger.WarnContext(ctx, "settings test connection: no workspace, checking the reference model",
			slog.String("provider", string(provider)))
		return "", false
	}
	bag, err := s.d.Store.List(ctx, workspaceID)
	if err != nil {
		s.d.Logger.WarnContext(ctx, "settings test connection: default_model unreadable, checking the reference model",
			slog.String("provider", string(provider)), slog.String("error", err.Error()))
		return "", false
	}
	var choice struct {
		Provider string `json:"provider"`
		Model    string `json:"model"`
	}
	if json.Unmarshal(bag["default_model"], &choice) != nil || Provider(choice.Provider) != provider || choice.Model == "" {
		return "", false
	}
	return choice.Model, true
}

// latencyMS is the whole milliseconds since started, never negative.
func (s *Service) latencyMS(started time.Time) int {
	return max(0, int(s.d.Clock().Sub(started).Milliseconds()))
}

// scrub replaces every occurrence of the secret in msg.
func scrub(msg, secret string) string {
	if secret == "" {
		return msg
	}
	return strings.ReplaceAll(msg, secret, logging.Redacted)
}

// Package app holds the settings use cases: the non-secret preference bag of a workspace,
// which provider keys are configured, and the provider connection test.
package app

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"strings"
	"time"

	"dev-digest-go/backend/internal/llm/custom"
	"dev-digest-go/backend/internal/platform/apperr"
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
	// GitLab is tested per connection (a connection id), not through a single stored key; it is
	// not one of Providers.
	GitLab Provider = "gitlab"
)

// IsCustom reports whether p is a well-formed custom provider id (`custom:<slug>`). Such a
// provider is not one of Providers: it is tested through the custom providers port.
func (p Provider) IsCustom() bool {
	_, ok := custom.ParseID(string(p))
	return ok
}

// Providers lists every provider with a stored key, in the order of SecretsStatus.
var Providers = []Provider{OpenAI, Anthropic, OpenRouter, GitHub}

// ProviderNames returns the wire names of Providers.
func ProviderNames() []string {
	names := make([]string, len(Providers))
	for i, p := range Providers {
		names[i] = string(p)
	}
	return names
}

// ConnTestProviderNames returns the wire names a connection test accepts besides a custom
// provider id: Providers, then gitlab (the ConnTestProvider enum of the UI contract).
func ConnTestProviderNames() []string {
	return append(ProviderNames(), string(GitLab))
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
	// ModelReachable says whether Model is in the provider's full model list. A custom provider
	// whose gateway has no model list leaves it nil.
	ModelReachable *bool
	// Warning is set only by a custom provider test that passed without checking the key (a
	// gateway that answered 404 or 405 to the model list).
	Warning *string
	// Login is the owner of the GitHub token.
	Login *string
	// Scopes is the X-OAuth-Scopes list of the token: empty when the header was empty, nil
	// when GitHub did not report scopes (fine-grained token). A GitLab test sets it from
	// /personal_access_tokens/self (nil when that call failed).
	Scopes []string

	// The fields below belong to a GitLab test (FR10, FR11); the result carries Login and Scopes
	// too. Each is nil when unknown or when the test failed.

	// ExpiresAt is the token's expiry as YYYY-MM-DD; nil = no expiry or not reported.
	ExpiresAt *string
	// InstanceVersion is the version of GET /metadata (for example 17.5.1-ee).
	InstanceVersion *string
	// TokenAccess is "full" or "read_only"; nil when the scopes are unknown.
	TokenAccess *string
	// Warnings are the hints of a passed test, in the FR11 order; empty when there are none.
	Warnings []TestWarning
}

// TestWarning is one hint of a successful GitLab test.
type TestWarning struct {
	Code    string
	Message string
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

// customProviders is the settings view of the custom providers context; cmd binds it to
// customproviders/app.Service. The settings use cases never import that context.
type customProviders interface {
	// Defined reports whether the workspace defines the custom provider id (`custom:<slug>`). A
	// store failure counts as not defined: the caller answers a validation error and nothing is
	// written on a guess.
	Defined(ctx context.Context, workspaceID, id string) bool
	// TestConnection runs the connection test of a defined custom provider: it saves key first when
	// given, lists the models once and never completes. It returns an apperr NotFound with code
	// custom_provider_not_found for an undefined id (nothing saved); any other failure of the
	// check is data (OK false, scrubbed message), not an error. The result's Provider is id.
	TestConnection(ctx context.Context, workspaceID, id, key string) (ConnTestResult, error)
}

// GitLabTester is the settings view of the GitLab connections context; cmd binds it to an adapter
// over gitlabconn/app.Service. The settings use cases never import that context.
type GitLabTester interface {
	// TestConnection runs the connection test of the GitLab connection connectionID (FR9, FR10): it
	// saves key as the connection's token first when given (and keeps it when the test fails),
	// makes the three calls and evaluates the token. It returns an apperr NotFound with code
	// gitlab_connection_not_found for an unknown id (nothing saved); any failure of the check
	// itself, including a connection without a token, is data (OK false, scrubbed message), not
	// an error. The result carries Login, Scopes, LatencyMS, ExpiresAt, InstanceVersion,
	// TokenAccess and Warnings; the Service sets Provider.
	TestConnection(ctx context.Context, connectionID, key string) (ConnTestResult, error)
}

// Deps are the ports a Service needs. Custom, GitLab, Clock and Logger are optional: a nil Custom
// means the workspace has no custom providers (every custom id is undefined), a nil GitLab means
// there are no GitLab connections (every connection id is unknown), the clock defaults to
// time.Now and a missing logger discards its output.
type Deps struct {
	Store     store
	Secrets   secretStore
	GitHub    githubClient
	Providers modelLister
	Custom    customProviders
	GitLab    GitLabTester
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

// Get returns every stored setting of the workspace. Only automatic_reviews gets a default: it
// is always present, as the full object (FR1); no other defaults are injected.
func (s *Service) Get(ctx context.Context, workspaceID string) (Bag, error) {
	bag, err := s.d.Store.List(ctx, workspaceID)
	if err != nil {
		return nil, fmt.Errorf("list settings: %w", err)
	}
	if bag == nil {
		bag = Bag{}
	}
	bag[AutomaticReviewsKey] = AutomaticReviewsFromStored(bag[AutomaticReviewsKey]).JSON()
	return bag, nil
}

// Update upserts every entry for the user and returns the full stored bag after the write.
// An automatic_reviews input (a validated boolean or partial object) is first merged into the
// stored value and written as the full object with the server-set enabled_at (FR2). The merge is
// a read then a write without a lock: concurrent updates are last write wins (A10).
func (s *Service) Update(ctx context.Context, workspaceID, userID string, entries Bag) (Bag, error) {
	if input, ok := entries[AutomaticReviewsKey]; ok {
		current, err := s.d.Store.List(ctx, workspaceID)
		if err != nil {
			return nil, fmt.Errorf("list settings: %w", err)
		}
		merged, err := mergeAutomaticReviews(input, current[AutomaticReviewsKey], s.d.Clock())
		if err != nil {
			return nil, err
		}
		entries = maps.Clone(entries)
		entries[AutomaticReviewsKey] = merged
	}
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

// CustomDefined reports whether the workspace defines the custom provider id; it is the check
// behind PUT /settings (FR7). Without a custom providers port nothing is defined.
func (s *Service) CustomDefined(ctx context.Context, workspaceID, id string) bool {
	if s.d.Custom == nil || workspaceID == "" {
		return false
	}
	if _, ok := custom.ParseID(id); !ok {
		return false
	}
	return s.d.Custom.Defined(ctx, workspaceID, id)
}

// TestCustomConnection tests the custom provider id through the custom providers port (FR11): the
// key (when given) is saved by the port, the check is one model-list call. An undefined or
// malformed id is a 404 custom_provider_not_found error and nothing is saved; the check's own
// failures come back as ok=false data. Each test logs one INFO line with provider, ok and
// latency; the key never reaches a log line.
func (s *Service) TestCustomConnection(ctx context.Context, workspaceID string, id Provider, key string) (ConnTestResult, error) {
	notFound := func() error {
		return apperr.NotFound("Custom provider " + string(id) + " is not configured").WithCode("custom_provider_not_found")
	}
	if s.d.Custom == nil || workspaceID == "" || !id.IsCustom() {
		return ConnTestResult{}, notFound()
	}
	start := s.d.Clock()
	res, err := s.d.Custom.TestConnection(ctx, workspaceID, string(id), key)
	if err != nil {
		return ConnTestResult{}, err
	}
	res.Provider = id
	res.Message = scrub(res.Message, key)
	if res.Warning != nil {
		w := scrub(*res.Warning, key)
		res.Warning = &w
	}
	s.d.Logger.InfoContext(ctx, "settings test connection",
		slog.String("provider", string(id)),
		slog.Bool("ok", res.OK),
		slog.Int64("latency_ms", s.d.Clock().Sub(start).Milliseconds()))
	return res, nil
}

// TestGitLabConnection tests the GitLab connection connectionID through the GitLab port (FR9): the
// key (when given) is saved by the port. An unknown or empty id is a 404 gitlab_connection_not_found
// error and nothing is saved; the check's own failures come back as ok=false data with the key
// value scrubbed. Each test logs one INFO line with provider, ok and latency; neither the key nor
// the connection id reaches a log line.
func (s *Service) TestGitLabConnection(ctx context.Context, connectionID, key string) (ConnTestResult, error) {
	if s.d.GitLab == nil || connectionID == "" {
		return ConnTestResult{}, gitLabNotFound()
	}
	start := s.d.Clock()
	res, err := s.d.GitLab.TestConnection(ctx, connectionID, key)
	if err != nil {
		return ConnTestResult{}, err
	}
	res.Provider = GitLab
	res.Message = scrub(res.Message, key)
	if len(res.Warnings) > 0 {
		warnings := make([]TestWarning, len(res.Warnings))
		for i, w := range res.Warnings {
			warnings[i] = TestWarning{Code: w.Code, Message: scrub(w.Message, key)}
		}
		res.Warnings = warnings
	}
	s.d.Logger.InfoContext(ctx, "settings test connection",
		slog.String("provider", string(GitLab)),
		slog.Bool("ok", res.OK),
		slog.Int64("latency_ms", s.d.Clock().Sub(start).Milliseconds()))
	return res, nil
}

func gitLabNotFound() error {
	return apperr.NotFound("GitLab connection not found").WithCode("gitlab_connection_not_found")
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

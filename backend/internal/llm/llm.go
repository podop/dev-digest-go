// Package llm is the minimal structured-completion layer (Phase 2, decision D11): one port that
// asks a model for JSON matching a schema, validates the answer, re-prompts on failure and
// accounts tokens and cost, plus model listing, a price book, a content-free prompt log and the
// feature-model registry.
//
// The package knows no HTTP. A provider is an Adapter (adapters/openaicompat, adapters/anthropic,
// adapters/mock) that performs exactly one request; retries, the per-call budget, re-prompts,
// key lookup and redaction live here. Keys are read from the secrets store on every call and
// never reach a log line or an error text.
package llm

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"dev-digest-go/backend/internal/llm/custom"
	"dev-digest-go/backend/internal/platform/keynames"
	"dev-digest-go/backend/internal/platform/retry"
)

// Provider identifies a model provider on the wire and in settings.
type Provider string

// The supported providers.
const (
	OpenAI     Provider = "openai"
	Anthropic  Provider = "anthropic"
	OpenRouter Provider = "openrouter"
)

// Providers lists every provider in a stable order.
func Providers() []Provider { return []Provider{OpenAI, Anthropic, OpenRouter} }

// ParseProvider converts a wire value to a Provider.
func ParseProvider(s string) (Provider, bool) {
	p := Provider(s)
	return p, p.Valid()
}

// Valid reports whether p is one of the supported providers.
func (p Provider) Valid() bool {
	switch p {
	case OpenAI, Anthropic, OpenRouter:
		return true
	}
	return false
}

// DisplayName is the provider's name in error texts (OpenAI, Anthropic, OpenRouter).
func (p Provider) DisplayName() string {
	switch p {
	case OpenAI:
		return "OpenAI"
	case Anthropic:
		return "Anthropic"
	case OpenRouter:
		return "OpenRouter"
	}
	return string(p)
}

// KeyName is the secrets-store name of the provider's API key.
func (p Provider) KeyName() string {
	switch p {
	case OpenAI:
		return keynames.OpenAIKey
	case Anthropic:
		return keynames.AnthropicKey
	case OpenRouter:
		return keynames.OpenRouterKey
	}
	return ""
}

// IsCustom reports whether p is the id of a custom provider: `custom:` plus a well-formed slug
// (custom.ParseID). It says nothing about whether the provider is defined; Valid stays true for
// the built-in providers only.
func (p Provider) IsCustom() bool {
	_, ok := custom.ParseID(string(p))
	return ok
}

// Role is the author of a message.
type Role string

// Message roles.
const (
	System    Role = "system"
	User      Role = "user"
	Assistant Role = "assistant"
)

// Message is one text message of a conversation.
type Message struct {
	Role    Role
	Content string
}

// Defaults of the structured call (spec FR205, FR210).
const (
	// DefaultReprompts is the number of re-prompts when Request.Reprompts is 0.
	DefaultReprompts = 2
	// NoReprompts in Request.Reprompts makes the call give up after its first attempt.
	NoReprompts = -1
	// DefaultTotalBudget bounds one structured call: every request, retry and re-prompt. It is
	// twice Node's 360 s, so a slow gateway model keeps room for re-prompts after a long request
	// (the review request alone may take reviewer/domain.RequestTimeout, 300 s).
	DefaultTotalBudget = 720 * time.Second
)

// Request is one structured completion. The context carries cancellation.
type Request struct {
	Provider Provider
	Model    string
	Messages []Message
	// SchemaName names the response schema (OpenAI json_schema name, Anthropic tool name).
	SchemaName string
	// Schema is the JSON Schema of the answer. The answer is validated against it as written
	// (array length limits included); the provider receives its strict form.
	Schema json.RawMessage
	// Temperature is sent only when set (and the model accepts it).
	Temperature *float64
	// MaxTokens caps the output; 0 = provider default.
	MaxTokens int
	// Timeout bounds each HTTP request; 0 = the provider default (120 s, OpenRouter 180 s).
	Timeout time.Duration
	// Reprompts is the number of re-prompts after a bad answer: 0 = DefaultReprompts,
	// NoReprompts = none, n > 0 = n.
	Reprompts int

	// Feature and CorrelationID label the prompt_assembled log line.
	Feature       string
	CorrelationID string
	// Sections describes the prompt for that line. Empty = one section per message.
	Sections []PromptSection
	// Chunk is the map-reduce position of this call; the prompt_assembled line carries it when set.
	Chunk *Chunk

	// SessionID groups related calls in the OpenRouter dashboard (body field session_id); other
	// providers ignore it.
	SessionID string
	// OnUsage is called once per received response, schema-invalid ones included, with that
	// attempt's usage, so a caller can account spend when the call ends in an error. It is
	// observational: it runs on the calling goroutine and a panic in it is recovered and logged.
	OnUsage func(AttemptUsage)
	// Charge labels the call for the spend ledger: whose money it is and which unit of work it
	// belongs to. With Options.Recorder set, a nil (or incomplete) Charge is logged as one
	// warning and the call proceeds unrecorded.
	Charge *Charge
}

// UsageKind names the unit of work an LLM call is charged to (the ledger's kind column).
type UsageKind string

// The kinds a call can be charged to. The CI-ingested run kind is not one of them: those costs
// arrive through the CI ingest, not through a call.
const (
	KindReviewRun       UsageKind = "review_run"
	KindReviewIntent    UsageKind = "review_intent"
	KindIntentRefresh   UsageKind = "intent_refresh"
	KindBrief           UsageKind = "brief"
	KindOnboarding      UsageKind = "onboarding"
	KindConventionsScan UsageKind = "conventions_scan"
	KindEvalCase        UsageKind = "eval_case"
)

// Valid reports whether k is one of the kinds above.
func (k UsageKind) Valid() bool {
	switch k {
	case KindReviewRun, KindReviewIntent, KindIntentRefresh, KindBrief, KindOnboarding,
		KindConventionsScan, KindEvalCase:
		return true
	}
	return false
}

// Charge is the ledger label of one call: the workspace that pays, the kind of work and the id of
// the unit (review run, pull request, repo, scan or eval case, as the kind says).
type Charge struct {
	WorkspaceID string
	Kind        UsageKind
	RefID       string
}

// complete reports whether the label can be recorded: a workspace and a known kind.
func (c *Charge) complete() bool { return c != nil && c.WorkspaceID != "" && c.Kind.Valid() }

// RecordedUsage is one answered attempt as the Recorder receives it.
type RecordedUsage struct {
	Charge   Charge
	Provider Provider
	Model    string
	Tokens   Usage
	// CostUSD is the provider-reported cost, else the price book's estimate; nil = unpriced.
	CostUSD *float64
}

// Recorder receives the usage of every answered attempt of a call that carries a Charge: once per
// response, schema-invalid and no-choices answers included, never for a request that got no answer,
// and before the answer is validated. It runs synchronously on the calling goroutine, so an
// implementation bounds its own work and detaches from the caller's cancellation (a cancelled call
// has still spent its tokens). A panic in it is recovered and logged.
type Recorder interface {
	RecordUsage(ctx context.Context, u RecordedUsage)
}

// Chunk is the position of a call among the chunks of one review.
type Chunk struct{ Index, Total int }

// AttemptUsage is the usage of one response (one attempt) reported through Request.OnUsage.
type AttemptUsage struct {
	TokensIn, TokensOut int
	// CostUSD is the provider-reported cost, else the price book's estimate; nil = unpriced.
	CostUSD *float64
}

// Result is a successful structured completion.
type Result struct {
	// Data is the validated answer as JSON.
	Data json.RawMessage
	// Model is the requested model.
	Model string
	// TokensIn and TokensOut are summed over all attempts.
	TokensIn, TokensOut int
	// CostUSD is nil when any attempt's cost is unknown.
	CostUSD *float64
	// Raw is the last raw output.
	Raw      string
	Attempts int
}

// Attempt is what an Adapter sends in one request.
type Attempt struct {
	Model    string
	Messages []Message
	// SchemaName and Schema (strict form) describe the required answer.
	SchemaName  string
	Schema      json.RawMessage
	Temperature *float64
	MaxTokens   int
	// SessionID is Request.SessionID; only OpenRouter sends it.
	SessionID string
	// Mode is the structured-output mode of a custom provider (CustomCall.Mode); empty for the
	// built-in providers, whose adapters ignore it.
	Mode custom.Mode
}

// Usage is the token usage a provider reported.
type Usage struct{ TokensIn, TokensOut int }

// AttemptResult is the answer of one request.
type AttemptResult struct {
	// Raw is the model's text (or the tool input as JSON text).
	Raw string
	// Usage is nil when the response carried none; the client estimates it.
	Usage *Usage
	// CostUSD is the provider-reported cost of this attempt (OpenRouter usage.cost, mock), or nil.
	CostUSD *float64
	// Answered is set on the result that accompanies an error when the provider did answer (a
	// 200 without choices): its usage, or an estimate, is still counted and reported.
	Answered bool
}

// Policy is a provider's transport defaults (spec FR210, FR214).
type Policy struct {
	// RequestTimeout bounds one structured HTTP request when the caller sets none.
	RequestTimeout time.Duration
	// Retries is how often a transient failure is retried.
	Retries int
	// ListTimeout and ListRetries are the same for model listing.
	ListTimeout time.Duration
	ListRetries int
}

// Adapter is one provider. Each method performs exactly one HTTP request (or none), with no
// retry and no timeout of its own beyond ctx: the Client owns both. Errors must not contain
// the key; a non-2xx response is an error with a StatusCode() int method so that retries work.
type Adapter interface {
	// RequiresKey reports whether Complete and ListModels need an API key.
	RequiresKey() bool
	Policy() Policy
	Complete(ctx context.Context, key string, a Attempt) (AttemptResult, error)
	ListModels(ctx context.Context, key string) ([]ModelInfo, error)
}

// KeyReader reads a secret by name; it returns "" when unset. *secrets.Store implements it and
// reads its file on every call.
type KeyReader interface{ Get(name string) string }

// Pricer prices tokens; it returns nil for an unknown model. *PriceBook implements it.
type Pricer interface {
	Estimate(model string, tokensIn, tokensOut int) *float64
}

// ErrCustomNotConfigured is returned by CustomResolver.Resolve for a well-formed custom provider
// id that is not defined in the workspace.
var ErrCustomNotConfigured = errors.New("custom provider is not configured")

// CustomCall is everything a call through one custom provider needs, resolved per call from the
// provider's stored definition and secrets.
type CustomCall struct {
	// Adapter performs the request; it already carries the base URL, auth, extra headers and the
	// outbound policy, so it is not shared between providers.
	Adapter Adapter
	// Name is the provider's display name, for error texts.
	Name string
	// Protocol is the provider's wire protocol; the Client uses it only to word the FR33 hint
	// (the Anthropic protocol has no JSON-object mode). Empty is read as the Anthropic wording,
	// which is valid for every protocol.
	Protocol custom.Protocol
	// Host is the host of the provider's base URL; the Client logs it with every request (NFR6).
	Host string
	// Mode is the provider's structured-output mode; the Client copies it to Attempt.Mode.
	Mode custom.Mode
	// Price prices the provider's manual models; nil = every model unpriced. The built-in and
	// live price books are never consulted for a custom provider.
	Price Pricer
	// Secrets are the key and the extra-header values; the Client redacts them from every error
	// text and log line of the call.
	Secrets []string
}

// CustomResolver resolves a custom provider id of a workspace to a CustomCall. It returns
// ErrCustomNotConfigured (possibly wrapped) when the id is not defined there.
type CustomResolver interface {
	Resolve(ctx context.Context, workspaceID string, p Provider) (CustomCall, error)
}

// Options configures a Client.
type Options struct {
	// Keys and Adapters are required. A provider without an adapter cannot be called.
	Keys     KeyReader
	Adapters map[Provider]Adapter
	// Logger receives the prompt_assembled line and warnings; nil discards.
	Logger *slog.Logger
	// Prices overrides the price book. Nil = a PriceBook fed by the OpenRouter listing, owned by
	// the Client and stopped by Close.
	Prices Pricer
	// PriceTTL is the live-price lifetime of the default price book (default 6 h).
	PriceTTL time.Duration
	// TotalBudget bounds one structured call (default DefaultTotalBudget, 720 s).
	TotalBudget time.Duration
	// Retry tunes the backoff (BaseDelay, MaxDelay, Jitter, OnRetry); the retry count always
	// comes from the adapter's Policy.
	Retry retry.Options
	// Now is the clock of the default price book (default time.Now).
	Now func() time.Time
	// Recorder receives the usage of every answered attempt (see Recorder). Nil = off; pass a
	// true nil interface, never a typed-nil pointer.
	Recorder Recorder
	// Custom resolves custom provider ids per call. Nil = custom providers are not available.
	Custom CustomResolver
	// PriceReportedUsageOnly makes a built-in attempt whose response carried no usage (the tokens
	// are the client's estimate) unpriced, unless the provider reported a cost: estimated tokens
	// are never priced (spec D30, SCR7). Off = the price book prices estimated usage, as the
	// studio's ledger does.
	PriceReportedUsageOnly bool
	// ExtraRedactions lists secrets Redact masks besides the built-in keys: the custom providers'
	// keys and header values (NFR1), read at each Redact call so a rotation applies at once. Nil =
	// none. Only values of at least minRedactLen bytes are used: Redact runs over unrelated text,
	// where a short header value ("1", "v2") would mask ordinary characters.
	ExtraRedactions func() []string
}

// Client runs structured calls and model listings. It is safe for concurrent use.
type Client struct {
	keys     KeyReader
	adapters map[Provider]Adapter
	log      *slog.Logger
	prices   Pricer
	book     *PriceBook // owned default price book, or nil
	budget   time.Duration
	backoff  retry.Options
	recorder Recorder
	custom   CustomResolver
	// extraRedactions is Options.ExtraRedactions.
	extraRedactions func() []string
	// reportedOnly is Options.PriceReportedUsageOnly.
	reportedOnly bool
}

// New builds a Client. It panics when Keys is nil: a wiring error caught at startup.
func New(opts Options) *Client {
	if opts.Keys == nil {
		panic("llm.New: Keys is required")
	}
	c := &Client{
		keys:     opts.Keys,
		adapters: opts.Adapters,
		log:      opts.Logger,
		prices:   opts.Prices,
		budget:   opts.TotalBudget,
		backoff:  opts.Retry,
		recorder: opts.Recorder,
		custom:   opts.Custom,

		extraRedactions: opts.ExtraRedactions,
		reportedOnly:    opts.PriceReportedUsageOnly,
	}
	if c.log == nil {
		c.log = slog.New(slog.DiscardHandler)
	}
	if c.budget <= 0 {
		c.budget = DefaultTotalBudget
	}
	if c.prices == nil {
		c.book = NewPriceBook(PriceBookOptions{List: c.listLivePrices, TTL: opts.PriceTTL, Now: opts.Now})
		c.prices = c.book
	}
	return c
}

// Close stops the background price refresh and waits for it. It is idempotent.
func (c *Client) Close() {
	if c.book != nil {
		c.book.Close()
	}
}

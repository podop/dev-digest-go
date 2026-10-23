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
	"log/slog"
	"time"

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
	// DefaultTotalBudget bounds one structured call: every request, retry and re-prompt.
	DefaultTotalBudget = 360 * time.Second
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
	// TotalBudget bounds one structured call (default 360 s).
	TotalBudget time.Duration
	// Retry tunes the backoff (BaseDelay, MaxDelay, Jitter, OnRetry); the retry count always
	// comes from the adapter's Policy.
	Retry retry.Options
	// Now is the clock of the default price book (default time.Now).
	Now func() time.Time
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

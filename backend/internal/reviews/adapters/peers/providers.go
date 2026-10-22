package peers

import (
	"context"

	"dev-digest-go/backend/internal/llm"
	"dev-digest-go/backend/internal/reviewer/adapters/llmcall"
	reviewerapp "dev-digest-go/backend/internal/reviewer/app"
	reviewer "dev-digest-go/backend/internal/reviewer/domain"
	reviewsapp "dev-digest-go/backend/internal/reviews/app"
)

// LLMClient is the part of *llm.Client the providers adapter uses.
type LLMClient interface {
	llmcall.Structured
	// Ready reports whether a call to the provider can be made now ("<KEY> is not configured").
	Ready(p llm.Provider) error
	// Redact scrubs provider keys out of a text.
	Redact(s string) string
}

// Providers implements reviews/app.Providers over the LLM client.
type Providers struct{ c LLMClient }

var _ reviewsapp.Providers = (*Providers)(nil)

// NewProviders wraps the LLM client (*llm.Client).
func NewProviders(c LLMClient) *Providers { return &Providers{c: c} }

// Resolve checks that the provider can be called (its key is stored, or the mock override is on)
// and returns the engine's LLM port bound to the run. It makes no call.
func (p *Providers) Resolve(_ context.Context, b reviewsapp.LLMBinding) (reviewerapp.LLM, error) {
	provider := llm.Provider(b.Provider)
	if err := p.c.Ready(provider); err != nil {
		return nil, err
	}
	cfg := llmcall.Config{
		Client: p.c, Provider: provider, Model: b.Model, RunID: b.RunID, SessionID: b.SessionID,
	}
	if b.OnUsage != nil {
		on := b.OnUsage
		cfg.OnUsage = func(u reviewer.Usage) { on(u) }
	}
	return llmcall.New(cfg), nil
}

// Redact scrubs provider keys out of a text.
func (p *Providers) Redact(s string) string { return p.c.Redact(s) }

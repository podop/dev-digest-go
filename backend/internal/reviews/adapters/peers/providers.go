package peers

import (
	"context"

	"dev-digest-go/backend/internal/llm"
	reviewerapp "dev-digest-go/backend/internal/reviewer/app"
	reviewer "dev-digest-go/backend/internal/reviewer/domain"
	reviewsapp "dev-digest-go/backend/internal/reviews/app"
)

// LLMClient is the part of *llm.Client the providers adapter uses.
type LLMClient interface {
	// Ready reports whether a call to the provider can be made now ("<KEY> is not configured").
	Ready(p llm.Provider) error
	// Redact scrubs provider keys out of a text.
	Redact(s string) string
}

// CallerConfig is the identity of one run's LLM calls.
type CallerConfig struct {
	Provider  llm.Provider
	Model     string
	RunID     string
	SessionID string
	// OnUsage receives the usage of every response; nil = none.
	OnUsage func(reviewer.Usage)
	// Charge labels the run's LLM calls for the spend ledger: the run's workspace, kind review_run
	// and the run id. The factory copies it into the run's llmcall.Config.
	Charge *llm.Charge
}

// CallerFactory builds the engine's LLM port for one run. cmd/server supplies it (it binds
// reviewer/adapters/llmcall to the LLM client), so this adapter does not import another adapter.
type CallerFactory func(CallerConfig) reviewerapp.LLM

// Providers implements reviews/app.Providers over the LLM client.
type Providers struct {
	c         LLMClient
	newCaller CallerFactory
}

var _ reviewsapp.Providers = (*Providers)(nil)

// NewProviders wraps the LLM client (*llm.Client) and the factory of the run's LLM port. A nil
// factory is a wiring error and panics.
func NewProviders(c LLMClient, newCaller CallerFactory) *Providers {
	if c == nil || newCaller == nil {
		panic("reviews/adapters/peers: NewProviders needs an LLM client and a caller factory")
	}
	return &Providers{c: c, newCaller: newCaller}
}

// Resolve checks that the provider can be called (its key is stored, or the mock override is on)
// and returns the engine's LLM port bound to the run. It makes no call.
func (p *Providers) Resolve(_ context.Context, b reviewsapp.LLMBinding) (reviewerapp.LLM, error) {
	provider := llm.Provider(b.Provider)
	if err := p.c.Ready(provider); err != nil {
		return nil, err
	}
	cfg := CallerConfig{
		Provider: provider, Model: b.Model, RunID: b.RunID, SessionID: b.SessionID,
		Charge: &llm.Charge{WorkspaceID: b.WorkspaceID, Kind: llm.KindReviewRun, RefID: b.RunID},
	}
	if b.OnUsage != nil {
		on := b.OnUsage
		cfg.OnUsage = func(u reviewer.Usage) { on(u) }
	}
	return p.newCaller(cfg), nil
}

// Redact scrubs provider keys out of a text.
func (p *Providers) Redact(s string) string { return p.c.Redact(s) }

// Package peers binds the evals context to the other parts of the program that are not its own
// adapters. It holds the provider resolver of the eval executor: it checks that a provider can
// be called and hands the review engine an LLM port bound to the case.
package peers

import (
	"context"

	"dev-digest-go/backend/internal/evals/app/executor"
	"dev-digest-go/backend/internal/llm"
	reviewerapp "dev-digest-go/backend/internal/reviewer/app"
	reviewer "dev-digest-go/backend/internal/reviewer/domain"
)

// LLMClient is the part of *llm.Client the providers adapter uses.
type LLMClient interface {
	// ReadyIn reports whether a call to the provider can be made now for the workspace
	// ("<KEY> is not configured", "Custom provider custom:<slug> is not configured").
	ReadyIn(ctx context.Context, workspaceID string, p llm.Provider) error
	// Redact scrubs provider keys out of a text.
	Redact(s string) string
}

// CallerConfig is the identity of one case's LLM calls. A case has no session id.
type CallerConfig struct {
	Provider llm.Provider
	Model    string
	// RunID is the correlation id of the prompt log line (the suite run's id).
	RunID string
	// Charge labels the case's calls for the spend ledger: the workspace, kind eval_case and the
	// eval case id. The factory sets it as llmcall's Config.Charge.
	Charge *llm.Charge
	// OnUsage receives the usage of every response; nil = none.
	OnUsage func(reviewer.Usage)
}

// CallerFactory builds the engine's LLM port for one case. cmd/server supplies it (it binds
// reviewer/adapters/llmcall to the LLM client), so this adapter imports no other adapter.
type CallerFactory func(CallerConfig) reviewerapp.LLM

// Providers implements executor.Providers over the LLM client.
type Providers struct {
	c         LLMClient
	newCaller CallerFactory
}

var _ executor.Providers = (*Providers)(nil)

// NewProviders wraps the LLM client (*llm.Client) and the factory of the case's LLM port. A nil
// client or factory is a wiring error and panics.
func NewProviders(c LLMClient, newCaller CallerFactory) *Providers {
	if c == nil || newCaller == nil {
		panic("evals/adapters/peers: NewProviders needs an LLM client and a caller factory")
	}
	return &Providers{c: c, newCaller: newCaller}
}

// Resolve checks that the provider can be called (its key is stored, or the mock override is on,
// or a custom provider is defined in the workspace)
// and returns the engine's LLM port bound to the case. It makes no call.
func (p *Providers) Resolve(ctx context.Context, b executor.LLMBinding) (reviewerapp.LLM, error) {
	provider := llm.Provider(b.Provider)
	if err := p.c.ReadyIn(ctx, b.WorkspaceID, provider); err != nil {
		return nil, err
	}
	cfg := CallerConfig{
		Provider: provider, Model: b.Model, RunID: b.RunID,
		Charge: &llm.Charge{WorkspaceID: b.WorkspaceID, Kind: llm.KindEvalCase, RefID: b.CaseID},
	}
	if b.OnUsage != nil {
		on := b.OnUsage
		cfg.OnUsage = func(u reviewer.Usage) { on(u) }
	}
	return p.newCaller(cfg), nil
}

// Redact scrubs provider keys out of a text (stored error texts pass through it first).
func (p *Providers) Redact(s string) string { return p.c.Redact(s) }

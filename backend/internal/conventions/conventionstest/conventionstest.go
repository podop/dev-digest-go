// Package conventionstest holds test doubles for the conventions context: a scripted LLM
// provider and a real llm.Client built over it. It is for tests only.
package conventionstest

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"testing"
	"time"

	"dev-digest-go/backend/internal/llm"
	"dev-digest-go/backend/internal/platform/retry"
)

// Keys is an in-memory secrets store: name to value.
type Keys map[string]string

// Get implements llm.KeyReader.
func (k Keys) Get(name string) string { return k[name] }

// Provider is a scripted llm.Adapter that records every attempt it receives.
type Provider struct {
	// Reply answers attempt n (1-based). Nil answers every attempt with an empty candidate list.
	Reply func(ctx context.Context, n int, a llm.Attempt) (llm.AttemptResult, error)

	mu    sync.Mutex
	calls []llm.Attempt
}

var _ llm.Adapter = (*Provider)(nil)

// RequiresKey is true: a scan needs the provider's key.
func (p *Provider) RequiresKey() bool { return true }

// Policy has no retries, so one failed request is one attempt.
func (p *Provider) Policy() llm.Policy {
	return llm.Policy{RequestTimeout: 120 * time.Second, ListTimeout: 30 * time.Second}
}

// Complete records the attempt and returns the scripted answer.
func (p *Provider) Complete(ctx context.Context, _ string, a llm.Attempt) (llm.AttemptResult, error) {
	p.mu.Lock()
	p.calls = append(p.calls, a)
	n := len(p.calls)
	p.mu.Unlock()
	if p.Reply == nil {
		return Answer(`{"candidates":[]}`), nil
	}
	return p.Reply(ctx, n, a)
}

// ListModels lists nothing.
func (p *Provider) ListModels(context.Context, string) ([]llm.ModelInfo, error) { return nil, nil }

// Calls returns the attempts received so far.
func (p *Provider) Calls() []llm.Attempt {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]llm.Attempt(nil), p.calls...)
}

// Answer is a successful attempt with the given raw model output and fixed token usage.
func Answer(raw string) llm.AttemptResult {
	return llm.AttemptResult{Raw: raw, Usage: &llm.Usage{TokensIn: 1000, TokensOut: 200}}
}

// Candidates renders a ConventionExtraction answer from candidate JSON objects.
func Candidates(candidates ...json.RawMessage) string {
	b, err := json.Marshal(map[string]any{"candidates": candidates})
	if err != nil {
		panic(err)
	}
	return string(b)
}

// NewClient builds an llm.Client whose three providers are p, with static prices, a 1 ms
// backoff and logs written to log (nil discards them).
func NewClient(t testing.TB, p llm.Adapter, keys llm.KeyReader, log *slog.Logger) *llm.Client {
	t.Helper()
	c := llm.New(llm.Options{
		Keys:     keys,
		Adapters: map[llm.Provider]llm.Adapter{llm.OpenAI: p, llm.Anthropic: p, llm.OpenRouter: p},
		Logger:   log,
		Prices:   llm.NewPriceBook(llm.PriceBookOptions{}),
		Retry: retry.Options{
			BaseDelay: time.Millisecond, MaxDelay: 10 * time.Millisecond,
			Jitter: func(time.Duration) time.Duration { return 0 },
		},
	})
	t.Cleanup(c.Close)
	return c
}

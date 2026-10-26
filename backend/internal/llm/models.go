package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"dev-digest-go/backend/internal/platform/apperr"
	"dev-digest-go/backend/internal/platform/retry"
)

// ModelPricing is a model's price in USD per 1,000,000 tokens.
type ModelPricing struct {
	PromptPerM     float64 `json:"promptPerM"`
	CompletionPerM float64 `json:"completionPerM"`
}

// ModelInfo is one entry of a provider's model list. Its JSON form follows the vendored
// ModelInfo contract and differs per provider (spec 7.2): absent keys are not sent, and the
// OpenRouter entry carries explicit nulls.
type ModelInfo struct {
	ID       string
	Provider Provider
	// Created is the OpenAI creation time (unix seconds).
	Created *int64
	// Label is the Anthropic display name or the OpenRouter name.
	Label *string
	// Pricing and ContextLength are OpenRouter's; nil = unknown.
	Pricing       *ModelPricing
	ContextLength *int
	// Mock marks the synthetic entry of the mock provider ({id, provider, label} only).
	Mock bool
}

// MarshalJSON writes the provider-specific wire shape.
func (m ModelInfo) MarshalJSON() ([]byte, error) {
	type head struct {
		ID       string   `json:"id"`
		Provider Provider `json:"provider"`
	}
	switch {
	case m.Provider.IsCustom():
		// A custom provider's entry carries what its manual model says: label and pricing when set.
		return json.Marshal(struct {
			head
			Label   *string       `json:"label,omitempty"`
			Pricing *ModelPricing `json:"pricing,omitempty"`
		}{head{m.ID, m.Provider}, m.Label, m.Pricing})
	case m.Mock || m.Provider == Anthropic:
		return json.Marshal(struct {
			head
			Label *string `json:"label,omitempty"`
		}{head{m.ID, m.Provider}, m.Label})
	case m.Provider == OpenRouter:
		return json.Marshal(struct {
			head
			Label         *string       `json:"label"`
			Pricing       *ModelPricing `json:"pricing"`
			ContextLength *int          `json:"contextLength"`
		}{head{m.ID, m.Provider}, m.Label, m.Pricing, m.ContextLength})
	default:
		return json.Marshal(struct {
			head
			Created *int64 `json:"created,omitempty"`
		}{head{m.ID, m.Provider}, m.Created})
	}
}

// ListModels lists the built-in provider's models: the key is read now, the request gets the
// provider's listing timeout and retries (spec FR214). A missing key is a config error. A custom
// provider belongs to a workspace and is listed by ListModelsIn.
func (c *Client) ListModels(ctx context.Context, p Provider) ([]ModelInfo, error) {
	if p.IsCustom() {
		return nil, customNotConfigured(p)
	}
	ad, err := c.adapter(p)
	if err != nil {
		return nil, err
	}
	key, err := c.keyFor(p, ad)
	if err != nil {
		return nil, err
	}
	return c.listModels(ctx, p, ad, key, nil)
}

// ListModelsIn is ListModels for a provider of workspace ws. A custom provider is resolved first
// (an undefined one is the FR9 config error) and listed through its own adapter with the OpenAI
// limits: 30 s per request, 3 retries (NFR4). The result is the live list only; merging a
// provider's manual models is the caller's job.
func (c *Client) ListModelsIn(ctx context.Context, ws string, p Provider) ([]ModelInfo, error) {
	if !p.IsCustom() {
		return c.ListModels(ctx, p)
	}
	call, err := c.resolveCustom(ctx, ws, p)
	if err != nil {
		return nil, err
	}
	return c.listModels(ctx, p, call.Adapter, "", &call)
}

func (c *Client) listModels(ctx context.Context, p Provider, ad Adapter, key string, cc *CustomCall) ([]ModelInfo, error) {
	pol := ad.Policy()
	opts := c.backoff
	opts.Retries = pol.ListRetries
	name := displayName(p, cc)
	var secrets []string
	if cc != nil {
		secrets = cc.Secrets
	}
	trace := newTrace(cc, p, "list_models")
	models, err := retry.Value(ctx, opts, func(ctx context.Context) ([]ModelInfo, error) {
		rctx := ctx
		if pol.ListTimeout > 0 {
			var cancel context.CancelFunc
			rctx, cancel = context.WithTimeout(ctx, pol.ListTimeout)
			defer cancel()
		}
		start := time.Now()
		out, err := ad.ListModels(rctx, key)
		if err != nil && pol.ListTimeout > 0 && rctx.Err() == context.DeadlineExceeded {
			// The listing's own deadline, not the caller's: a plain, non-retryable failure.
			err = fmt.Errorf("%s model list timed out after %s", name, pol.ListTimeout.Round(time.Millisecond))
			out = nil
		}
		trace.log(ctx, c.log, err, time.Since(start))
		return out, err
	})
	if err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("%s model list was cancelled: %w", name, ctx.Err())
		}
		return nil, apperr.ExternalService(c.scrub(err.Error(), key, secrets))
	}
	return models, nil
}

// listLivePrices feeds the default price book: the OpenRouter listing with the stored key.
func (c *Client) listLivePrices(ctx context.Context) ([]ModelInfo, error) {
	return c.ListModels(ctx, OpenRouter)
}

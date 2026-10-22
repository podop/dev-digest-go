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

// ListModels lists the provider's models: the key is read now, the request gets the provider's
// listing timeout and retries (spec FR214). A missing key is a config error.
func (c *Client) ListModels(ctx context.Context, p Provider) ([]ModelInfo, error) {
	ad, err := c.adapter(p)
	if err != nil {
		return nil, err
	}
	key, err := c.keyFor(p, ad)
	if err != nil {
		return nil, err
	}
	pol := ad.Policy()
	opts := c.backoff
	opts.Retries = pol.ListRetries
	models, err := retry.Value(ctx, opts, func(ctx context.Context) ([]ModelInfo, error) {
		if pol.ListTimeout > 0 {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, pol.ListTimeout)
			defer cancel()
		}
		out, err := ad.ListModels(ctx, key)
		if err != nil && pol.ListTimeout > 0 && ctx.Err() == context.DeadlineExceeded {
			// The listing's own deadline, not the caller's: a plain, non-retryable failure.
			return nil, fmt.Errorf("%s model list timed out after %s", p.DisplayName(), pol.ListTimeout.Round(time.Millisecond))
		}
		return out, err
	})
	if err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("%s model list was cancelled: %w", p.DisplayName(), ctx.Err())
		}
		return nil, apperr.ExternalService(c.scrub(err.Error(), key))
	}
	return models, nil
}

// listLivePrices feeds the default price book: the OpenRouter listing with the stored key.
func (c *Client) listLivePrices(ctx context.Context) ([]ModelInfo, error) {
	return c.ListModels(ctx, OpenRouter)
}

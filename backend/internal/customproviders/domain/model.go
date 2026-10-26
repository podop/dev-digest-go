// Package domain holds the rules of the custom model providers context (spec FR1, FR4, FR5, FR10,
// FR12): the stored definition of a provider, the validation of a create or update request, the
// merge of extra-header values and the manual-model price. It imports the standard library and
// llm/custom (the pure rules shared with the CI runner) only.
package domain

import (
	"errors"
	"math"
	"net/url"
	"strings"
	"time"

	"dev-digest-go/backend/internal/llm/custom"
)

// Limits of a workspace's custom providers (spec FR1).
const (
	// MaxProviders is the most custom providers one workspace can define.
	MaxProviders = 20
	// MaxModels is the most manual models one provider can have.
	MaxModels = 200
	// MaxNameLen, MaxModelIDLen and MaxLabelLen are lengths in UTF-16 code units, which is how the
	// UI counts.
	MaxNameLen    = 80
	MaxModelIDLen = 200
	MaxLabelLen   = 200
	// MaxKeyLen is the longest API key.
	MaxKeyLen = 4096
	// MaxPrice is the highest manual price, USD per 1M tokens; PriceDecimals the most decimals.
	MaxPrice      = 10000.0
	PriceDecimals = 6
)

// Errors a Store returns; the use cases map them to application errors.
var (
	// ErrNotFound means the workspace defines no provider with that id.
	ErrNotFound = errors.New("custom provider not found")
	// ErrExists means a provider with that id exists already (unique violation).
	ErrExists = errors.New("custom provider already exists")
	// ErrLimit means the workspace has MaxProviders providers already.
	ErrLimit = errors.New("custom provider limit reached")
)

// Pricing is the price of a model in USD per 1,000,000 tokens.
type Pricing struct {
	PromptPerM     float64
	CompletionPerM float64
}

// Model is a manual model of a provider. Label and Pricing are nil when not set.
type Model struct {
	ID      string
	Label   *string
	Pricing *Pricing
}

// Provider is the stored definition of one custom provider. The key and the extra-header values
// are not part of it: they live in the secrets store.
type Provider struct {
	// ID is the public id, `custom:<slug>` (the database keeps the bare slug).
	ID       string
	Name     string
	Protocol custom.Protocol
	// BaseURL is the normalized API root (no trailing slash).
	BaseURL  string
	AuthMode custom.AuthMode
	// AuthHeader is the header that carries the key in header mode; "" for bearer.
	AuthHeader       string
	StructuredOutput custom.Mode
	// ExtraHeaderNames are the names of the extra headers in order (at most custom.MaxHeaders).
	ExtraHeaderNames []string
	// Models are the manual models in the order the user gave them.
	Models    []Model
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Slug returns the part of the id after `custom:`.
func (p Provider) Slug() string {
	slug, _ := custom.ParseID(p.ID)
	return slug
}

// PlainHTTP reports whether the key travels unencrypted: http to a host that is not loopback
// (spec FR3, D8). It is derived from the base URL, never stored.
func (p Provider) PlainHTTP() bool {
	_, plain, msg := custom.CheckBaseURL(p.BaseURL)
	return msg == "" && plain
}

// Host is the host (with the port, if any) of the base URL, for log lines; "" when the URL does
// not parse.
func (p Provider) Host() string {
	u, err := url.Parse(p.BaseURL)
	if err != nil {
		return ""
	}
	return u.Host
}

// Estimate prices a call of a manual model: tokensIn x promptPerM / 1e6 + tokensOut x
// completionPerM / 1e6 (spec FR12). It returns nil when the model has no manual price, a count is
// negative or the result is not finite: the built-in price books are never consulted. It makes a
// Provider an llm.Pricer.
func (p Provider) Estimate(model string, tokensIn, tokensOut int) *float64 {
	if tokensIn < 0 || tokensOut < 0 {
		return nil
	}
	for _, m := range p.Models {
		if m.ID != model {
			continue
		}
		if m.Pricing == nil {
			return nil
		}
		cost := float64(tokensIn)*m.Pricing.PromptPerM/1e6 + float64(tokensOut)*m.Pricing.CompletionPerM/1e6
		if math.IsNaN(cost) || math.IsInf(cost, 0) || cost < 0 {
			return nil
		}
		return &cost
	}
	return nil
}

// ModelByID returns the manual model with that id.
func (p Provider) ModelByID(id string) (Model, bool) {
	for _, m := range p.Models {
		if m.ID == id {
			return m, true
		}
	}
	return Model{}, false
}

// HeaderValue is a stored extra header: the name as the user wrote it and its secret value.
type HeaderValue struct {
	Name  string
	Value string
}

// FindHeader returns the index of the header with that name, compared case-insensitively, or -1.
func FindHeader(headers []HeaderValue, name string) int {
	for i, h := range headers {
		if strings.EqualFold(h.Name, name) {
			return i
		}
	}
	return -1
}

// Usage says what uses a provider (spec FR6).
type Usage struct {
	// Agents are the agents, enabled or not, whose provider it is.
	Agents []AgentRef
	// FeatureModels are the ids of the feature_models entries that name it.
	FeatureModels []string
	// DefaultModel is true when default_model names it.
	DefaultModel bool
}

// AgentRef identifies an agent that uses a provider.
type AgentRef struct {
	ID   string
	Name string
}

// Empty reports whether nothing uses the provider.
func (u Usage) Empty() bool {
	return len(u.Agents) == 0 && len(u.FeatureModels) == 0 && !u.DefaultModel
}

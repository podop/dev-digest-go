// Package openaicompat implements llm.Adapter for the OpenAI chat-completions API and for
// OpenRouter, which speaks the same protocol with a few extensions (spec 7.2, FR206, FR208,
// FR214). One adapter value serves one provider.
package openaicompat

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"dev-digest-go/backend/internal/llm"
	"dev-digest-go/backend/internal/llm/adapters/internal/wire"
)

// Default base URLs.
const (
	DefaultOpenAIBaseURL     = "https://api.openai.com/v1"
	DefaultOpenRouterBaseURL = "https://openrouter.ai/api/v1"
)

// Transport defaults (spec FR210, FR214, decisions D206, D207).
const (
	openAIRequestTimeout     = 120 * time.Second
	openRouterRequestTimeout = 180 * time.Second
	openAIRetries            = 3
	openRouterRetries        = 2
	openAIListTimeout        = 30 * time.Second
	openRouterListTimeout    = 15 * time.Second
)

// Options configures an Adapter. Zero fields take the defaults.
type Options struct {
	// BaseURL is the API root including /v1 (tests point it at an httptest server).
	BaseURL string
	// HTTPClient performs the requests. It must not set Client.Timeout: deadlines come from the
	// per-request context.
	HTTPClient *http.Client
}

// Adapter talks to one OpenAI-compatible endpoint.
type Adapter struct {
	provider llm.Provider
	name     string
	base     string
	hc       *http.Client
}

var _ llm.Adapter = (*Adapter)(nil)

// NewOpenAI returns the OpenAI adapter.
func NewOpenAI(opts Options) *Adapter { return newAdapter(llm.OpenAI, DefaultOpenAIBaseURL, opts) }

// NewOpenRouter returns the OpenRouter adapter.
func NewOpenRouter(opts Options) *Adapter {
	return newAdapter(llm.OpenRouter, DefaultOpenRouterBaseURL, opts)
}

func newAdapter(p llm.Provider, def string, opts Options) *Adapter {
	a := &Adapter{provider: p, name: p.DisplayName(), base: strings.TrimRight(opts.BaseURL, "/"), hc: opts.HTTPClient}
	if a.base == "" {
		a.base = def
	}
	if a.hc == nil {
		a.hc = &http.Client{}
	}
	return a
}

// RequiresKey is always true.
func (a *Adapter) RequiresKey() bool { return true }

// Policy returns the provider's timeouts and retry counts.
func (a *Adapter) Policy() llm.Policy {
	if a.provider == llm.OpenRouter {
		return llm.Policy{RequestTimeout: openRouterRequestTimeout, Retries: openRouterRetries, ListTimeout: openRouterListTimeout}
	}
	return llm.Policy{RequestTimeout: openAIRequestTimeout, Retries: openAIRetries, ListTimeout: openAIListTimeout, ListRetries: openAIRetries}
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatRequest struct {
	Model               string         `json:"model"`
	Messages            []chatMessage  `json:"messages"`
	ResponseFormat      responseFormat `json:"response_format"`
	Temperature         *float64       `json:"temperature,omitempty"`
	MaxTokens           int            `json:"max_tokens,omitempty"`
	MaxCompletionTokens int            `json:"max_completion_tokens,omitempty"`
	Usage               *usageRequest  `json:"usage,omitempty"`
}

type usageRequest struct {
	Include bool `json:"include"`
}

type responseFormat struct {
	Type       string     `json:"type"`
	JSONSchema jsonSchema `json:"json_schema"`
}

type jsonSchema struct {
	Name   string `json:"name"`
	Schema any    `json:"schema"`
	Strict bool   `json:"strict"`
}

type chatResponse struct {
	Choices []struct {
		Message struct {
			Content *string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		Cost             any `json:"cost"`
	} `json:"usage"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

var (
	openAIReasoning = regexp.MustCompile(`^(gpt-5|o1|o3|o4)`)
	noTemperature   = []*regexp.Regexp{
		regexp.MustCompile(`^o\d(?:$|[-.])`),
		regexp.MustCompile(`^gpt-5(?:$|[-.])`),
		regexp.MustCompile(`^deepseek-(?:reasoner|r1)(?:$|[-:.])`),
	}
)

// supportsTemperature matches the model name after its last "/", lowercased (OpenRouter slugs).
func supportsTemperature(model string) bool {
	name := strings.ToLower(model[strings.LastIndex(model, "/")+1:])
	return !slices.ContainsFunc(noTemperature, func(re *regexp.Regexp) bool { return re.MatchString(name) })
}

func (a *Adapter) buildRequest(at llm.Attempt) chatRequest {
	msgs := make([]chatMessage, len(at.Messages))
	for i, m := range at.Messages {
		msgs[i] = chatMessage{Role: string(m.Role), Content: m.Content}
	}
	req := chatRequest{
		Model:    at.Model,
		Messages: msgs,
		ResponseFormat: responseFormat{Type: "json_schema", JSONSchema: jsonSchema{
			Name: at.SchemaName, Schema: at.Schema, Strict: true,
		}},
	}
	if a.provider == llm.OpenRouter {
		req.Usage = &usageRequest{Include: true}
		if at.Temperature != nil && supportsTemperature(at.Model) {
			req.Temperature = at.Temperature
		}
		req.MaxTokens = at.MaxTokens
		return req
	}
	if openAIReasoning.MatchString(at.Model) {
		req.MaxCompletionTokens = at.MaxTokens
		return req
	}
	temp := 0.0
	if at.Temperature != nil {
		temp = *at.Temperature
	}
	req.Temperature = &temp
	req.MaxTokens = at.MaxTokens
	return req
}

// Complete sends one chat completion with a strict JSON-schema response format.
func (a *Adapter) Complete(ctx context.Context, key string, at llm.Attempt) (llm.AttemptResult, error) {
	var resp chatResponse
	err := wire.Do(ctx, a.hc, wire.Call{
		Provider: a.name, Op: "chat completions", Method: http.MethodPost, URL: a.base + "/chat/completions",
		Headers: map[string]string{"Authorization": "Bearer " + key},
		Body:    a.buildRequest(at), Key: key, ErrorMessage: a.provider != llm.OpenRouter,
	}, &resp)
	if err != nil {
		return llm.AttemptResult{}, err
	}
	// OpenRouter can answer 200 without choices (upstream error, moderation, free-tier limit).
	if a.provider == llm.OpenRouter && len(resp.Choices) == 0 {
		msg := ""
		if resp.Error != nil && resp.Error.Message != "" {
			msg = ": " + resp.Error.Message
		}
		return llm.AttemptResult{}, fmt.Errorf("OpenRouter returned no choices for %s%s", at.SchemaName, msg)
	}
	out := llm.AttemptResult{}
	if len(resp.Choices) > 0 && resp.Choices[0].Message.Content != nil {
		out.Raw = *resp.Choices[0].Message.Content
	}
	if u := resp.Usage; u != nil {
		out.Usage = &llm.Usage{TokensIn: u.PromptTokens, TokensOut: u.CompletionTokens}
		if cost, ok := u.Cost.(float64); ok && a.provider == llm.OpenRouter {
			out.CostUSD = &cost
		}
	}
	return out, nil
}

type modelList struct {
	Data []struct {
		ID            string  `json:"id"`
		Created       *int64  `json:"created"`
		Name          *string `json:"name"`
		ContextLength *int    `json:"context_length"`
		Pricing       *struct {
			Prompt     any `json:"prompt"`
			Completion any `json:"completion"`
		} `json:"pricing"`
	} `json:"data"`
}

// ListModels lists the models: for OpenAI the ids starting with gpt or containing o1 or o3 in
// upstream order; for OpenRouter all of them with pricing, cheapest completion first and
// unknown prices last (ties keep the upstream order).
func (a *Adapter) ListModels(ctx context.Context, key string) ([]llm.ModelInfo, error) {
	var list modelList
	err := wire.Do(ctx, a.hc, wire.Call{
		Provider: a.name, Op: "/models", Method: http.MethodGet, URL: a.base + "/models",
		Headers: map[string]string{"Authorization": "Bearer " + key}, Key: key, ErrorMessage: a.provider != llm.OpenRouter,
	}, &list)
	if err != nil {
		return nil, err
	}
	models := make([]llm.ModelInfo, 0, len(list.Data))
	for _, m := range list.Data {
		if a.provider == llm.OpenAI {
			if !strings.HasPrefix(m.ID, "gpt") && !strings.Contains(m.ID, "o1") && !strings.Contains(m.ID, "o3") {
				continue
			}
			models = append(models, llm.ModelInfo{ID: m.ID, Provider: llm.OpenAI, Created: m.Created})
			continue
		}
		info := llm.ModelInfo{ID: m.ID, Provider: llm.OpenRouter, Label: m.Name, ContextLength: m.ContextLength}
		if m.Pricing != nil {
			prompt, pok := number(m.Pricing.Prompt)
			completion, cok := number(m.Pricing.Completion)
			// A negative price is OpenRouter's marker for variable-priced router models.
			if pok && cok && prompt >= 0 && completion >= 0 {
				info.Pricing = &llm.ModelPricing{PromptPerM: prompt * 1_000_000, CompletionPerM: completion * 1_000_000}
			}
		}
		models = append(models, info)
	}
	if a.provider == llm.OpenRouter {
		sort.SliceStable(models, func(i, j int) bool { return completionPrice(models[i]) < completionPrice(models[j]) })
	}
	return models, nil
}

func completionPrice(m llm.ModelInfo) float64 {
	if m.Pricing == nil {
		return 1e308 // unknown last
	}
	return m.Pricing.CompletionPerM
}

// number reads a JSON number or numeric string (OpenRouter sends prices as strings).
func number(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(x), 64)
		return f, err == nil && !math.IsNaN(f) && !math.IsInf(f, 0)
	}
	return 0, false
}

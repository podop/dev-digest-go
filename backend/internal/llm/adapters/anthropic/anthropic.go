// Package anthropic implements llm.Adapter for the Anthropic Messages API with forced tool use
// (spec 7.2, FR207, FR214).
package anthropic

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"regexp"
	"strings"
	"time"

	"dev-digest-go/backend/internal/llm"
	"dev-digest-go/backend/internal/llm/adapters/internal/wire"
)

// DefaultBaseURL is the API root.
const DefaultBaseURL = "https://api.anthropic.com/v1"

const (
	apiVersion     = "2023-06-01"
	defaultMaxToks = 4096
	requestTimeout = 120 * time.Second
	retries        = 3
	listTimeout    = 30 * time.Second
)

// Options configures an Adapter. Zero fields take the defaults.
type Options struct {
	// BaseURL is the API root including /v1 (tests point it at an httptest server).
	BaseURL string
	// HTTPClient performs the requests. It must not set Client.Timeout: deadlines come from the
	// per-request context.
	HTTPClient *http.Client
}

// Adapter talks to the Anthropic API.
type Adapter struct {
	base string
	hc   *http.Client
}

var _ llm.Adapter = (*Adapter)(nil)

// New returns the Anthropic adapter.
func New(opts Options) *Adapter {
	a := &Adapter{base: strings.TrimRight(opts.BaseURL, "/"), hc: opts.HTTPClient}
	if a.base == "" {
		a.base = DefaultBaseURL
	}
	a.hc = withoutRedirects(a.hc)
	return a
}

// RequiresKey is always true.
func (a *Adapter) RequiresKey() bool { return true }

// Policy returns the provider's timeouts and retry counts.
func (a *Adapter) Policy() llm.Policy {
	return llm.Policy{RequestTimeout: requestTimeout, Retries: retries, ListTimeout: listTimeout, ListRetries: retries}
}

type message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
}

type toolChoice struct {
	Type string `json:"type"`
	Name string `json:"name"`
}

type messagesRequest struct {
	Model       string      `json:"model"`
	System      string      `json:"system,omitempty"`
	Messages    []message   `json:"messages"`
	MaxTokens   int         `json:"max_tokens"`
	Temperature float64     `json:"temperature"`
	Tools       []tool      `json:"tools,omitempty"`
	ToolChoice  *toolChoice `json:"tool_choice,omitempty"`
}

type messagesResponse struct {
	Content []struct {
		Type  string          `json:"type"`
		Text  string          `json:"text"`
		Input json.RawMessage `json:"input"`
	} `json:"content"`
	Usage *struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
		// Cost is read only by custom providers (a gateway's usage.cost).
		Cost json.RawMessage `json:"cost"`
	} `json:"usage"`
}

var invalidToolChars = regexp.MustCompile(`[^A-Za-z0-9_-]`)

func (a *Adapter) headers(key string) map[string]string {
	return map[string]string{"x-api-key": key, "anthropic-version": apiVersion}
}

func buildRequest(at llm.Attempt) messagesRequest {
	var system []string
	msgs := make([]message, 0, len(at.Messages))
	for _, m := range at.Messages {
		if m.Role == llm.System {
			system = append(system, m.Content)
			continue
		}
		msgs = append(msgs, message{Role: string(m.Role), Content: m.Content})
	}
	name := invalidToolChars.ReplaceAllString(at.SchemaName, "_")
	req := messagesRequest{
		Model:     at.Model,
		System:    strings.Join(system, "\n\n"),
		Messages:  msgs,
		MaxTokens: defaultMaxToks,
		Tools: []tool{{
			Name: name, Description: "Return the result as " + at.SchemaName + ".", InputSchema: at.Schema,
		}},
		ToolChoice: &toolChoice{Type: "tool", Name: name},
	}
	if at.MaxTokens > 0 {
		req.MaxTokens = at.MaxTokens
	}
	if at.Temperature != nil {
		req.Temperature = *at.Temperature
	}
	return req
}

// Complete sends one forced-tool-use request; the raw output is the tool input as JSON text, or
// empty when the response has no tool call.
func (a *Adapter) Complete(ctx context.Context, key string, at llm.Attempt) (llm.AttemptResult, error) {
	var resp messagesResponse
	err := wire.Do(ctx, a.hc, wire.Call{
		Provider: "Anthropic", Op: "messages", Method: http.MethodPost, URL: a.base + "/messages",
		Headers: a.headers(key), Body: buildRequest(at), Key: key, ErrorMessage: true,
	}, &resp)
	if err != nil {
		return llm.AttemptResult{}, err
	}
	out := llm.AttemptResult{}
	for _, block := range resp.Content {
		if block.Type == "tool_use" {
			var compact bytes.Buffer
			if len(block.Input) > 0 && json.Compact(&compact, block.Input) == nil {
				out.Raw = compact.String()
			}
			break
		}
	}
	if resp.Usage != nil {
		out.Usage = &llm.Usage{TokensIn: resp.Usage.InputTokens, TokensOut: resp.Usage.OutputTokens}
	}
	return out, nil
}

// ListModels lists the first page of models in upstream order.
func (a *Adapter) ListModels(ctx context.Context, key string) ([]llm.ModelInfo, error) {
	var list struct {
		Data []struct {
			ID          string `json:"id"`
			DisplayName string `json:"display_name"`
		} `json:"data"`
	}
	err := wire.Do(ctx, a.hc, wire.Call{
		Provider: "Anthropic", Op: "/models", Method: http.MethodGet, URL: a.base + "/models",
		Headers: a.headers(key), Key: key, ErrorMessage: true,
	}, &list)
	if err != nil {
		return nil, err
	}
	models := make([]llm.ModelInfo, len(list.Data))
	for i, m := range list.Data {
		label := m.DisplayName
		models[i] = llm.ModelInfo{ID: m.ID, Provider: llm.Anthropic, Label: &label}
	}
	return models, nil
}

// noRedirects stops the client at a provider's 3xx: following it would resend the key header
// (Anthropic's x-api-key survives a cross-host redirect) and the request body to another host,
// possibly over plain http. The 3xx is then handled like any other non-2xx answer.
func noRedirects(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

// withoutRedirects returns a copy of hc (a new client when nil) that never follows redirects;
// the caller's client is left unchanged.
func withoutRedirects(hc *http.Client) *http.Client {
	c := http.Client{}
	if hc != nil {
		c = *hc
	}
	c.CheckRedirect = noRedirects
	return &c
}

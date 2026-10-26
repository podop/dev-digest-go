package anthropic

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"dev-digest-go/backend/internal/llm"
	"dev-digest-go/backend/internal/llm/adapters/internal/wire"
	"dev-digest-go/backend/internal/llm/custom"
)

// CustomOptions configures the adapter of one custom Anthropic-compatible provider (spec FR4,
// FR8).
type CustomOptions struct {
	// ID is the provider id, "custom:<slug>"; ListModels stamps it on every model.
	ID string
	// Name is the display name in error texts; empty = ID.
	Name string
	// BaseURL is the API root including any version segment, as stored (custom.CheckBaseURL is
	// applied again); requests go to <BaseURL>/messages and <BaseURL>/models.
	BaseURL string
	// AuthMode is header (<AuthHeader>: <Key>) or bearer (Authorization: Bearer <Key>); empty =
	// header, the default of the anthropic protocol. Without a Key no auth header is sent.
	AuthMode custom.AuthMode
	// AuthHeader is the header of the header mode; empty = custom.DefaultAuthHeader (x-api-key).
	AuthHeader string
	// Key is the API key; the key argument of Complete and ListModels is ignored.
	Key string
	// Headers are the extra static headers (at most custom.MaxHeaders). An extra header named
	// anthropic-version replaces the default 2023-06-01.
	Headers map[string]string
	// HTTPClient performs the requests and is required: the caller owns the outbound policy (the
	// egress client with the address guard). It must not set Client.Timeout. A nil client makes
	// every call fail; no default client is ever built.
	HTTPClient *http.Client
	// Policy is the timeouts and retries; the zero value takes the built-in limits (120 s per
	// request, 30 s per model list, 3 retries each, NFR4).
	Policy llm.Policy
}

// CustomAdapter talks to one custom Anthropic-compatible gateway. Build it per call from the
// provider's stored definition; it holds the key and the header values.
type CustomAdapter struct {
	id      llm.Provider
	name    string
	base    string
	headers map[string]string
	policy  llm.Policy
	hc      *http.Client
	// initErr is why the adapter cannot call: every call returns it without a request.
	initErr error
}

var _ llm.Adapter = (*CustomAdapter)(nil)

// NewCustom returns the adapter of a custom Anthropic-compatible provider. It never fails to
// construct: a bad option (id, base URL, auth, header, missing client) makes Complete and
// ListModels return that error before any request is made.
func NewCustom(opts CustomOptions) *CustomAdapter {
	a := &CustomAdapter{id: llm.Provider(opts.ID), name: opts.Name}
	if a.name == "" {
		a.name = opts.ID
	}
	a.policy = opts.Policy
	if a.policy == (llm.Policy{}) {
		a.policy = llm.Policy{RequestTimeout: requestTimeout, Retries: retries, ListTimeout: listTimeout, ListRetries: retries}
	}
	fail := func(format string, args ...any) *CustomAdapter {
		a.initErr = fmt.Errorf("custom provider %s: %s", a.name, fmt.Sprintf(format, args...))
		return a
	}
	if _, ok := custom.ParseID(opts.ID); !ok {
		return fail("invalid provider id")
	}
	if opts.HTTPClient == nil {
		return fail("no HTTP client")
	}
	base, _, msg := custom.CheckBaseURL(opts.BaseURL)
	if msg != "" {
		return fail("%s", msg)
	}
	a.base = base
	mode := opts.AuthMode
	if mode == "" {
		mode = custom.AuthHeader
	}
	headers, err := wire.CustomHeaders(mode, opts.AuthHeader, opts.Key, opts.Headers, map[string]string{"anthropic-version": apiVersion})
	if err != nil {
		return fail("%s", err)
	}
	a.headers = headers
	secrets := []string{opts.Key}
	for _, v := range opts.Headers {
		secrets = append(secrets, v)
	}
	a.hc = wire.CustomClient(opts.HTTPClient, secrets)
	return a
}

// RequiresKey is false: a gateway may authenticate by network alone (spec FR4).
func (a *CustomAdapter) RequiresKey() bool { return false }

// Policy returns the timeouts and retry counts of the options.
func (a *CustomAdapter) Policy() llm.Policy { return a.policy }

// Complete sends one request shaped by at.Mode: strict (also the empty mode) is the built-in
// forced tool call and the raw output is the tool input as JSON text; prompt_only sends no tools
// and no tool_choice (the caller put the schema in the prompt, FR33) and the raw output is the
// text content blocks joined in order. json_object does not exist for this protocol and is
// refused. The cost is usage.cost when it is a finite number >= 0.
func (a *CustomAdapter) Complete(ctx context.Context, _ string, at llm.Attempt) (llm.AttemptResult, error) {
	if a.initErr != nil {
		return llm.AttemptResult{}, a.initErr
	}
	req := buildRequest(at)
	promptOnly := false
	switch at.Mode {
	case "", custom.ModeStrict:
	case custom.ModePromptOnly:
		promptOnly = true
		req.Tools, req.ToolChoice = nil, nil
	default:
		return llm.AttemptResult{}, fmt.Errorf("custom provider %s: unsupported structured output mode %q", a.name, string(at.Mode))
	}
	var resp messagesResponse
	err := wire.Do(ctx, a.hc, wire.Call{
		Provider: a.name, Op: "messages", Method: http.MethodPost, URL: a.base + "/messages",
		Headers: a.headers, Body: req, ErrorMessage: true,
	}, &resp)
	if err != nil {
		return llm.AttemptResult{}, err
	}
	out := llm.AttemptResult{}
	if promptOnly {
		var text strings.Builder
		for _, block := range resp.Content {
			if block.Type == "text" {
				text.WriteString(block.Text)
			}
		}
		out.Raw = text.String()
	} else {
		for _, block := range resp.Content {
			if block.Type == "tool_use" {
				var compact bytes.Buffer
				if len(block.Input) > 0 && json.Compact(&compact, block.Input) == nil {
					out.Raw = compact.String()
				}
				break
			}
		}
	}
	if u := resp.Usage; u != nil {
		out.Usage = &llm.Usage{TokensIn: u.InputTokens, TokensOut: u.OutputTokens}
		if cost, ok := wire.JSONNumber(u.Cost); ok && cost >= 0 {
			out.CostUSD = &cost
		}
	}
	return out, nil
}

// ListModels is one GET <base>/models; every data[].id becomes a model of this provider, in
// upstream order (the caller sorts and merges), labelled with display_name when present. A 404
// or 405 is an error whose StatusCode() the caller maps to "Model list not available" (spec
// FR11, D5).
func (a *CustomAdapter) ListModels(ctx context.Context, _ string) ([]llm.ModelInfo, error) {
	if a.initErr != nil {
		return nil, a.initErr
	}
	var list struct {
		Data []struct {
			ID          string `json:"id"`
			DisplayName string `json:"display_name"`
		} `json:"data"`
	}
	err := wire.Do(ctx, a.hc, wire.Call{
		Provider: a.name, Op: "/models", Method: http.MethodGet, URL: a.base + "/models",
		Headers: a.headers, ErrorMessage: true,
	}, &list)
	if err != nil {
		return nil, err
	}
	models := make([]llm.ModelInfo, 0, len(list.Data))
	seen := make(map[string]struct{}, len(list.Data))
	for _, m := range list.Data {
		if strings.TrimSpace(m.ID) == "" {
			continue
		}
		if _, dup := seen[m.ID]; dup {
			continue
		}
		seen[m.ID] = struct{}{}
		info := llm.ModelInfo{ID: m.ID, Provider: a.id}
		if m.DisplayName != "" {
			label := m.DisplayName
			info.Label = &label
		}
		models = append(models, info)
	}
	return models, nil
}

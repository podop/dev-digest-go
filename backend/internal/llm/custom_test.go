package llm_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"testing"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/llm"
	"dev-digest-go/backend/internal/llm/adapters/mock"
	"dev-digest-go/backend/internal/llm/custom"
	"dev-digest-go/backend/internal/platform/apperr"
)

const (
	customKey    = "sk-custom-gateway-key-777"
	customHeader = "hdr-value-secret-42"
)

// fakeResolver is a CustomResolver over a fixed table keyed by workspace and provider id.
type fakeResolver struct {
	mu    sync.Mutex
	calls []string // "ws/provider"
	defs  map[string]llm.CustomCall
	err   error
}

func (r *fakeResolver) Resolve(_ context.Context, ws string, p llm.Provider) (llm.CustomCall, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, ws+"/"+string(p))
	if r.err != nil {
		return llm.CustomCall{}, r.err
	}
	call, ok := r.defs[ws+"/"+string(p)]
	if !ok {
		return llm.CustomCall{}, fmt.Errorf("workspace %s: %w", ws, llm.ErrCustomNotConfigured)
	}
	return call, nil
}

// manualPricer prices the manual models of one custom provider: per-million prices by model.
type manualPricer map[string][2]float64

func (m manualPricer) Estimate(model string, in, out int) *float64 {
	p, ok := m[model]
	if !ok {
		return nil
	}
	c := float64(in)*p[0]/1e6 + float64(out)*p[1]/1e6
	return &c
}

// customClient is a Client with one custom provider "custom:synapse" in workspace ws-1, served by ad.
func customClient(t *testing.T, ad llm.Adapter, mod func(*llm.CustomCall), opts ...func(*llm.Options)) (*llm.Client, *fakeResolver, *logBuf) {
	t.Helper()
	call := llm.CustomCall{
		Adapter: ad, Name: "Synapse", Protocol: custom.ProtocolOpenAI, Host: "gw.example.com",
		Mode: custom.ModeStrict, Secrets: []string{customKey, customHeader},
	}
	if mod != nil {
		mod(&call)
	}
	res := &fakeResolver{defs: map[string]llm.CustomCall{"ws-1/custom:synapse": call}}
	c, logs := newClient(t, &fakeAdapter{reply: answer(validAnswer)}, newKeys(), func(o *llm.Options) {
		o.Custom = res
		o.Recorder = nil
		for _, f := range opts {
			f(o)
		}
	})
	return c, res, logs
}

func customRequest() llm.Request {
	req := chargedRequest()
	req.Provider = "custom:synapse"
	req.Model = "gpt-4.1" // a built-in model name on purpose: the price books must stay out of it
	return req
}

func TestCustom_notConfigured(t *testing.T) { // FR9, EC8
	t.Parallel()
	ad := &fakeAdapter{reply: answer(validAnswer)}
	const want = "Custom provider custom:ghost is not configured"
	withResolver, _, _ := customClient(t, ad, nil)
	noResolver, _ := newClient(t, ad, newKeys(), nil)
	tests := []struct {
		name   string
		client *llm.Client
		ws     string
	}{
		{"undefined id", withResolver, "ws-1"},
		{"no resolver wired", noResolver, "ws-1"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := customRequest()
			req.Provider = "custom:ghost"
			req.Charge.WorkspaceID = tc.ws
			_, err := tc.client.Structured(t.Context(), req)
			var ae *apperr.Error
			if !errors.As(err, &ae) || ae.Kind != apperr.KindConfig || ae.Code != "config_error" || ae.Message != want {
				t.Errorf("Structured(custom:ghost) error = %v, want config_error %q", err, want)
			}
			if got := tc.client.ReadyIn(t.Context(), tc.ws, "custom:ghost"); got == nil || got.Error() != want {
				t.Errorf("ReadyIn(custom:ghost) = %v, want %q", got, want)
			}
			if _, got := tc.client.ListModelsIn(t.Context(), tc.ws, "custom:ghost"); got == nil || got.Error() != want {
				t.Errorf("ListModelsIn(custom:ghost) = %v, want %q", got, want)
			}
		})
	}
	if n := ad.callCount(); n != 0 {
		t.Errorf("adapter calls = %d, want 0 (no network request for an undefined provider)", n)
	}
}

func TestCustom_resolverFailureIsInternalAndNotAnAdapterCall(t *testing.T) {
	t.Parallel()
	ad := &fakeAdapter{reply: answer(validAnswer)}
	c, res, _ := customClient(t, ad, nil)
	res.err = errors.New("db down")
	_, err := c.Structured(t.Context(), customRequest())
	var ae *apperr.Error
	if !errors.As(err, &ae) || ae.Kind != apperr.KindInternal || strings.Contains(ae.Message, "db down") {
		t.Errorf("Structured() error = %v, want an internal error whose client text hides the cause", err)
	}
	if ad.callCount() != 0 {
		t.Errorf("adapter calls = %d, want 0", ad.callCount())
	}
}

func TestCustom_resolvesInTheChargedWorkspaceAndNeedsNoKey(t *testing.T) { // FR9, D4, P4
	t.Parallel()
	ad := &fakeAdapter{reply: answer(validAnswer)} // RequiresKey() is true; a custom call must not care
	c, res, _ := customClient(t, ad, nil)
	if _, err := c.Structured(t.Context(), customRequest()); err != nil {
		t.Fatalf("Structured() error = %v", err)
	}
	if diff := cmp.Diff([]string{"ws-1/custom:synapse"}, res.calls); diff != "" {
		t.Errorf("Resolve calls mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff([]string{""}, ad.keys); diff != "" {
		t.Errorf("key passed to Complete mismatch (-want +got):\n%s", diff)
	}
	// The same id in another workspace is another (here: undefined) provider.
	other := customRequest()
	other.Charge.WorkspaceID = "ws-2"
	if _, err := c.Structured(t.Context(), other); err == nil || !strings.Contains(err.Error(), "is not configured") {
		t.Errorf("Structured() in ws-2 error = %v, want not configured", err)
	}
	// A call without a Charge has no workspace: nothing is defined there.
	bare := customRequest()
	bare.Charge = nil
	if _, err := c.Structured(t.Context(), bare); err == nil {
		t.Error("Structured() without a Charge error = nil, want not configured")
	}
	if diff := cmp.Diff([]string{"ws-1/custom:synapse", "ws-2/custom:synapse", "/custom:synapse"}, res.calls); diff != "" {
		t.Errorf("Resolve calls mismatch (-want +got):\n%s", diff)
	}
}

func TestCustom_readyAndListModelsIn(t *testing.T) { // P4
	t.Parallel()
	ad := &fakeAdapter{reply: answer(validAnswer), list: func(_ context.Context, key string) ([]llm.ModelInfo, error) {
		if key != "" {
			return nil, errors.New("key must not be passed")
		}
		return []llm.ModelInfo{{ID: "m1", Provider: "custom:synapse"}}, nil
	}}
	c, _, _ := customClient(t, ad, nil)
	if err := c.ReadyIn(t.Context(), "ws-1", "custom:synapse"); err != nil {
		t.Errorf("ReadyIn(defined) = %v, want nil (a custom provider needs no key)", err)
	}
	if err := c.Ready("custom:synapse"); err == nil {
		t.Error("Ready(custom) = nil, want not configured: a custom provider is checked per workspace")
	}
	got, err := c.ListModelsIn(t.Context(), "ws-1", "custom:synapse")
	if err != nil || len(got) != 1 || got[0].ID != "m1" {
		t.Errorf("ListModelsIn() = %v, %v, want [m1]", got, err)
	}
	if _, err := c.ListModels(t.Context(), "custom:synapse"); err == nil {
		t.Error("ListModels(custom) error = nil, want not configured")
	}
	// Built-ins go through the same entry points unchanged.
	if err := c.ReadyIn(t.Context(), "ws-1", llm.OpenAI); err == nil || err.Error() != "OPENAI_API_KEY is not configured" {
		t.Errorf("ReadyIn(openai) = %v, want the key error", err)
	}
}

func TestCustom_listModelsErrorIsScrubbed(t *testing.T) { // NFR6
	t.Parallel()
	ad := &fakeAdapter{list: func(context.Context, string) ([]llm.ModelInfo, error) {
		return nil, statusErrText{status: 401, text: "bad key " + customKey + " header " + customHeader}
	}}
	c, _, _ := customClient(t, ad, nil)
	_, err := c.ListModelsIn(t.Context(), "ws-1", "custom:synapse")
	if err == nil || strings.Contains(err.Error(), customKey) || strings.Contains(err.Error(), customHeader) {
		t.Errorf("ListModelsIn() error = %v, want key and header value scrubbed", err)
	}
}

// statusErrText is a provider failure with a status and a message.
type statusErrText struct {
	status int
	text   string
}

func (e statusErrText) Error() string   { return e.text }
func (e statusErrText) StatusCode() int { return e.status }

func TestCustom_costPerAttempt(t *testing.T) { // FR12, EC9, AC12
	t.Parallel()
	price := manualPricer{"gpt-4.1": {2, 8}}
	f := func(v float64) *float64 { return &v }
	tests := []struct {
		name    string
		price   llm.Pricer
		result  llm.AttemptResult
		want    *float64
		wantSum *float64
	}{
		{"reported cost wins over a manual price", price,
			llm.AttemptResult{Raw: validAnswer, Usage: &llm.Usage{TokensIn: 1e6, TokensOut: 5e5}, CostUSD: f(0.0123)}, f(0.0123), f(0.0123)},
		{"reported zero is a cost", price,
			llm.AttemptResult{Raw: validAnswer, Usage: &llm.Usage{TokensIn: 10, TokensOut: 10}, CostUSD: f(0)}, f(0), f(0)},
		{"no cost: manual price on reported tokens", price,
			llm.AttemptResult{Raw: validAnswer, Usage: &llm.Usage{TokensIn: 1e6, TokensOut: 5e5}}, f(6), f(6)},
		{"negative cost ignored, no price", nil,
			llm.AttemptResult{Raw: validAnswer, Usage: &llm.Usage{TokensIn: 1e6, TokensOut: 5e5}, CostUSD: f(-1)}, nil, nil},
		{"negative cost ignored, manual price", price,
			llm.AttemptResult{Raw: validAnswer, Usage: &llm.Usage{TokensIn: 1e6, TokensOut: 5e5}, CostUSD: f(-1)}, f(6), f(6)},
		{"NaN cost ignored", price,
			llm.AttemptResult{Raw: validAnswer, Usage: &llm.Usage{TokensIn: 1e6, TokensOut: 5e5}, CostUSD: f(math.NaN())}, f(6), f(6)},
		{"infinite cost ignored, no price", nil,
			llm.AttemptResult{Raw: validAnswer, Usage: &llm.Usage{TokensIn: 1, TokensOut: 1}, CostUSD: f(math.Inf(1))}, nil, nil},
		{"no usage at all: estimated tokens are never priced", price,
			llm.AttemptResult{Raw: validAnswer}, nil, nil},
		{"no price: NULL, not the price book's gpt-4.1 price", nil,
			llm.AttemptResult{Raw: validAnswer, Usage: &llm.Usage{TokensIn: 1e6, TokensOut: 5e5}}, nil, nil},
		{"model without a manual price", manualPricer{"other": {2, 8}},
			llm.AttemptResult{Raw: validAnswer, Usage: &llm.Usage{TokensIn: 1e6, TokensOut: 5e5}}, nil, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rec := &fakeRecorder{}
			ad := &fakeAdapter{reply: func(int, context.Context, llm.Attempt) (llm.AttemptResult, error) { return tc.result, nil }}
			c, _, _ := customClient(t, ad, func(cc *llm.CustomCall) { cc.Price = tc.price }, withRecorder(rec),
				// The built-in book would price gpt-4.1; a custom call must never ask it.
				func(o *llm.Options) { o.Prices = fixedPricer{cost: map[string]float64{"gpt-4.1": 99}} })
			var seen *float64
			seenCalls := 0
			req := customRequest()
			req.OnUsage = func(u llm.AttemptUsage) { seen = u.CostUSD; seenCalls++ }
			res, err := c.Structured(t.Context(), req)
			if err != nil {
				t.Fatalf("Structured() error = %v", err)
			}
			recs := rec.records()
			if len(recs) != 1 {
				t.Fatalf("RecordUsage calls = %d, want 1", len(recs))
			}
			for name, got := range map[string]*float64{"RecordUsage": recs[0].CostUSD, "OnUsage": seen, "Result": res.CostUSD} {
				if diff := cmp.Diff(tc.want, got); diff != "" {
					t.Errorf("%s cost mismatch (-want +got):\n%s", name, diff)
				}
			}
			if recs[0].Provider != "custom:synapse" {
				t.Errorf("RecordUsage provider = %q, want the custom id", recs[0].Provider)
			}
		})
	}
}

func TestCustom_costSumsAttemptsAndOneUnknownMakesTheSumUnknown(t *testing.T) { // FR12
	t.Parallel()
	price := manualPricer{"gpt-4.1": {2, 8}}
	f := func(v float64) *float64 { return &v }
	tests := []struct {
		name  string
		first llm.AttemptResult
		want  *float64
	}{
		{"both priced", llm.AttemptResult{Raw: `{"x":1}`, Usage: &llm.Usage{TokensIn: 1e6}, CostUSD: f(1)}, f(1 + 6)},
		{"first estimated", llm.AttemptResult{Raw: `{"x":1}`}, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ad := &fakeAdapter{reply: func(n int, _ context.Context, _ llm.Attempt) (llm.AttemptResult, error) {
				if n == 1 {
					return tc.first, nil
				}
				return llm.AttemptResult{Raw: validAnswer, Usage: &llm.Usage{TokensIn: 1e6, TokensOut: 5e5}}, nil
			}}
			c, _, _ := customClient(t, ad, func(cc *llm.CustomCall) { cc.Price = price })
			res, err := c.Structured(t.Context(), customRequest())
			if err != nil || res.Attempts != 2 {
				t.Fatalf("Structured() = %+v, %v, want 2 attempts", res, err)
			}
			if diff := cmp.Diff(tc.want, res.CostUSD); diff != "" {
				t.Errorf("Result.CostUSD mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestCustom_pricerSeesOnlyTheCustomProvidersModels(t *testing.T) { // FR12: no price book
	t.Parallel()
	ad := &fakeAdapter{reply: answer(validAnswer)}
	c, _, _ := customClient(t, ad, nil, func(o *llm.Options) {
		o.Prices = fixedPricer{cost: map[string]float64{"gpt-4.1": 99}}
	})
	res, err := c.Structured(t.Context(), customRequest())
	if err != nil || res.CostUSD != nil {
		t.Errorf("Structured() = cost %v, %v; want NULL cost for a custom provider without a price", res.CostUSD, err)
	}
	// Built-ins keep their price: the same model through openai is priced by the book.
	ad2 := &fakeAdapter{reply: answer(validAnswer)}
	b, _ := newClient(t, ad2, newKeys("OPENAI_API_KEY", openAIKey), func(o *llm.Options) {
		o.Prices = fixedPricer{cost: map[string]float64{"gpt-4.1": 99}}
	})
	res, err = b.Structured(t.Context(), baseRequest())
	if err != nil || res.CostUSD == nil || *res.CostUSD != 99 {
		t.Errorf("built-in Structured() = cost %v, %v; want the book's 99", res.CostUSD, err)
	}
}

func TestCustom_modeReachesTheAdapterAndPromptModesCarryTheSchema(t *testing.T) { // FR33, P6, EC31, AC40
	t.Parallel()
	tests := []struct {
		mode       custom.Mode
		wantSchema bool
	}{
		{custom.ModeStrict, false},
		{custom.ModeJSONObject, true},
		{custom.ModePromptOnly, true},
	}
	for _, tc := range tests {
		t.Run(string(tc.mode), func(t *testing.T) {
			t.Parallel()
			ad := &fakeAdapter{reply: func(n int, _ context.Context, _ llm.Attempt) (llm.AttemptResult, error) {
				if n == 1 { // one re-prompt: the block must still be there at request 2
					return llm.AttemptResult{Raw: `{"candidates":[{}]}`, Usage: &llm.Usage{TokensIn: 1, TokensOut: 1}}, nil
				}
				return llm.AttemptResult{Raw: "Sure!\n```json\n" + validAnswer + "\n```", Usage: &llm.Usage{TokensIn: 1, TokensOut: 1}}, nil
			}}
			c, _, _ := customClient(t, ad, func(cc *llm.CustomCall) { cc.Mode = tc.mode })
			req := customRequest()
			orig := append([]llm.Message(nil), req.Messages...)
			if _, err := c.Structured(t.Context(), req); err != nil {
				t.Fatalf("Structured() error = %v", err)
			}
			if diff := cmp.Diff(orig, req.Messages); diff != "" {
				t.Errorf("Request.Messages changed (-want +got):\n%s", diff)
			}
			if len(ad.calls) != 2 {
				t.Fatalf("adapter calls = %d, want 2 (one re-prompt)", len(ad.calls))
			}
			for i, a := range ad.calls {
				if a.Mode != tc.mode {
					t.Errorf("request %d Attempt.Mode = %q, want %q", i+1, a.Mode, tc.mode)
				}
				sys := a.Messages[0]
				has := strings.Contains(sys.Content, `"candidates"`) && strings.Contains(sys.Content, "exactly one JSON object")
				if sys.Role != llm.System || !strings.HasPrefix(sys.Content, "sys") || has != tc.wantSchema {
					t.Errorf("request %d system message = %q, want the schema block = %v after the original text", i+1, sys.Content, tc.wantSchema)
				}
				if len(a.Schema) == 0 {
					t.Errorf("request %d Attempt.Schema empty, want the strict schema in every mode", i+1)
				}
			}
			if last := ad.calls[1].Messages; !strings.Contains(last[len(last)-1].Content, "did not match the required schema") {
				t.Errorf("re-prompt = %q, want the schema issues", last[len(last)-1].Content)
			}
		})
	}
}

func TestCustom_promptModeWithoutASystemMessageGetsOne(t *testing.T) {
	t.Parallel()
	ad := &fakeAdapter{reply: answer(validAnswer)}
	c, _, _ := customClient(t, ad, func(cc *llm.CustomCall) { cc.Mode = custom.ModePromptOnly })
	req := customRequest()
	req.Messages = []llm.Message{{Role: llm.User, Content: "hello"}}
	if _, err := c.Structured(t.Context(), req); err != nil {
		t.Fatalf("Structured() error = %v", err)
	}
	got := ad.calls[0].Messages
	if len(got) != 2 || got[0].Role != llm.System || !strings.Contains(got[0].Content, `"candidates"`) || got[1].Content != "hello" {
		t.Errorf("messages = %+v, want a system message with the schema, then the user message", got)
	}
}

func TestCustom_unsetModeIsStrict(t *testing.T) {
	t.Parallel()
	ad := &fakeAdapter{reply: answer(validAnswer)}
	c, _, _ := customClient(t, ad, func(cc *llm.CustomCall) { cc.Mode = "" })
	if _, err := c.Structured(t.Context(), customRequest()); err != nil {
		t.Fatalf("Structured() error = %v", err)
	}
	if got := ad.calls[0]; got.Mode != custom.ModeStrict || got.Messages[0].Content != "sys" {
		t.Errorf("Attempt = mode %q, system %q; want strict and an untouched prompt", got.Mode, got.Messages[0].Content)
	}
}

func TestCustom_builtInAttemptsCarryNoMode(t *testing.T) {
	t.Parallel()
	ad := &fakeAdapter{reply: answer(validAnswer)}
	c, _ := newClient(t, ad, newKeys("OPENAI_API_KEY", openAIKey), nil)
	if _, err := c.Structured(t.Context(), baseRequest()); err != nil {
		t.Fatalf("Structured() error = %v", err)
	}
	if got := ad.calls[0]; got.Mode != "" || got.Messages[0].Content != "sys" {
		t.Errorf("built-in Attempt = mode %q, system %q; want no mode and an untouched prompt", got.Mode, got.Messages[0].Content)
	}
}

func TestCustom_strictRefusalGetsTheHint(t *testing.T) { // FR33, EC15, AC41
	t.Parallel()
	const openaiHint = "If this gateway does not support strict structured output, set Structured output of Synapse to JSON object or Prompt only."
	const anthropicHint = "If this gateway does not support strict structured output, set Structured output of Synapse to Prompt only."
	tests := []struct {
		name     string
		protocol custom.Protocol
		mode     custom.Mode
		status   int
		hint     string // "" = no hint
	}{
		{"openai strict 400", custom.ProtocolOpenAI, custom.ModeStrict, 400, openaiHint},
		{"openai strict 422", custom.ProtocolOpenAI, custom.ModeStrict, 422, openaiHint},
		{"anthropic strict 400", custom.ProtocolAnthropic, custom.ModeStrict, 400, anthropicHint},
		{"unknown protocol uses the wording valid for both", "", custom.ModeStrict, 400, anthropicHint},
		{"strict 500 has no hint", custom.ProtocolOpenAI, custom.ModeStrict, 500, ""},
		{"strict 401 has no hint", custom.ProtocolOpenAI, custom.ModeStrict, 401, ""},
		{"json_object 400 has no hint", custom.ProtocolOpenAI, custom.ModeJSONObject, 400, ""},
		{"prompt_only 400 has no hint", custom.ProtocolOpenAI, custom.ModePromptOnly, 400, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ad := &fakeAdapter{reply: func(int, context.Context, llm.Attempt) (llm.AttemptResult, error) {
				return llm.AttemptResult{}, statusErrText{status: tc.status, text: "gateway said no, key " + customKey + " hdr " + customHeader}
			}, policy: llm.Policy{RequestTimeout: 1e9, Retries: 0}}
			c, _, _ := customClient(t, ad, func(cc *llm.CustomCall) { cc.Protocol, cc.Mode = tc.protocol, tc.mode })
			_, err := c.Structured(t.Context(), customRequest())
			var ae *apperr.Error
			if !errors.As(err, &ae) || ae.Kind != apperr.KindExternalService {
				t.Fatalf("Structured() error = %v, want external_service", err)
			}
			if strings.Contains(ae.Message, customKey) || strings.Contains(ae.Message, customHeader) {
				t.Errorf("message = %q, want key and header value scrubbed", ae.Message)
			}
			if !strings.Contains(ae.Message, "gateway said no") {
				t.Errorf("message = %q, want the gateway message", ae.Message)
			}
			if tc.hint != "" && !strings.HasSuffix(ae.Message, "\n"+tc.hint) {
				t.Errorf("message = %q, want it to end with the hint %q", ae.Message, tc.hint)
			}
			if tc.hint == "" && strings.Contains(ae.Message, "If this gateway") {
				t.Errorf("message = %q, want no hint", ae.Message)
			}
			if n := ad.callCount(); n != 1 {
				t.Errorf("adapter calls = %d, want 1 (never repeated in another mode)", n)
			}
		})
	}
}

func TestCustom_builtInErrorsGetNoHint(t *testing.T) {
	t.Parallel()
	ad := &fakeAdapter{reply: func(int, context.Context, llm.Attempt) (llm.AttemptResult, error) {
		return llm.AttemptResult{}, statusErr(400)
	}}
	c, _ := newClient(t, ad, newKeys("OPENAI_API_KEY", openAIKey), nil)
	_, err := c.Structured(t.Context(), baseRequest())
	if err == nil || strings.Contains(err.Error(), "If this gateway") {
		t.Errorf("Structured() error = %v, want no custom hint on a built-in provider", err)
	}
}

func TestCustom_secretsAreRedactedBeforeTheCut(t *testing.T) { // NFR6
	t.Parallel()
	// The answer is invalid JSON that echoes the key and a header value; the final failure text
	// shows a cut of it, which must not hold either secret (or half of one).
	ad := &fakeAdapter{reply: func(int, context.Context, llm.Attempt) (llm.AttemptResult, error) {
		return llm.AttemptResult{Raw: strings.Repeat("x", 380) + customKey + customHeader, Usage: &llm.Usage{TokensIn: 1, TokensOut: 1}}, nil
	}}
	c, _, _ := customClient(t, ad, nil)
	_, err := c.Structured(t.Context(), customRequest())
	if err == nil {
		t.Fatal("Structured() error = nil, want a schema failure")
	}
	for _, s := range []string{customKey, customHeader, customKey[:10], customHeader[:10]} {
		if strings.Contains(err.Error(), s) {
			t.Errorf("error %q contains %q, want it redacted", err, s)
		}
	}
}

func TestCustom_oneLogLinePerRequest(t *testing.T) { // NFR6, AC18, NFR4
	t.Parallel()
	ad := &fakeAdapter{reply: func(n int, _ context.Context, _ llm.Attempt) (llm.AttemptResult, error) {
		if n < 3 {
			return llm.AttemptResult{}, statusErrText{status: 500, text: "boom " + customKey}
		}
		return llm.AttemptResult{Raw: validAnswer, Usage: &llm.Usage{TokensIn: 1, TokensOut: 1}}, nil
	}}
	c, _, logs := customClient(t, ad, nil)
	req := customRequest()
	req.Messages[1].Content = "the secret prompt text"
	if _, err := c.Structured(t.Context(), req); err != nil {
		t.Fatalf("Structured() error = %v", err)
	}
	var got []map[string]any
	for _, r := range logs.records(t) {
		if r["msg"] == "custom provider request" {
			got = append(got, r)
		}
	}
	if len(got) != 3 {
		t.Fatalf("request log lines = %d, want 3 (two failures, one success)", len(got))
	}
	for i, wantStatus := range []string{"500", "500", "2xx"} {
		r := got[i]
		if r["provider"] != "custom:synapse" || r["host"] != "gw.example.com" || r["operation"] != "complete" ||
			r["status"] != wantStatus || r["attempt"] != float64(i+1) {
			t.Errorf("log line %d = %v, want provider/host/operation/status %q/attempt %d", i+1, r, wantStatus, i+1)
		}
		if _, ok := r["latency_ms"]; !ok {
			t.Errorf("log line %d has no latency_ms: %v", i+1, r)
		}
	}
	all := fmt.Sprint(logs.records(t))
	for _, s := range []string{customKey, customHeader, "the secret prompt text", validAnswer} {
		if strings.Contains(all, s) {
			t.Errorf("logs contain %q, want no key, header value, prompt or answer", s)
		}
	}
}

func TestCustom_listingLogsAndBuiltInsDoNotLogRequestLines(t *testing.T) {
	t.Parallel()
	ad := &fakeAdapter{reply: answer(validAnswer), list: func(context.Context, string) ([]llm.ModelInfo, error) { return nil, nil }}
	c, _, logs := customClient(t, ad, nil)
	if _, err := c.ListModelsIn(t.Context(), "ws-1", "custom:synapse"); err != nil {
		t.Fatal(err)
	}
	var ops []any
	for _, r := range logs.records(t) {
		if r["msg"] == "custom provider request" {
			ops = append(ops, r["operation"])
		}
	}
	if diff := cmp.Diff([]any{"list_models"}, ops); diff != "" {
		t.Errorf("request log operations mismatch (-want +got):\n%s", diff)
	}

	b, blogs := newClient(t, ad, newKeys("OPENAI_API_KEY", openAIKey), nil)
	if _, err := b.Structured(t.Context(), baseRequest()); err != nil {
		t.Fatal(err)
	}
	for _, r := range blogs.records(t) {
		if r["msg"] == "custom provider request" {
			t.Errorf("built-in call logged %v, want no custom request line", r)
		}
	}
}

func TestCustom_mockOverrideAnswersForCustomProviders(t *testing.T) { // A4
	t.Parallel()
	// Under LLM_PROVIDER_OVERRIDE=mock the wiring's resolver hands out a mock adapter for any
	// well-formed custom id; the Client needs nothing more to answer without a network request.
	res := mockResolver{}
	c, _ := newClient(t, &fakeAdapter{}, newKeys(), func(o *llm.Options) { o.Custom = res })
	req := llm.Request{
		Provider: "custom:anything", Model: "m", SchemaName: mock.ConventionExtraction,
		Messages: []llm.Message{{Role: llm.System, Content: "sys"}, {Role: llm.User, Content: "nothing here"}},
		Schema:   json.RawMessage(`{"type":"object","required":["candidates"],"properties":{"candidates":{"type":"array"}}}`),
		Charge:   &llm.Charge{WorkspaceID: "ws-1", Kind: llm.KindBrief, RefID: "r"},
	}
	got, err := c.Structured(t.Context(), req)
	if err != nil || got.CostUSD == nil || *got.CostUSD != mock.CostUSD || got.TokensIn != mock.TokensIn {
		t.Errorf("Structured(custom, mock) = %+v, %v, want the mock's usage and cost", got, err)
	}
	if err := c.ReadyIn(t.Context(), "ws-1", "custom:anything"); err != nil {
		t.Errorf("ReadyIn(custom, mock) = %v, want nil", err)
	}
}

type mockResolver struct{}

func (mockResolver) Resolve(_ context.Context, _ string, p llm.Provider) (llm.CustomCall, error) {
	return llm.CustomCall{Adapter: mock.New(p, mock.Options{}), Name: string(p), Mode: custom.ModeStrict}, nil
}

func TestCustom_modelInfoJSON(t *testing.T) { // FR10 shape
	t.Parallel()
	label := "Gateway model"
	tests := []struct {
		name string
		in   llm.ModelInfo
		want string
	}{
		{"id only", llm.ModelInfo{ID: "m", Provider: "custom:synapse"}, `{"id":"m","provider":"custom:synapse"}`},
		{"manual label and pricing", llm.ModelInfo{ID: "m", Provider: "custom:synapse", Label: &label, Pricing: &llm.ModelPricing{PromptPerM: 2, CompletionPerM: 8}},
			`{"id":"m","provider":"custom:synapse","label":"Gateway model","pricing":{"promptPerM":2,"completionPerM":8}}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := json.Marshal(tc.in)
			if err != nil || string(got) != tc.want {
				t.Errorf("Marshal(%+v) = %s, %v; want %s", tc.in, got, err, tc.want)
			}
		})
	}
}

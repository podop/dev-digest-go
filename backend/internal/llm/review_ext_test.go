package llm_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	"dev-digest-go/backend/internal/llm"
	"dev-digest-go/backend/internal/platform/apperr"
	reviewdomain "dev-digest-go/backend/internal/reviewer/domain"
)

func TestReady(t *testing.T) {
	t.Parallel()
	ad := &fakeAdapter{reply: answer(validAnswer)}
	keys := newKeys("OPENAI_API_KEY", openAIKey)
	c, _ := newClient(t, ad, keys, func(o *llm.Options) {
		o.Adapters = map[llm.Provider]llm.Adapter{llm.OpenAI: ad, llm.Anthropic: ad}
	})
	tests := []struct {
		name     string
		provider llm.Provider
		want     string // error text, "" = ready
	}{
		{"key stored", llm.OpenAI, ""},
		{"key missing", llm.Anthropic, "ANTHROPIC_API_KEY is not configured"},
		{"no adapter", llm.OpenRouter, `no adapter configured for LLM provider "openrouter"`},
		{"unknown provider", llm.Provider("x"), `unknown LLM provider "x"`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := c.Ready(tc.provider)
			if tc.want == "" {
				if err != nil {
					t.Errorf("Ready(%q) = %v, want nil", tc.provider, err)
				}
				return
			}
			if err == nil || err.Error() != tc.want {
				t.Errorf("Ready(%q) = %v, want %q", tc.provider, err, tc.want)
			}
		})
	}

	_, err := c.Structured(t.Context(), func() llm.Request { r := baseRequest(); r.Provider = llm.Anthropic; return r }())
	var ready, called *apperr.Error
	if !errors.As(c.Ready(llm.Anthropic), &ready) || !errors.As(err, &called) || ready.Error() != called.Error() {
		t.Errorf("Ready(anthropic) = %v, Structured error = %v, want the same error", c.Ready(llm.Anthropic), err)
	}

	keys.Set("ANTHROPIC_API_KEY", "ak-rotated")
	if err := c.Ready(llm.Anthropic); err != nil {
		t.Errorf("Ready(anthropic) after saving the key = %v, want nil (the key is read on every call)", err)
	}
}

func TestReady_providerWithoutKey(t *testing.T) {
	t.Parallel()
	ad := &keylessAdapter{fakeAdapter{reply: answer(validAnswer)}}
	c, _ := newClient(t, ad, newKeys(), nil)
	if err := c.Ready(llm.OpenAI); err != nil {
		t.Errorf("Ready(openai) over a key-less adapter = %v, want nil", err)
	}
}

type keylessAdapter struct{ fakeAdapter }

func (*keylessAdapter) RequiresKey() bool { return false }

func TestStructured_onUsagePerAttempt(t *testing.T) {
	t.Parallel()
	f := func(v float64) *float64 { return &v }
	// Attempt 1 is schema-invalid (reported cost 0.01), attempt 2 is valid (no reported cost, priced).
	ad := &fakeAdapter{reply: func(n int, _ context.Context, _ llm.Attempt) (llm.AttemptResult, error) {
		if n == 1 {
			return llm.AttemptResult{Raw: `{"x":1}`, Usage: &llm.Usage{TokensIn: 10, TokensOut: 2}, CostUSD: f(0.01)}, nil
		}
		return llm.AttemptResult{Raw: validAnswer, Usage: &llm.Usage{TokensIn: 20, TokensOut: 4}}, nil
	}}
	c, _ := newClient(t, ad, newKeys("OPENAI_API_KEY", openAIKey), func(o *llm.Options) {
		o.Prices = fixedPricer{cost: map[string]float64{"known": 0.25}}
	})
	var got []llm.AttemptUsage
	req := baseRequest()
	req.Model = "known"
	req.OnUsage = func(u llm.AttemptUsage) { got = append(got, u) }
	if _, err := c.Structured(t.Context(), req); err != nil {
		t.Fatalf("Structured() error = %v", err)
	}
	want := []llm.AttemptUsage{
		{TokensIn: 10, TokensOut: 2, CostUSD: f(0.01)},
		{TokensIn: 20, TokensOut: 4, CostUSD: f(0.25)},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("OnUsage calls mismatch (-want +got):\n%s", diff)
	}
}

func TestStructured_onUsageSurvivesAFailedCall(t *testing.T) {
	t.Parallel()
	ad := &fakeAdapter{reply: answer(`{"x":1}`)} // never valid
	c, _ := newClient(t, ad, newKeys("OPENAI_API_KEY", openAIKey), nil)
	var got []llm.AttemptUsage
	req := baseRequest()
	req.Model = "unpriced-model"
	req.OnUsage = func(u llm.AttemptUsage) { got = append(got, u) }
	if _, err := c.Structured(t.Context(), req); err == nil {
		t.Fatal("Structured() error = nil, want the schema failure")
	}
	if len(got) != 1+llm.DefaultReprompts {
		t.Fatalf("OnUsage calls = %d, want %d (every response of the failed call)", len(got), 1+llm.DefaultReprompts)
	}
	if got[0].TokensIn != 100 || got[0].TokensOut != 50 || got[0].CostUSD != nil {
		t.Errorf("OnUsage(first) = %+v, want 100/50 tokens, unpriced (nil cost)", got[0])
	}
}

func TestStructured_onUsageNotCalledWithoutAResponse(t *testing.T) {
	t.Parallel()
	ad := &fakeAdapter{reply: func(int, context.Context, llm.Attempt) (llm.AttemptResult, error) {
		return llm.AttemptResult{}, statusErr(401)
	}}
	c, _ := newClient(t, ad, newKeys("OPENAI_API_KEY", openAIKey), nil)
	calls := 0
	req := baseRequest()
	req.OnUsage = func(llm.AttemptUsage) { calls++ }
	if _, err := c.Structured(t.Context(), req); err == nil {
		t.Fatal("Structured() error = nil, want the 401")
	}
	if calls != 0 {
		t.Errorf("OnUsage calls = %d, want 0 for a request that got no response", calls)
	}
}

func TestStructured_onUsagePanicDoesNotFailTheCall(t *testing.T) {
	t.Parallel()
	ad := &fakeAdapter{reply: answer(validAnswer)}
	c, logs := newClient(t, ad, newKeys("OPENAI_API_KEY", openAIKey), nil)
	req := baseRequest()
	req.OnUsage = func(llm.AttemptUsage) { panic("observer bug") }
	res, err := c.Structured(t.Context(), req)
	if err != nil || string(res.Data) != validAnswer {
		t.Fatalf("Structured() = %s, %v, want the answer despite the panicking observer", res.Data, err)
	}
	warned := false
	for _, r := range logs.records(t) {
		warned = warned || (r["level"] == "WARN" && strings.Contains(r["msg"].(string), "usage observer panicked"))
	}
	if !warned {
		t.Error("no warning logged for the panicking usage observer")
	}
}

func TestStructured_sessionIDReachesTheAdapter(t *testing.T) {
	t.Parallel()
	ad := &fakeAdapter{reply: answer(validAnswer)}
	c, _ := newClient(t, ad, newKeys("OPENROUTER_API_KEY", "or-key"), nil)
	req := baseRequest()
	req.Provider = llm.OpenRouter
	req.SessionID = "acme/payments-api#482:Security Reviewer"
	if _, err := c.Structured(t.Context(), req); err != nil {
		t.Fatalf("Structured() error = %v", err)
	}
	if got := ad.calls[0].SessionID; got != req.SessionID {
		t.Errorf("Attempt.SessionID = %q, want %q", got, req.SessionID)
	}
}

func TestStructured_promptLogChunkRoleAndTruncated(t *testing.T) {
	t.Parallel()
	ad := &fakeAdapter{reply: answer(validAnswer)}
	c, logs := newClient(t, ad, newKeys("OPENAI_API_KEY", openAIKey), nil)
	items, truncated, notTruncated := 3, true, false
	req := baseRequest()
	req.Feature, req.CorrelationID = "review", "run-1"
	req.Chunk = &llm.Chunk{Index: 1, Total: 2}
	req.Messages = []llm.Message{{Role: llm.System, Content: "SECRET SYSTEM"}, {Role: llm.User, Content: "SECRET USER"}}
	req.Sections = []llm.PromptSection{
		{Name: "system", Source: "agent_config", Role: "system", Trust: llm.Trusted, Chars: 8},
		{Name: "injection_guard", Source: "engine", Role: "system", Trust: llm.Trusted, Chars: 40},
		{Name: "task", Source: "author", Role: "user", Trust: llm.Untrusted, Chars: 4},
		{Name: "pr_description", Source: "author", Trust: llm.Untrusted, Chars: 4000, Truncated: &truncated},
		{Name: "specs", Source: "repo", Trust: llm.Untrusted, Chars: 12, Items: &items, Truncated: &notTruncated},
	}
	if _, err := c.Structured(t.Context(), req); err != nil {
		t.Fatalf("Structured() error = %v", err)
	}
	rec := logs.records(t)[0]
	if diff := cmp.Diff(map[string]any{"index": float64(1), "total": float64(2)}, rec["chunk"]); diff != "" {
		t.Errorf("chunk mismatch (-want +got):\n%s", diff)
	}
	secs, _ := rec["sections"].([]any)
	type brief struct {
		role      any
		items     any
		truncated any
	}
	got := map[string]brief{}
	for _, s := range secs {
		m := s.(map[string]any)
		got[m["name"].(string)] = brief{m["role"], m["items"], m["truncated"]}
	}
	want := map[string]brief{
		"system":          {role: "system"},
		"injection_guard": {role: "system"},
		"task":            {role: "user"},
		"pr_description":  {role: "user", truncated: true},
		"specs":           {role: "user", items: float64(3), truncated: false},
	}
	if diff := cmp.Diff(want, got, cmp.AllowUnexported(brief{})); diff != "" {
		t.Errorf("section role/items/truncated mismatch (-want +got):\n%s", diff)
	}
	for _, r := range logs.records(t) {
		if b := mustJSON(t, r); strings.Contains(b, "SECRET") {
			t.Errorf("log line %s carries prompt text", b)
		}
	}
}

func TestStructured_promptLogWithoutChunk(t *testing.T) {
	t.Parallel()
	ad := &fakeAdapter{reply: answer(validAnswer)}
	c, logs := newClient(t, ad, newKeys("OPENAI_API_KEY", openAIKey), nil)
	if _, err := c.Structured(t.Context(), baseRequest()); err != nil {
		t.Fatal(err)
	}
	if rec := logs.records(t)[0]; rec["chunk"] != nil {
		t.Errorf("prompt_assembled chunk = %v, want none when the request has no chunk", rec["chunk"])
	}
}

func TestStrictSchema_exported(t *testing.T) {
	t.Parallel()
	got, err := llm.StrictSchema([]byte(`{"type":"object","properties":{"b":{"type":"string"},"a":{"type":"array","maxItems":2,"items":{"type":"string"}}}}`))
	if err != nil {
		t.Fatalf("StrictSchema() error = %v", err)
	}
	want := `{"type":"object","properties":{"b":{"type":"string"},"a":{"type":"array","items":{"type":"string"}}},"required":["b","a"],"additionalProperties":false}`
	if string(got) != want {
		t.Errorf("StrictSchema() = %s, want %s", got, want)
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("json.Marshal(%v) error = %v", v, err)
	}
	return string(b)
}

// A strict provider answers every property of a Review, nulls included: the answer must validate
// against the Review schema as written (nullable scalars written as "nullable": true).
func TestStructured_reviewAnswerWithNullsValidates(t *testing.T) {
	t.Parallel()
	const answerWithNulls = `{"verdict":"comment","summary":"s","score":70,"findings":[{
	  "id":"f1","severity":"WARNING","category":"bug","title":"t","file":"a.ts","start_line":1,"end_line":2,
	  "rationale":"r","suggestion":null,"confidence":0.5,"kind":null,"trifecta_components":null,"evidence":null,
	  "skill":null,"out_of_scope":null}]}`
	ad := &fakeAdapter{reply: answer(answerWithNulls)}
	c, _ := newClient(t, ad, newKeys("OPENAI_API_KEY", openAIKey), nil)
	req := baseRequest()
	req.SchemaName, req.Schema = "Review", reviewdomain.ReviewSchemaJSON
	res, err := c.Structured(t.Context(), req)
	if err != nil {
		t.Fatalf("Structured() error = %v, want the answer with null optional fields accepted", err)
	}
	if res.Attempts != 1 {
		t.Errorf("Structured() attempts = %d, want 1", res.Attempts)
	}
	if got := string(ad.calls[0].Schema); got != string(reviewdomain.ReviewProviderSchemaJSON) {
		t.Errorf("schema sent to the provider differs from ReviewProviderSchemaJSON:\n got %s\nwant %s", got, reviewdomain.ReviewProviderSchemaJSON)
	}

	bad := strings.Replace(answerWithNulls, `"title":"t"`, `"title":null`, 1)
	ad2 := &fakeAdapter{reply: answer(bad)}
	c2, _ := newClient(t, ad2, newKeys("OPENAI_API_KEY", openAIKey), nil)
	req.Reprompts = llm.NoReprompts
	if _, err := c2.Structured(t.Context(), req); err == nil || !strings.Contains(err.Error(), "findings.0.title: Expected string, received null") {
		t.Errorf("Structured() with a null required title error = %v, want Expected string, received null", err)
	}
}

// A 200 answer the adapter cannot use still cost tokens: they are reported to the observer
// (parity with the Node provider, engine goldens llm.json no_choices_*), estimated when absent.
func TestStructured_answeredErrorReportsUsage(t *testing.T) {
	t.Parallel()
	cost := 0.0001
	tests := []struct {
		name  string
		reply llm.AttemptResult
		want  llm.AttemptUsage
	}{
		{"provider usage", llm.AttemptResult{Answered: true, Usage: &llm.Usage{TokensIn: 10}, CostUSD: &cost}, llm.AttemptUsage{TokensIn: 10, CostUSD: &cost}},
		// "sys\nhello" is 9 UTF-16 units: 3 tokens in, none out (the price book prices gpt-4.1, so the cost is not nil).
		{"estimated", llm.AttemptResult{Answered: true}, llm.AttemptUsage{TokensIn: 3, TokensOut: 0}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ad := &fakeAdapter{reply: func(int, context.Context, llm.Attempt) (llm.AttemptResult, error) {
				return tc.reply, errors.New("returned no choices")
			}}
			c, _ := newClient(t, ad, newKeys("OPENAI_API_KEY", openAIKey), nil)
			var got []llm.AttemptUsage
			req := baseRequest()
			req.OnUsage = func(u llm.AttemptUsage) { got = append(got, u) }
			if _, err := c.Structured(t.Context(), req); err == nil {
				t.Fatal("Structured() error = nil, want the adapter's failure")
			}
			opts := []cmp.Option{}
			if tc.reply.Usage == nil {
				opts = append(opts, cmpopts.IgnoreFields(llm.AttemptUsage{}, "CostUSD"))
			}
			if diff := cmp.Diff([]llm.AttemptUsage{tc.want}, got, opts...); diff != "" {
				t.Errorf("OnUsage calls mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

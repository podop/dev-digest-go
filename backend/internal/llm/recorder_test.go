package llm_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/llm"
)

// fakeRecorder collects what the Client records. onRecord runs inside RecordUsage.
type fakeRecorder struct {
	mu       sync.Mutex
	got      []llm.RecordedUsage
	onRecord func(n int)
}

func (r *fakeRecorder) RecordUsage(_ context.Context, u llm.RecordedUsage) {
	r.mu.Lock()
	r.got = append(r.got, u)
	n := len(r.got)
	r.mu.Unlock()
	if r.onRecord != nil {
		r.onRecord(n)
	}
}

func (r *fakeRecorder) records() []llm.RecordedUsage {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]llm.RecordedUsage(nil), r.got...)
}

func withRecorder(rec llm.Recorder) func(*llm.Options) {
	return func(o *llm.Options) { o.Recorder = rec }
}

func chargedRequest() llm.Request {
	req := baseRequest()
	req.Charge = &llm.Charge{WorkspaceID: "ws-1", Kind: llm.KindBrief, RefID: "ref-1"}
	return req
}

func warnings(t *testing.T, logs *logBuf, contains string) int {
	t.Helper()
	n := 0
	for _, r := range logs.records(t) {
		if r["level"] == "WARN" && strings.Contains(r["msg"].(string), contains) {
			n++
		}
	}
	return n
}

func TestRecorder_oncePerAnsweredAttemptIncludingReprompts(t *testing.T) {
	t.Parallel()
	rec := &fakeRecorder{}
	ad := &fakeAdapter{reply: func(n int, _ context.Context, _ llm.Attempt) (llm.AttemptResult, error) {
		if n == 1 {
			return llm.AttemptResult{Raw: `{"x":1}`, Usage: &llm.Usage{TokensIn: 10, TokensOut: 2}, CostUSD: costPtr(0.01)}, nil
		}
		return llm.AttemptResult{Raw: validAnswer, Usage: &llm.Usage{TokensIn: 20, TokensOut: 4}}, nil
	}}
	c, _ := newClient(t, ad, newKeys("OPENAI_API_KEY", openAIKey), func(o *llm.Options) {
		o.Recorder = rec
		o.Prices = fixedPricer{cost: map[string]float64{"known": 0.25}}
	})
	req := chargedRequest() // no OnUsage: the recorder does not depend on it
	req.Model = "known"
	if _, err := c.Structured(t.Context(), req); err != nil {
		t.Fatalf("Structured() error = %v", err)
	}
	charge := *req.Charge
	want := []llm.RecordedUsage{
		{Charge: charge, Provider: llm.OpenAI, Model: "known", Tokens: llm.Usage{TokensIn: 10, TokensOut: 2}, CostUSD: costPtr(0.01)},
		{Charge: charge, Provider: llm.OpenAI, Model: "known", Tokens: llm.Usage{TokensIn: 20, TokensOut: 4}, CostUSD: costPtr(0.25)},
	}
	if diff := cmp.Diff(want, rec.records()); diff != "" {
		t.Errorf("RecordUsage calls mismatch (-want +got):\n%s", diff)
	}
}

func TestRecorder_recordsBeforeTheAnswerIsValidated(t *testing.T) {
	t.Parallel()
	// An answer that never validates, with the budget of re-prompts exhausted: the call fails, and
	// every response was recorded before it was judged. The adapter also sees, at each request, that
	// the previous response was recorded before the re-prompt was sent.
	rec := &fakeRecorder{}
	var seen []int
	ad := &fakeAdapter{}
	ad.reply = func(int, context.Context, llm.Attempt) (llm.AttemptResult, error) {
		seen = append(seen, len(rec.records()))
		return llm.AttemptResult{Raw: `{"x":1}`, Usage: &llm.Usage{TokensIn: 5, TokensOut: 1}}, nil
	}
	c, _ := newClient(t, ad, newKeys("OPENAI_API_KEY", openAIKey), withRecorder(rec))
	if _, err := c.Structured(t.Context(), chargedRequest()); err == nil {
		t.Fatal("Structured() error = nil, want the schema failure")
	}
	if got := len(rec.records()); got != 1+llm.DefaultReprompts {
		t.Errorf("RecordUsage calls = %d, want %d (every response of the failed call)", got, 1+llm.DefaultReprompts)
	}
	if diff := cmp.Diff([]int{0, 1, 2}, seen); diff != "" {
		t.Errorf("records seen at each request mismatch (-want +got):\n%s", diff)
	}
}

func TestRecorder_notCalledWithoutAnAnswer(t *testing.T) {
	t.Parallel()
	rec := &fakeRecorder{}
	ad := &fakeAdapter{reply: func(int, context.Context, llm.Attempt) (llm.AttemptResult, error) {
		return llm.AttemptResult{}, statusErr(401)
	}}
	c, _ := newClient(t, ad, newKeys("OPENAI_API_KEY", openAIKey), withRecorder(rec))
	if _, err := c.Structured(t.Context(), chargedRequest()); err == nil {
		t.Fatal("Structured() error = nil, want the 401")
	}
	if got := rec.records(); len(got) != 0 {
		t.Errorf("RecordUsage calls = %+v, want none for a request that got no answer", got)
	}
}

func TestRecorder_notCalledWhenTheCallStopsBeforeARequest(t *testing.T) {
	t.Parallel()
	rec := &fakeRecorder{}
	ad := &fakeAdapter{reply: answer(validAnswer)}
	c, _ := newClient(t, ad, newKeys(), withRecorder(rec)) // no key stored
	if _, err := c.Structured(t.Context(), chargedRequest()); err == nil {
		t.Fatal("Structured() error = nil, want the missing key")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	c2, _ := newClient(t, ad, newKeys("OPENAI_API_KEY", openAIKey), withRecorder(rec))
	if _, err := c2.Structured(ctx, chargedRequest()); err == nil {
		t.Fatal("Structured() error = nil, want the cancellation")
	}
	if got := rec.records(); len(got) != 0 {
		t.Errorf("RecordUsage calls = %+v, want none", got)
	}
}

// A 200 the adapter cannot use (OpenRouter without choices) still cost tokens (EC5/EC6).
func TestRecorder_recordsAnAnsweredErrorWithEstimatedUsage(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		reply llm.AttemptResult
		want  llm.Usage
	}{
		{"provider usage", llm.AttemptResult{Answered: true, Usage: &llm.Usage{TokensIn: 10, TokensOut: 1}, CostUSD: costPtr(0.5)}, llm.Usage{TokensIn: 10, TokensOut: 1}},
		// "sys\nhello" is 9 UTF-16 units: 3 tokens in, none out.
		{"estimated", llm.AttemptResult{Answered: true}, llm.Usage{TokensIn: 3}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rec := &fakeRecorder{}
			ad := &fakeAdapter{reply: func(int, context.Context, llm.Attempt) (llm.AttemptResult, error) {
				return tc.reply, errors.New("returned no choices")
			}}
			c, _ := newClient(t, ad, newKeys("OPENAI_API_KEY", openAIKey), withRecorder(rec))
			if _, err := c.Structured(t.Context(), chargedRequest()); err == nil {
				t.Fatal("Structured() error = nil, want the adapter's failure")
			}
			got := rec.records()
			if len(got) != 1 {
				t.Fatalf("RecordUsage calls = %d, want 1", len(got))
			}
			if diff := cmp.Diff(tc.want, got[0].Tokens); diff != "" {
				t.Errorf("RecordUsage tokens mismatch (-want +got):\n%s", diff)
			}
			if tc.reply.CostUSD != nil && (got[0].CostUSD == nil || *got[0].CostUSD != *tc.reply.CostUSD) {
				t.Errorf("RecordUsage cost = %v, want the provider's %v", got[0].CostUSD, *tc.reply.CostUSD)
			}
		})
	}
}

// An answered attempt whose error is transient is retried; the answer it carried is not lost.
func TestRecorder_answeredAttemptIsNotLostToARetry(t *testing.T) {
	t.Parallel()
	rec := &fakeRecorder{}
	ad := &fakeAdapter{reply: func(n int, _ context.Context, _ llm.Attempt) (llm.AttemptResult, error) {
		if n == 1 {
			return llm.AttemptResult{Answered: true, Usage: &llm.Usage{TokensIn: 7, TokensOut: 3}}, statusErr(503)
		}
		return llm.AttemptResult{Raw: validAnswer, Usage: &llm.Usage{TokensIn: 20, TokensOut: 4}}, nil
	}}
	c, _ := newClient(t, ad, newKeys("OPENAI_API_KEY", openAIKey), withRecorder(rec))
	if _, err := c.Structured(t.Context(), chargedRequest()); err != nil {
		t.Fatalf("Structured() error = %v", err)
	}
	var tokens []llm.Usage
	for _, r := range rec.records() {
		tokens = append(tokens, r.Tokens)
	}
	want := []llm.Usage{{TokensIn: 7, TokensOut: 3}, {TokensIn: 20, TokensOut: 4}}
	if diff := cmp.Diff(want, tokens); diff != "" {
		t.Errorf("recorded tokens mismatch (-want +got):\n%s", diff)
	}
}

func TestRecorder_answeredErrorIsReportedOncePerRequest(t *testing.T) {
	t.Parallel()
	rec := &fakeRecorder{}
	ad := &fakeAdapter{reply: func(int, context.Context, llm.Attempt) (llm.AttemptResult, error) {
		return llm.AttemptResult{Answered: true, Usage: &llm.Usage{TokensIn: 1}}, errors.New("returned no choices")
	}}
	c, _ := newClient(t, ad, newKeys("OPENAI_API_KEY", openAIKey), withRecorder(rec))
	var observed int
	req := chargedRequest()
	req.OnUsage = func(llm.AttemptUsage) { observed++ }
	if _, err := c.Structured(t.Context(), req); err == nil {
		t.Fatal("Structured() error = nil, want the adapter's failure")
	}
	if got := len(rec.records()); got != 1 || observed != 1 {
		t.Errorf("recorder calls = %d, OnUsage calls = %d, want 1 and 1", got, observed)
	}
}

func TestRecorder_panicIsRecoveredAndTheCallProceeds(t *testing.T) {
	t.Parallel()
	rec := &fakeRecorder{onRecord: func(int) { panic("recorder bug") }}
	ad := &fakeAdapter{reply: answer(validAnswer)}
	c, logs := newClient(t, ad, newKeys("OPENAI_API_KEY", openAIKey), withRecorder(rec))
	observed := 0
	req := chargedRequest()
	req.OnUsage = func(llm.AttemptUsage) { observed++ }
	res, err := c.Structured(t.Context(), req)
	if err != nil || string(res.Data) != validAnswer {
		t.Fatalf("Structured() = %s, %v, want the answer despite the panicking recorder", res.Data, err)
	}
	if observed != 1 {
		t.Errorf("OnUsage calls = %d, want 1 (a recorder panic must not skip the observer)", observed)
	}
	if n := warnings(t, logs, "usage recorder panicked"); n != 1 {
		t.Errorf("recorder-panic warnings = %d, want 1", n)
	}
}

func TestRecorder_missingChargeWarnsOnceAndTheCallProceeds(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		charge *llm.Charge
	}{
		{"nil", nil},
		{"no workspace", &llm.Charge{Kind: llm.KindBrief, RefID: "r"}},
		{"unknown kind", &llm.Charge{WorkspaceID: "ws", Kind: "ci_run", RefID: "r"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rec := &fakeRecorder{}
			ad := &fakeAdapter{reply: func(n int, _ context.Context, _ llm.Attempt) (llm.AttemptResult, error) {
				if n == 1 {
					return llm.AttemptResult{Raw: `{"x":1}`, Usage: &llm.Usage{TokensIn: 1}}, nil
				}
				return llm.AttemptResult{Raw: validAnswer, Usage: &llm.Usage{TokensIn: 1}}, nil
			}}
			c, logs := newClient(t, ad, newKeys("OPENAI_API_KEY", openAIKey), withRecorder(rec))
			req := baseRequest()
			req.Charge = tc.charge
			res, err := c.Structured(t.Context(), req)
			if err != nil || string(res.Data) != validAnswer {
				t.Fatalf("Structured() = %s, %v, want the answer", res.Data, err)
			}
			if got := rec.records(); len(got) != 0 {
				t.Errorf("RecordUsage calls = %+v, want none without a usable charge", got)
			}
			if n := warnings(t, logs, "no usable spend charge"); n != 1 {
				t.Errorf("missing-charge warnings over a 2-attempt call = %d, want 1", n)
			}
		})
	}
}

func TestRecorder_offWithoutARecorder(t *testing.T) {
	t.Parallel()
	ad := &fakeAdapter{reply: answer(validAnswer)}
	c, logs := newClient(t, ad, newKeys("OPENAI_API_KEY", openAIKey), nil)
	for _, req := range []llm.Request{baseRequest(), chargedRequest()} {
		if _, err := c.Structured(t.Context(), req); err != nil {
			t.Fatalf("Structured() error = %v", err)
		}
	}
	if n := warnings(t, logs, "spend charge"); n != 0 {
		t.Errorf("charge warnings without a recorder = %d, want 0", n)
	}
}

func TestUsageKind_valid(t *testing.T) {
	t.Parallel()
	for _, k := range []llm.UsageKind{llm.KindReviewRun, llm.KindReviewIntent, llm.KindIntentRefresh, llm.KindBrief, llm.KindOnboarding, llm.KindConventionsScan, llm.KindEvalCase} {
		if !k.Valid() {
			t.Errorf("UsageKind(%q).Valid() = false, want true", k)
		}
	}
	for _, k := range []llm.UsageKind{"", "ci_run", "review"} {
		if k.Valid() {
			t.Errorf("UsageKind(%q).Valid() = true, want false", k)
		}
	}
}

func costPtr(v float64) *float64 { return &v }

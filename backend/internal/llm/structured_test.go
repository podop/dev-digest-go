package llm_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/llm"
	"dev-digest-go/backend/internal/platform/apperr"
)

const openAIKey = "sk-test-openai-key-123"

func TestStructured_success(t *testing.T) {
	t.Parallel()
	ad := &fakeAdapter{reply: answer(validAnswer)}
	c, _ := newClient(t, ad, newKeys("OPENAI_API_KEY", openAIKey), nil)
	req := baseRequest()
	req.MaxTokens = 500
	res, err := c.Structured(t.Context(), req)
	if err != nil {
		t.Fatalf("Structured() error = %v", err)
	}
	if string(res.Data) != validAnswer || res.Model != "gpt-4.1" || res.Attempts != 1 || res.TokensIn != 100 || res.TokensOut != 50 || res.Raw != validAnswer {
		t.Errorf("Structured() = %+v, want data %s, model gpt-4.1, 1 attempt, 100/50 tokens", res, validAnswer)
	}
	// (100 * 2.0 + 50 * 8.0) / 1e6 from the static table for gpt-4.1.
	if res.CostUSD == nil || *res.CostUSD != 0.0006 {
		t.Errorf("Structured() cost = %v, want 0.0006", res.CostUSD)
	}
	if got := ad.keys; len(got) != 1 || got[0] != openAIKey {
		t.Errorf("adapter keys = %v, want the stored key once", got)
	}
	sent := ad.calls[0]
	if sent.MaxTokens != 500 || sent.Model != "gpt-4.1" || sent.SchemaName != "Test" || !strings.Contains(string(sent.Schema), `"additionalProperties":false`) {
		t.Errorf("attempt = %+v, want max 500, model, schema name and a strict schema", sent)
	}
}

func TestStructured_fencedAnswer(t *testing.T) {
	t.Parallel()
	ad := &fakeAdapter{reply: answer("```json\n" + validAnswer + "\n```")}
	c, _ := newClient(t, ad, newKeys("OPENAI_API_KEY", openAIKey), nil)
	res, err := c.Structured(t.Context(), baseRequest())
	if err != nil || res.Attempts != 1 || string(res.Data) != validAnswer {
		t.Fatalf("Structured() = %+v, %v; want success on attempt 1", res, err)
	}
}

func TestStructured_repromptNotJSON(t *testing.T) {
	t.Parallel()
	ad := &fakeAdapter{reply: func(n int, _ context.Context, _ llm.Attempt) (llm.AttemptResult, error) {
		raw := "not json"
		if n > 1 {
			raw = validAnswer
		}
		return llm.AttemptResult{Raw: raw, Usage: &llm.Usage{TokensIn: 10, TokensOut: 5}}, nil
	}}
	c, _ := newClient(t, ad, newKeys("OPENAI_API_KEY", openAIKey), nil)
	res, err := c.Structured(t.Context(), baseRequest())
	if err != nil {
		t.Fatalf("Structured() error = %v", err)
	}
	if res.Attempts != 2 || res.TokensIn != 20 || res.TokensOut != 10 {
		t.Errorf("Structured() = %+v, want 2 attempts and summed tokens 20/10", res)
	}
	msgs := ad.calls[1].Messages
	if len(msgs) != 4 || msgs[2] != (llm.Message{Role: llm.Assistant, Content: "not json"}) {
		t.Fatalf("second attempt messages = %+v, want the original two plus the assistant output and a reprompt", msgs)
	}
	if last := msgs[3]; last.Role != llm.User || !strings.HasPrefix(last.Content, "Output was not valid JSON: ") ||
		!strings.HasSuffix(last.Content, "\nReturn ONLY a single valid JSON object matching the schema, no prose.") {
		t.Errorf("reprompt = %+v, want the not-JSON text", last)
	}
	if len(ad.calls[0].Messages) != 2 {
		t.Errorf("first attempt messages = %d, want 2 (the caller's slice must not be shared)", len(ad.calls[0].Messages))
	}
}

func TestStructured_repromptSchemaMismatch(t *testing.T) {
	t.Parallel()
	ad := &fakeAdapter{reply: answer(`{"x":1}`)}
	c, _ := newClient(t, ad, newKeys("OPENAI_API_KEY", openAIKey), nil)
	req := baseRequest()
	req.Reprompts = 1
	_, err := c.Structured(t.Context(), req)
	var ae *apperr.Error
	if !errors.As(err, &ae) || ae.Kind != apperr.KindExternalService {
		t.Fatalf("Structured() error = %v, want an external_service error", err)
	}
	want := "OpenAI structured output failed schema validation for Test after 2 attempt(s). Last issues:\n- candidates: Required\nLast raw output (7 chars): {\"x\":1}"
	if ae.Message != want {
		t.Errorf("Structured() message = %q, want %q", ae.Message, want)
	}
	if got := ad.callCount(); got != 2 {
		t.Errorf("adapter calls = %d, want 2", got)
	}
	re := ad.calls[1].Messages[3].Content
	if want := "Your JSON did not match the required schema. Fix these and return ONLY valid JSON:\n- candidates: Required"; re != want {
		t.Errorf("reprompt = %q, want %q", re, want)
	}
}

func TestStructured_noReprompts(t *testing.T) {
	t.Parallel()
	ad := &fakeAdapter{reply: answer(`{}`)}
	c, _ := newClient(t, ad, newKeys("OPENAI_API_KEY", openAIKey), nil)
	req := baseRequest()
	req.Reprompts = llm.NoReprompts
	if _, err := c.Structured(t.Context(), req); err == nil || !strings.Contains(err.Error(), "after 1 attempt(s)") {
		t.Fatalf("Structured() error = %v, want failure after 1 attempt", err)
	}
	if got := ad.callCount(); got != 1 {
		t.Errorf("adapter calls = %d, want 1", got)
	}
}

func TestStructured_failureTextIsCut(t *testing.T) {
	t.Parallel()
	ad := &fakeAdapter{reply: answer(`"` + strings.Repeat("x", 1000) + `"`)}
	c, _ := newClient(t, ad, newKeys("OPENAI_API_KEY", openAIKey), nil)
	req := baseRequest()
	req.Reprompts = llm.NoReprompts
	_, err := c.Structured(t.Context(), req)
	var ae *apperr.Error
	if !errors.As(err, &ae) {
		t.Fatalf("Structured() error = %v, want apperr", err)
	}
	if !strings.Contains(ae.Message, "Last raw output (1002 chars): \""+strings.Repeat("x", 399)+"…") {
		t.Errorf("message = %q, want the raw output cut at 400 chars", ae.Message)
	}
}

func TestStructured_missingKeyThenSaved(t *testing.T) {
	t.Parallel()
	ad := &fakeAdapter{reply: answer(validAnswer)}
	keys := newKeys()
	c, _ := newClient(t, ad, keys, nil)
	_, err := c.Structured(t.Context(), baseRequest())
	var ae *apperr.Error
	if !errors.As(err, &ae) || ae.Kind != apperr.KindConfig || ae.Message != "OPENAI_API_KEY is not configured" {
		t.Fatalf("Structured() error = %v, want config OPENAI_API_KEY is not configured", err)
	}
	if got := ad.callCount(); got != 0 {
		t.Errorf("adapter calls = %d, want 0", got)
	}
	keys.Set("OPENAI_API_KEY", openAIKey)
	if _, err := c.Structured(t.Context(), baseRequest()); err != nil {
		t.Fatalf("Structured() after saving the key: %v", err)
	}
	if ad.keys[0] != openAIKey {
		t.Errorf("key sent = %q, want the freshly stored key", ad.keys[0])
	}
}

func TestStructured_missingKeyNames(t *testing.T) {
	t.Parallel()
	for provider, want := range map[llm.Provider]string{
		llm.Anthropic:  "ANTHROPIC_API_KEY is not configured",
		llm.OpenRouter: "OPENROUTER_API_KEY is not configured",
	} {
		c, _ := newClient(t, &fakeAdapter{reply: answer(validAnswer)}, newKeys(), nil)
		req := baseRequest()
		req.Provider = provider
		if _, err := c.Structured(t.Context(), req); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("Structured(%s) error = %v, want %q", provider, err, want)
		}
	}
}

func TestStructured_usageEstimated(t *testing.T) {
	t.Parallel()
	out := `{"candidates":[]}` + strings.Repeat(" ", 63) // 80 chars
	ad := &fakeAdapter{reply: func(int, context.Context, llm.Attempt) (llm.AttemptResult, error) {
		return llm.AttemptResult{Raw: out}, nil
	}}
	c, logs := newClient(t, ad, newKeys("OPENAI_API_KEY", openAIKey), nil)
	req := baseRequest()
	req.Messages = []llm.Message{{Role: llm.User, Content: strings.Repeat("a", 400)}}
	res, err := c.Structured(t.Context(), req)
	if err != nil {
		t.Fatalf("Structured() error = %v", err)
	}
	if res.TokensIn != 100 || res.TokensOut != 20 {
		t.Errorf("tokens = %d/%d, want 100/20", res.TokensIn, res.TokensOut)
	}
	warns := 0
	for _, r := range logs.records(t) {
		if r["level"] == "WARN" {
			warns++
			if msg, _ := r["msg"].(string); !strings.Contains(msg, "returned no usage; estimated 100 in / 20 out tokens") {
				t.Errorf("warning = %q, want the estimate text", msg)
			}
		}
	}
	if warns != 1 {
		t.Errorf("warning lines = %d, want 1", warns)
	}
}

type fixedPricer struct{ cost map[string]float64 }

func (p fixedPricer) Estimate(model string, _, _ int) *float64 {
	if c, ok := p.cost[model]; ok {
		return &c
	}
	return nil
}

func TestStructured_costAcrossAttempts(t *testing.T) {
	t.Parallel()
	f := func(v float64) *float64 { return &v }
	tests := []struct {
		name     string
		provider llm.Provider
		model    string
		costs    []*float64 // reported per attempt
		want     *float64
	}{
		{"openrouter unknown then reported", llm.OpenRouter, "unknown-model", []*float64{nil, f(0.01)}, nil},
		{"openrouter reported sum", llm.OpenRouter, "unknown-model", []*float64{f(0.01), f(0.02)}, f(0.03)},
		{"openrouter falls back to price book per attempt", llm.OpenRouter, "known", []*float64{nil, f(0.5)}, f(0.75)},
		{"openai prices summed tokens once", llm.OpenAI, "known", []*float64{nil, nil}, f(0.25)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ad := &fakeAdapter{reply: func(n int, _ context.Context, _ llm.Attempt) (llm.AttemptResult, error) {
				raw := `{"x":1}`
				if n == len(tc.costs) {
					raw = validAnswer
				}
				return llm.AttemptResult{Raw: raw, Usage: &llm.Usage{TokensIn: 1, TokensOut: 1}, CostUSD: tc.costs[n-1]}, nil
			}}
			c, _ := newClient(t, ad, newKeys("OPENAI_API_KEY", openAIKey, "OPENROUTER_API_KEY", "or-key"), func(o *llm.Options) {
				o.Prices = fixedPricer{cost: map[string]float64{"known": 0.25}}
			})
			req := baseRequest()
			req.Provider, req.Model = tc.provider, tc.model
			res, err := c.Structured(t.Context(), req)
			if err != nil {
				t.Fatalf("Structured() error = %v", err)
			}
			if diff := cmp.Diff(tc.want, res.CostUSD); diff != "" {
				t.Errorf("Structured() cost mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestStructured_retryTransient(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		ad := &fakeAdapter{reply: func(n int, _ context.Context, _ llm.Attempt) (llm.AttemptResult, error) {
			if n <= 2 {
				return llm.AttemptResult{}, statusErr(503)
			}
			return llm.AttemptResult{Raw: validAnswer}, nil
		}}
		c, _ := newClient(t, ad, newKeys("OPENAI_API_KEY", openAIKey), nil)
		if _, err := c.Structured(t.Context(), baseRequest()); err != nil {
			t.Fatalf("Structured() error = %v", err)
		}
		if got := ad.callCount(); got != 3 {
			t.Errorf("adapter calls = %d, want 3 (two 503s, then success)", got)
		}
	})
}

func TestStructured_noRetryOn401(t *testing.T) {
	t.Parallel()
	ad := &fakeAdapter{reply: func(int, context.Context, llm.Attempt) (llm.AttemptResult, error) {
		return llm.AttemptResult{}, statusErr(401)
	}}
	c, _ := newClient(t, ad, newKeys("OPENAI_API_KEY", openAIKey), nil)
	_, err := c.Structured(t.Context(), baseRequest())
	var ae *apperr.Error
	if !errors.As(err, &ae) || ae.Kind != apperr.KindExternalService {
		t.Fatalf("Structured() error = %v, want external_service", err)
	}
	if got := ad.callCount(); got != 1 {
		t.Errorf("adapter calls = %d, want 1", got)
	}
}

func TestStructured_requestTimeoutNotRetried(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		ad := &fakeAdapter{reply: func(_ int, ctx context.Context, _ llm.Attempt) (llm.AttemptResult, error) {
			<-ctx.Done()
			return llm.AttemptResult{}, ctx.Err()
		}}
		c, _ := newClient(t, ad, newKeys("OPENAI_API_KEY", openAIKey), nil)
		req := baseRequest()
		req.Timeout = time.Second
		start := time.Now()
		_, err := c.Structured(t.Context(), req)
		if err == nil || !strings.Contains(err.Error(), "OpenAI request for Test timed out after 1000ms") {
			t.Fatalf("Structured() error = %v, want the request-timeout text", err)
		}
		if errors.Is(err, context.DeadlineExceeded) {
			t.Error("request timeout wraps context.DeadlineExceeded, want a plain error (the job runner reads the context for its own timeout)")
		}
		if got := ad.callCount(); got != 1 || time.Since(start) != time.Second {
			t.Errorf("calls = %d after %v, want 1 after 1s", got, time.Since(start))
		}
	})
}

func TestStructured_totalBudget(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		ad := &fakeAdapter{reply: func(int, context.Context, llm.Attempt) (llm.AttemptResult, error) {
			return llm.AttemptResult{}, statusErr(503)
		}}
		c, _ := newClient(t, ad, newKeys("OPENAI_API_KEY", openAIKey), func(o *llm.Options) {
			o.TotalBudget = 2 * time.Second
			o.Retry.BaseDelay, o.Retry.MaxDelay = time.Second, 8*time.Second
		})
		start := time.Now()
		_, err := c.Structured(t.Context(), baseRequest())
		if err == nil || !strings.Contains(err.Error(), "OpenAI structured call for Test exceeded its total budget of 2000ms") {
			t.Fatalf("Structured() error = %v, want the budget text", err)
		}
		if elapsed := time.Since(start); elapsed > 3*time.Second {
			t.Errorf("Structured() took %v, want within 3s", elapsed)
		}
	})
}

func TestStructured_budgetCutsHungRequest(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		ad := &fakeAdapter{reply: func(_ int, ctx context.Context, _ llm.Attempt) (llm.AttemptResult, error) {
			<-ctx.Done()
			return llm.AttemptResult{}, ctx.Err()
		}}
		c, _ := newClient(t, ad, newKeys("OPENAI_API_KEY", openAIKey), func(o *llm.Options) { o.TotalBudget = 5 * time.Second })
		_, err := c.Structured(t.Context(), baseRequest())
		if err == nil || !strings.Contains(err.Error(), "exceeded its total budget of 5000ms") {
			t.Fatalf("Structured() error = %v, want the budget text", err)
		}
	})
}

func TestStructured_cancelled(t *testing.T) {
	t.Parallel()
	t.Run("before the call", func(t *testing.T) {
		ad := &fakeAdapter{reply: answer(validAnswer)}
		c, _ := newClient(t, ad, newKeys("OPENAI_API_KEY", openAIKey), nil)
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		_, err := c.Structured(ctx, baseRequest())
		if !errors.Is(err, context.Canceled) {
			t.Errorf("Structured() error = %v, want context.Canceled", err)
		}
		if got := ad.callCount(); got != 0 {
			t.Errorf("adapter calls = %d, want 0", got)
		}
	})
	t.Run("while waiting for a retry", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			ad := &fakeAdapter{reply: func(int, context.Context, llm.Attempt) (llm.AttemptResult, error) {
				return llm.AttemptResult{}, statusErr(503)
			}}
			c, _ := newClient(t, ad, newKeys("OPENAI_API_KEY", openAIKey), func(o *llm.Options) { o.Retry.BaseDelay = time.Minute })
			ctx, cancel := context.WithCancel(t.Context())
			done := make(chan error, 1)
			go func() { _, err := c.Structured(ctx, baseRequest()); done <- err }()
			synctest.Wait()
			cancel()
			synctest.Wait()
			err := <-done
			if !errors.Is(err, context.Canceled) {
				t.Errorf("Structured() error = %v, want context.Canceled", err)
			}
			if got := ad.callCount(); got != 1 {
				t.Errorf("adapter calls = %d, want 1", got)
			}
		})
	})
	t.Run("caller deadline", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			ad := &fakeAdapter{reply: func(_ int, ctx context.Context, _ llm.Attempt) (llm.AttemptResult, error) {
				<-ctx.Done()
				return llm.AttemptResult{}, ctx.Err()
			}}
			c, _ := newClient(t, ad, newKeys("OPENAI_API_KEY", openAIKey), nil)
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			_, err := c.Structured(ctx, baseRequest())
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Errorf("Structured() error = %v, want the caller's context.DeadlineExceeded", err)
			}
		})
	})
}

func TestStructured_keysNeverLeak(t *testing.T) {
	t.Parallel()
	const orKey = "sk-or-secret-456"
	ad := &fakeAdapter{reply: func(int, context.Context, llm.Attempt) (llm.AttemptResult, error) {
		return llm.AttemptResult{}, errors.New("401 bad key " + openAIKey + " and " + orKey)
	}}
	c, logs := newClient(t, ad, newKeys("OPENAI_API_KEY", openAIKey, "OPENROUTER_API_KEY", orKey), nil)
	_, err := c.Structured(t.Context(), baseRequest())
	if err == nil || strings.Contains(err.Error(), openAIKey) || strings.Contains(err.Error(), orKey) || !strings.Contains(err.Error(), "[REDACTED]") {
		t.Errorf("Structured() error = %v, want both keys replaced by [REDACTED]", err)
	}
	for _, r := range logs.records(t) {
		if msg, _ := r["msg"].(string); strings.Contains(msg, openAIKey) {
			t.Errorf("log line %v contains the key", r)
		}
	}
	if got := c.Redact("a " + openAIKey + " b"); got != "a [REDACTED] b" {
		t.Errorf("Redact() = %q, want the key replaced", got)
	}
}

func TestStructured_promptLog(t *testing.T) {
	t.Parallel()
	var logged int
	var logs *logBuf
	ad := &fakeAdapter{}
	ad.reply = func(int, context.Context, llm.Attempt) (llm.AttemptResult, error) {
		logged = len(logs.records(t)) // lines written before the model call
		return llm.AttemptResult{}, statusErr(400)
	}
	var c *llm.Client
	c, logs = newClient(t, ad, newKeys("OPENAI_API_KEY", openAIKey), nil)
	items := 7
	req := baseRequest()
	req.Feature, req.CorrelationID = "conventions", "scan-1"
	req.Messages = []llm.Message{{Role: llm.System, Content: "SECRET PROMPT TEXT"}, {Role: llm.User, Content: "SECRET SAMPLE"}}
	req.Sections = []llm.PromptSection{
		{Name: "system", Source: "engine", Trust: llm.Trusted, Chars: 18},
		{Name: "task", Source: "engine", Trust: llm.Trusted, Chars: 5},
		{Name: "repository_sample", Source: "repo", Trust: llm.Untrusted, Chars: 13, Items: &items},
	}
	if _, err := c.Structured(t.Context(), req); err == nil {
		t.Fatal("Structured() error = nil, want the scripted 400")
	}
	if logged != 1 {
		t.Errorf("lines before the model call = %d, want 1 (the log is written even though the call fails)", logged)
	}
	var lines []map[string]any
	for _, r := range logs.records(t) {
		if r["msg"] == "prompt_assembled" {
			lines = append(lines, r)
		}
	}
	if len(lines) != 1 {
		t.Fatalf("prompt_assembled lines = %d, want 1", len(lines))
	}
	got := lines[0]
	if got["evt"] != "prompt_assembled" || got["level"] != "INFO" || got["feature"] != "conventions" || got["correlationId"] != "scan-1" ||
		got["provider"] != "openai" || got["model"] != "gpt-4.1" || got["totalChars"] != float64(36) || got["totalTokens"] != float64(5+2+4) {
		t.Errorf("prompt_assembled = %v, want conventions/scan-1/openai/gpt-4.1, 36 chars, 11 tokens", got)
	}
	wantSections := []any{
		map[string]any{"name": "system", "source": "engine", "role": "system", "trust": "trusted", "chars": float64(18), "tokens": float64(5)},
		map[string]any{"name": "task", "source": "engine", "role": "user", "trust": "trusted", "chars": float64(5), "tokens": float64(2)},
		map[string]any{"name": "repository_sample", "source": "repo", "role": "user", "trust": "untrusted", "chars": float64(13), "tokens": float64(4), "items": float64(7)},
	}
	if diff := cmp.Diff(wantSections, got["sections"]); diff != "" {
		t.Errorf("sections mismatch (-want +got):\n%s", diff)
	}
}

func TestStructured_defaultSections(t *testing.T) {
	t.Parallel()
	ad := &fakeAdapter{reply: answer(validAnswer)}
	c, logs := newClient(t, ad, newKeys("OPENAI_API_KEY", openAIKey), nil)
	if _, err := c.Structured(t.Context(), baseRequest()); err != nil {
		t.Fatal(err)
	}
	rec := logs.records(t)[0]
	secs, _ := rec["sections"].([]any)
	if len(secs) != 2 || rec["totalChars"] != float64(8) {
		t.Errorf("prompt_assembled = %v, want one section per message and 8 chars", rec)
	}
}

func TestStructured_concurrent(t *testing.T) {
	t.Parallel()
	ad := &fakeAdapter{reply: answer(validAnswer)}
	c, _ := newClient(t, ad, newKeys("OPENAI_API_KEY", openAIKey), nil)
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			if _, err := c.Structured(t.Context(), baseRequest()); err != nil {
				t.Errorf("Structured() error = %v", err)
			}
		})
	}
	wg.Wait()
}

func TestStructured_invalidRequests(t *testing.T) {
	t.Parallel()
	c, _ := newClient(t, &fakeAdapter{reply: answer(validAnswer)}, newKeys("OPENAI_API_KEY", openAIKey), nil)
	tests := []struct {
		name string
		mod  func(*llm.Request)
	}{
		{"unknown provider", func(r *llm.Request) { r.Provider = "bad" }},
		{"no model", func(r *llm.Request) { r.Model = "" }},
		{"no messages", func(r *llm.Request) { r.Messages = nil }},
		{"bad schema", func(r *llm.Request) { r.Schema = []byte(`[`) }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := baseRequest()
			tc.mod(&req)
			if _, err := c.Structured(t.Context(), req); err == nil {
				t.Error("Structured() error = nil, want an error")
			}
		})
	}
}

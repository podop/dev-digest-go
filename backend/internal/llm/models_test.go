package llm_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/llm"
	"dev-digest-go/backend/internal/platform/apperr"
)

func TestModelInfo_JSON(t *testing.T) {
	t.Parallel()
	s := func(v string) *string { return &v }
	n := func(v int64) *int64 { return &v }
	i := func(v int) *int { return &v }
	tests := []struct {
		name string
		in   llm.ModelInfo
		want string
	}{
		{"openai", llm.ModelInfo{ID: "gpt-4.1", Provider: llm.OpenAI, Created: n(7)}, `{"id":"gpt-4.1","provider":"openai","created":7}`},
		{"openai without created", llm.ModelInfo{ID: "gpt-4.1", Provider: llm.OpenAI}, `{"id":"gpt-4.1","provider":"openai"}`},
		{"anthropic", llm.ModelInfo{ID: "c", Provider: llm.Anthropic, Label: s("Claude")}, `{"id":"c","provider":"anthropic","label":"Claude"}`},
		{"openrouter full", llm.ModelInfo{ID: "a", Provider: llm.OpenRouter, Label: s("A"), ContextLength: i(8), Pricing: &llm.ModelPricing{PromptPerM: 1, CompletionPerM: 3}},
			`{"id":"a","provider":"openrouter","label":"A","pricing":{"promptPerM":1,"completionPerM":3},"contextLength":8}`},
		{"openrouter nulls", llm.ModelInfo{ID: "b", Provider: llm.OpenRouter}, `{"id":"b","provider":"openrouter","label":null,"pricing":null,"contextLength":null}`},
		{"mock keeps the requested provider", llm.ModelInfo{ID: "mock/reviewer", Provider: llm.OpenRouter, Label: s("L"), Mock: true}, `{"id":"mock/reviewer","provider":"openrouter","label":"L"}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := json.Marshal(tc.in)
			if err != nil || string(got) != tc.want {
				t.Errorf("Marshal(%+v) = %s, %v; want %s", tc.in, got, err, tc.want)
			}
		})
	}
}

func TestListModels_missingKey(t *testing.T) {
	t.Parallel()
	ad := &fakeAdapter{list: func(context.Context, string) ([]llm.ModelInfo, error) { return nil, nil }}
	c, _ := newClient(t, ad, newKeys(), nil)
	_, err := c.ListModels(t.Context(), llm.OpenRouter)
	var ae *apperr.Error
	if !errors.As(err, &ae) || ae.Kind != apperr.KindConfig || ae.Message != "OPENROUTER_API_KEY is not configured" {
		t.Errorf("ListModels() error = %v, want config OPENROUTER_API_KEY is not configured", err)
	}
	if len(ad.keys) != 0 {
		t.Errorf("adapter was called %d times, want 0", len(ad.keys))
	}
}

func TestListModels_retriesAndPolicy(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		calls := 0
		ad := &fakeAdapter{list: func(context.Context, string) ([]llm.ModelInfo, error) {
			calls++
			if calls < 3 {
				return nil, statusErr(500)
			}
			return []llm.ModelInfo{{ID: "m", Provider: llm.OpenAI}}, nil
		}}
		c, _ := newClient(t, ad, newKeys("OPENAI_API_KEY", openAIKey), nil)
		got, err := c.ListModels(t.Context(), llm.OpenAI)
		if err != nil || len(got) != 1 || calls != 3 {
			t.Errorf("ListModels() = %v, %v after %d calls; want 1 model after 3 calls", got, err, calls)
		}
	})
}

func TestListModels_timeoutIsOneRequest(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		calls := 0
		ad := &fakeAdapter{
			policy: llm.Policy{RequestTimeout: time.Minute, ListTimeout: 15 * time.Second},
			list: func(ctx context.Context, _ string) ([]llm.ModelInfo, error) {
				calls++
				<-ctx.Done()
				return nil, ctx.Err()
			},
		}
		c, _ := newClient(t, ad, newKeys("OPENROUTER_API_KEY", "k"), nil)
		start := time.Now()
		_, err := c.ListModels(t.Context(), llm.OpenRouter)
		if err == nil || !strings.Contains(err.Error(), "model list timed out after 15s") {
			t.Fatalf("ListModels() error = %v, want the timeout text", err)
		}
		if calls != 1 || time.Since(start) != 15*time.Second {
			t.Errorf("calls = %d after %v, want 1 after 15s", calls, time.Since(start))
		}
	})
}

func TestListModels_errorsAreScrubbed(t *testing.T) {
	t.Parallel()
	ad := &fakeAdapter{list: func(_ context.Context, key string) ([]llm.ModelInfo, error) {
		return nil, errors.New("rejected " + key)
	}}
	c, _ := newClient(t, ad, newKeys("OPENAI_API_KEY", openAIKey), nil)
	_, err := c.ListModels(t.Context(), llm.OpenAI)
	if err == nil || strings.Contains(err.Error(), openAIKey) {
		t.Errorf("ListModels() error = %v, want the key scrubbed", err)
	}
	if diff := cmp.Diff("rejected [REDACTED]", err.Error()); diff != "" {
		t.Errorf("ListModels() error mismatch (-want +got):\n%s", diff)
	}
}

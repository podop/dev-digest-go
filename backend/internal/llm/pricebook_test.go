package llm_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"dev-digest-go/backend/internal/llm"
)

func TestStaticCost(t *testing.T) {
	t.Parallel()
	tests := []struct {
		model    string
		in, out  int
		want     float64
		wantNull bool
	}{
		{"gpt-5.4", 1_000_000, 100_000, 4.0, false},
		{"claude-haiku-4-5", 2000, 1000, 0.007, false},
		{"unknown-model", 10, 10, 0, true},
	}
	for _, tc := range tests {
		got := llm.StaticCost(tc.model, tc.in, tc.out)
		if tc.wantNull != (got == nil) || (got != nil && *got != tc.want) {
			t.Errorf("StaticCost(%s, %d, %d) = %v, want %v (null=%v)", tc.model, tc.in, tc.out, got, tc.want, tc.wantNull)
		}
	}
}

func liveList(calls *atomic.Int32, err *atomic.Bool) func(context.Context) ([]llm.ModelInfo, error) {
	return func(context.Context) ([]llm.ModelInfo, error) {
		calls.Add(1)
		if err.Load() {
			return nil, errors.New("down")
		}
		return []llm.ModelInfo{
			{ID: "vendor/m", Provider: llm.OpenRouter, Pricing: &llm.ModelPricing{PromptPerM: 1, CompletionPerM: 2}},
			{ID: "vendor/free-unknown", Provider: llm.OpenRouter},
		}, nil
	}
}

func TestPriceBook_liveAfterBackgroundFetch(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		var calls atomic.Int32
		var fail atomic.Bool
		b := llm.NewPriceBook(llm.PriceBookOptions{List: liveList(&calls, &fail)})
		defer b.Close()

		if got := b.Estimate("vendor/m", 1_000_000, 1_000_000); got != nil {
			t.Errorf("first Estimate() = %v, want nil (cold cache answers from the static table)", *got)
		}
		synctest.Wait()
		got := b.Estimate("vendor/m", 1_000_000, 1_000_000)
		if got == nil || *got != 3 {
			t.Errorf("second Estimate() = %v, want 3 (live 1 + 2 per 1M)", got)
		}
		time.Sleep(5 * time.Hour)
		b.Estimate("vendor/m", 1, 1)
		synctest.Wait()
		if n := calls.Load(); n != 1 {
			t.Errorf("list calls within 6h = %d, want 1", n)
		}
		if got := b.Estimate("gpt-5.4", 1_000_000, 0); got == nil || *got != 2.5 {
			t.Errorf("Estimate(gpt-5.4) = %v, want the static 2.5", got)
		}
		time.Sleep(2 * time.Hour)
		b.Estimate("vendor/m", 1, 1)
		synctest.Wait()
		if n := calls.Load(); n != 2 {
			t.Errorf("list calls after the TTL = %d, want 2", n)
		}
	})
}

func TestPriceBook_failedFetchRetriedOnNextLookup(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		var calls atomic.Int32
		var fail atomic.Bool
		fail.Store(true)
		b := llm.NewPriceBook(llm.PriceBookOptions{List: liveList(&calls, &fail)})
		defer b.Close()

		b.Estimate("gpt-4.1", 1, 1)
		synctest.Wait()
		b.Estimate("gpt-4.1", 1, 1)
		synctest.Wait()
		if n := calls.Load(); n != 2 {
			t.Errorf("list calls after two lookups with a failing list = %d, want 2", n)
		}
		fail.Store(false)
		b.Estimate("gpt-4.1", 1, 1)
		synctest.Wait()
		if got := b.Estimate("vendor/m", 1_000_000, 0); got == nil || *got != 1 {
			t.Errorf("Estimate() after recovery = %v, want live 1", got)
		}
	})
}

func TestPriceBook_closeStopsFetch(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		started := make(chan struct{})
		b := llm.NewPriceBook(llm.PriceBookOptions{List: func(ctx context.Context) ([]llm.ModelInfo, error) {
			close(started)
			<-ctx.Done()
			return nil, ctx.Err()
		}})
		b.Estimate("gpt-4.1", 1, 1)
		<-started
		b.Close() // returns only when the fetch goroutine has ended
		b.Close()
		b.Estimate("gpt-4.1", 1, 1) // no new fetch after Close
	})
}

func TestPriceBook_refresh(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	var fail atomic.Bool
	b := llm.NewPriceBook(llm.PriceBookOptions{List: liveList(&calls, &fail)})
	defer b.Close()
	if err := b.Refresh(t.Context()); err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}
	if got := b.Estimate("vendor/m", 1_000_000, 0); got == nil || *got != 1 {
		t.Errorf("Estimate() after Refresh = %v, want 1", got)
	}
	fail.Store(true)
	if err := b.Refresh(t.Context()); err == nil {
		t.Error("Refresh() error = nil, want the list error")
	}
}

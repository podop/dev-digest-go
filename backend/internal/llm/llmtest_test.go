package llm_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"dev-digest-go/backend/internal/llm"
	"dev-digest-go/backend/internal/platform/retry"
)

const testSchema = `{"type":"object","required":["candidates"],"properties":{"candidates":{"type":"array","items":{"type":"object","required":["rule"],"properties":{"rule":{"type":"string"}}}}}}`

const validAnswer = `{"candidates":[{"rule":"r"}]}`

// fakeKeys is a mutable in-memory secrets store.
type fakeKeys struct {
	mu sync.Mutex
	m  map[string]string
}

func newKeys(kv ...string) *fakeKeys {
	k := &fakeKeys{m: map[string]string{}}
	for i := 0; i+1 < len(kv); i += 2 {
		k.m[kv[i]] = kv[i+1]
	}
	return k
}

func (k *fakeKeys) Get(name string) string {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.m[name]
}

func (k *fakeKeys) Set(name, v string) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.m[name] = v
}

// statusErr is a provider failure carrying an HTTP status.
type statusErr int

func (e statusErr) Error() string   { return fmt.Sprintf("status %d", int(e)) }
func (e statusErr) StatusCode() int { return int(e) }

// fakeAdapter scripts the answers of a provider.
type fakeAdapter struct {
	mu     sync.Mutex
	calls  []llm.Attempt
	keys   []string
	policy llm.Policy
	// reply answers call number n (1-based).
	reply func(n int, ctx context.Context, a llm.Attempt) (llm.AttemptResult, error)
	list  func(ctx context.Context, key string) ([]llm.ModelInfo, error)
}

func (f *fakeAdapter) RequiresKey() bool { return true }

func (f *fakeAdapter) Policy() llm.Policy {
	if f.policy == (llm.Policy{}) {
		return llm.Policy{RequestTimeout: 120 * time.Second, Retries: 3, ListTimeout: 30 * time.Second, ListRetries: 3}
	}
	return f.policy
}

func (f *fakeAdapter) Complete(ctx context.Context, key string, a llm.Attempt) (llm.AttemptResult, error) {
	f.mu.Lock()
	f.calls = append(f.calls, a)
	f.keys = append(f.keys, key)
	n := len(f.calls)
	f.mu.Unlock()
	return f.reply(n, ctx, a)
}

func (f *fakeAdapter) ListModels(ctx context.Context, key string) ([]llm.ModelInfo, error) {
	f.mu.Lock()
	f.keys = append(f.keys, key)
	f.mu.Unlock()
	return f.list(ctx, key)
}

func (f *fakeAdapter) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func answer(raw string) func(int, context.Context, llm.Attempt) (llm.AttemptResult, error) {
	return func(int, context.Context, llm.Attempt) (llm.AttemptResult, error) {
		return llm.AttemptResult{Raw: raw, Usage: &llm.Usage{TokensIn: 100, TokensOut: 50}}, nil
	}
}

var noJitter = func(time.Duration) time.Duration { return 0 }

type logBuf struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *logBuf) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *logBuf) records(t *testing.T) []map[string]any {
	t.Helper()
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []map[string]any
	for line := range strings.SplitSeq(strings.TrimSpace(b.buf.String()), "\n") {
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("log line %q is not JSON: %v", line, err)
		}
		out = append(out, m)
	}
	return out
}

// newClient builds a Client over the fake with zero-jitter 1 ms backoff and static prices.
func newClient(t *testing.T, ad llm.Adapter, keys llm.KeyReader, mod func(*llm.Options)) (*llm.Client, *logBuf) {
	t.Helper()
	logs := &logBuf{}
	opts := llm.Options{
		Keys:     keys,
		Adapters: map[llm.Provider]llm.Adapter{llm.OpenAI: ad, llm.Anthropic: ad, llm.OpenRouter: ad},
		Logger:   slog.New(slog.NewJSONHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
		Prices:   llm.NewPriceBook(llm.PriceBookOptions{}),
		Retry:    retry.Options{BaseDelay: time.Millisecond, MaxDelay: 10 * time.Millisecond, Jitter: noJitter},
	}
	if mod != nil {
		mod(&opts)
	}
	c := llm.New(opts)
	t.Cleanup(c.Close)
	return c, logs
}

func baseRequest() llm.Request {
	return llm.Request{
		Provider:   llm.OpenAI,
		Model:      "gpt-4.1",
		Messages:   []llm.Message{{Role: llm.System, Content: "sys"}, {Role: llm.User, Content: "hello"}},
		SchemaName: "Test",
		Schema:     json.RawMessage(testSchema),
	}
}

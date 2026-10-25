package peers_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/llm"
	"dev-digest-go/backend/internal/llm/adapters/mock"
	"dev-digest-go/backend/internal/llm/adapters/openaicompat"
	"dev-digest-go/backend/internal/platform/retry"
	"dev-digest-go/backend/internal/reviewer/adapters/llmcall"
	reviewerapp "dev-digest-go/backend/internal/reviewer/app"
	reviewer "dev-digest-go/backend/internal/reviewer/domain"
	"dev-digest-go/backend/internal/reviews/adapters/peers"
	reviewsapp "dev-digest-go/backend/internal/reviews/app"
)

type keyMap map[string]string

func (k keyMap) Get(name string) string { return k[name] }

// testClient is what the tests hand to the providers: the client's readiness and redaction plus
// its structured call, which the production wiring (cmd/server) binds through llmcall.
type testClient interface {
	peers.LLMClient
	llmcall.Structured
}

func newProviders(c testClient) *peers.Providers {
	return peers.NewProviders(c, func(cc peers.CallerConfig) reviewerapp.LLM {
		return llmcall.New(llmcall.Config{
			Client: c, Provider: cc.Provider, Model: cc.Model, RunID: cc.RunID, SessionID: cc.SessionID, OnUsage: cc.OnUsage, Charge: cc.Charge,
		})
	})
}

func realClient(t *testing.T, ks keyMap, adapters map[llm.Provider]llm.Adapter) *llm.Client {
	t.Helper()
	c := llm.New(llm.Options{
		Keys: ks, Adapters: adapters, Logger: slog.New(slog.NewJSONHandler(io.Discard, nil)),
		Prices: llm.NewPriceBook(llm.PriceBookOptions{}),
		Retry:  retry.Options{BaseDelay: time.Millisecond, MaxDelay: 5 * time.Millisecond},
	})
	t.Cleanup(c.Close)
	return c
}

func TestProviders_MissingKeyFailsWithoutACall(t *testing.T) { // FR4038, EC4017
	// An adapter pointed at a closed port would fail differently if a call were made.
	ad := openaicompat.NewOpenRouter(openaicompat.Options{BaseURL: "http://127.0.0.1:1"})
	p := newProviders(realClient(t, keyMap{}, map[llm.Provider]llm.Adapter{llm.OpenRouter: ad}))
	port, err := p.Resolve(t.Context(), reviewsapp.LLMBinding{Provider: "openrouter", Model: "m", RunID: "r"})
	if port != nil || err == nil || err.Error() != "OPENROUTER_API_KEY is not configured" {
		t.Errorf("Resolve() = %v, %v, want nil and %q", port, err, "OPENROUTER_API_KEY is not configured")
	}
}

func TestProviders_UnknownProvider(t *testing.T) {
	p := newProviders(realClient(t, keyMap{}, nil))
	if _, err := p.Resolve(t.Context(), reviewsapp.LLMBinding{Provider: "nope"}); err == nil {
		t.Error("Resolve(unknown provider) error = nil, want an error")
	}
}

func TestProviders_MockOverrideNeedsNoKeyAndReportsUsage(t *testing.T) {
	ad := map[llm.Provider]llm.Adapter{llm.OpenRouter: mock.New(llm.OpenRouter, mock.Options{})}
	p := newProviders(realClient(t, keyMap{}, ad))
	var usages []reviewer.Usage
	port, err := p.Resolve(t.Context(), reviewsapp.LLMBinding{
		Provider: "openrouter", Model: "m", RunID: "r", SessionID: "o/n#1:A",
		OnUsage: func(u reviewer.Usage) { usages = append(usages, u) },
	})
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	res, err := port.Review(t.Context(), reviewerapp.Call{
		Messages: []reviewer.Message{{Role: reviewer.RoleSystem, Content: "s"}, {Role: reviewer.RoleUser, Content: "u"}},
		Chunk:    reviewer.ChunkRef{Total: 1},
	})
	if err != nil {
		t.Fatalf("Review() error = %v", err)
	}
	if len(res.Review.Findings) != 2 || len(usages) != 1 || usages[0].TokensIn != res.Usage.TokensIn {
		t.Errorf("Review() = %d findings, usage %+v, observer %+v", len(res.Review.Findings), res.Usage, usages)
	}
}

func TestProviders_ResolveWithoutOnUsage(t *testing.T) {
	ad := map[llm.Provider]llm.Adapter{llm.OpenAI: mock.New(llm.OpenAI, mock.Options{})}
	p := newProviders(realClient(t, keyMap{}, ad))
	port, err := p.Resolve(t.Context(), reviewsapp.LLMBinding{Provider: "openai", Model: "m", RunID: "r"})
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if _, err := port.Review(t.Context(), reviewerapp.Call{
		Messages: []reviewer.Message{{Role: reviewer.RoleUser, Content: "u"}}, Chunk: reviewer.ChunkRef{Total: 1},
	}); err != nil {
		t.Errorf("Review() error = %v", err)
	}
}

func TestProviders_RedactScrubsStoredKeys(t *testing.T) {
	p := newProviders(realClient(t, keyMap{"OPENAI_API_KEY": "sk-secret-123"}, nil))
	got := p.Redact("boom: Bearer sk-secret-123 rejected")
	if strings.Contains(got, "sk-secret-123") || !strings.Contains(got, "[REDACTED]") {
		t.Errorf("Redact() = %q, want the key replaced by [REDACTED]", got)
	}
}

// fakeLLMClient shows Resolve binds the run's identity into the request.
type fakeLLMClient struct {
	readyErr error
	req      llm.Request
}

func (f *fakeLLMClient) Ready(llm.Provider) error { return f.readyErr }
func (f *fakeLLMClient) Redact(s string) string   { return "R(" + s + ")" }
func (f *fakeLLMClient) Structured(_ context.Context, r llm.Request) (llm.Result, error) {
	f.req = r
	return llm.Result{Data: json.RawMessage(`{"verdict":"approve","summary":"","score":100,"findings":[]}`)}, nil
}

func TestProviders_BindsTheRunIntoTheRequest(t *testing.T) {
	f := &fakeLLMClient{}
	p := newProviders(f)
	port, err := p.Resolve(t.Context(), reviewsapp.LLMBinding{Provider: "anthropic", Model: "claude-x", RunID: "run-7", SessionID: "o/n#2:Ag"})
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if _, err := port.Review(t.Context(), reviewerapp.Call{Chunk: reviewer.ChunkRef{Index: 1, Total: 4}}); err != nil {
		t.Fatalf("Review() error = %v", err)
	}
	r := f.req
	if r.Provider != llm.Anthropic || r.Model != "claude-x" || r.CorrelationID != "run-7" || r.SessionID != "o/n#2:Ag" || r.Chunk.Index != 1 || r.Chunk.Total != 4 {
		t.Errorf("request = %+v", r)
	}
	if got := p.Redact("x"); got != "R(x)" {
		t.Errorf("Redact() = %q, want it to go through the client", got)
	}

	f.readyErr = errors.New("ANTHROPIC_API_KEY is not configured")
	if _, err := p.Resolve(t.Context(), reviewsapp.LLMBinding{Provider: "anthropic"}); !errors.Is(err, f.readyErr) {
		t.Errorf("Resolve() error = %v, want the client's Ready error", err)
	}
}

func TestProviders_ChargesTheRunToItsWorkspace(t *testing.T) { // FR1
	f := &fakeLLMClient{}
	p := newProviders(f)
	port, err := p.Resolve(t.Context(), reviewsapp.LLMBinding{
		Provider: "anthropic", Model: "claude-x", WorkspaceID: "ws-1", RunID: "run-7", SessionID: "o/n#2:Ag",
	})
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	for range 2 { // every call of the run, chunks included, carries the same label
		f.req = llm.Request{}
		if _, err := port.Review(t.Context(), reviewerapp.Call{Chunk: reviewer.ChunkRef{Index: 1, Total: 2}}); err != nil {
			t.Fatalf("Review() error = %v", err)
		}
		want := &llm.Charge{WorkspaceID: "ws-1", Kind: llm.KindReviewRun, RefID: "run-7"}
		if diff := cmp.Diff(want, f.req.Charge); diff != "" {
			t.Errorf("Review() request charge mismatch (-want +got):\n%s", diff)
		}
	}
}

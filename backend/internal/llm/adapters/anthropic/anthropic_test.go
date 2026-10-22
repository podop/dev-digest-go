package anthropic_test

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"dev-digest-go/backend/internal/llm"
	"dev-digest-go/backend/internal/llm/adapters/anthropic"
	"dev-digest-go/backend/internal/platform/retry"
)

const (
	testKey = "sk-ant-secret-0002"
	schema  = `{"type":"object","required":["candidates"],"properties":{"candidates":{"type":"array","items":{"type":"object","required":["rule"],"properties":{"rule":{"type":"string"}}}}}}`
)

type keys map[string]string

func (k keys) Get(name string) string { return k[name] }

type recorder struct {
	mu      sync.Mutex
	paths   []string
	headers []http.Header
	bodies  []map[string]any
}

func (r *recorder) record(req *http.Request) {
	raw, _ := io.ReadAll(req.Body)
	var body map[string]any
	_ = json.Unmarshal(raw, &body)
	r.mu.Lock()
	defer r.mu.Unlock()
	r.paths = append(r.paths, req.Method+" "+req.URL.Path)
	r.headers = append(r.headers, req.Header.Clone())
	r.bodies = append(r.bodies, body)
}

func setup(t *testing.T, handle func(n int, w http.ResponseWriter)) (*llm.Client, *recorder) {
	t.Helper()
	rec := &recorder{}
	srv := httptest.NewTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.record(r)
		rec.mu.Lock()
		n := len(rec.paths)
		rec.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		handle(n, w)
	}))
	hc := srv.Client()
	ad := anthropic.New(anthropic.Options{BaseURL: srv.URL + "/v1", HTTPClient: hc})
	c := llm.New(llm.Options{
		Keys:     keys{"ANTHROPIC_API_KEY": testKey},
		Adapters: map[llm.Provider]llm.Adapter{llm.Anthropic: ad},
		Logger:   slog.New(slog.DiscardHandler),
		Prices:   llm.NewPriceBook(llm.PriceBookOptions{}),
		Retry:    retry.Options{BaseDelay: time.Millisecond, Jitter: func(time.Duration) time.Duration { return 0 }},
	})
	t.Cleanup(c.Close)
	return c, rec
}

func toolUse(input string) string {
	return `{"content":[{"type":"text","text":"ok"},{"type":"tool_use","id":"t1","name":"X","input":` + input + `}],"usage":{"input_tokens":20,"output_tokens":9}}`
}

func request() llm.Request {
	return llm.Request{
		Provider: llm.Anthropic,
		Model:    "claude-haiku-4-5",
		Messages: []llm.Message{
			{Role: llm.System, Content: "first"},
			{Role: llm.System, Content: "second"},
			{Role: llm.User, Content: "hi"},
		},
		SchemaName: "Convention.Extraction v1",
		Schema:     json.RawMessage(schema),
	}
}

func TestStructured_forcedToolUse(t *testing.T) {
	t.Parallel()
	c, rec := setup(t, func(_ int, w http.ResponseWriter) {
		_, _ = io.WriteString(w, toolUse(`{"candidates": [ {"rule":"r"} ]}`))
	})
	res, err := c.Structured(t.Context(), request())
	if err != nil {
		t.Fatalf("Structured() error = %v", err)
	}
	if res.Raw != `{"candidates":[{"rule":"r"}]}` || res.TokensIn != 20 || res.TokensOut != 9 || res.Attempts != 1 {
		t.Errorf("Structured() = %+v, want the compact tool input, 20/9 tokens, 1 attempt", res)
	}
	// (20 * 1.0 + 9 * 5.0) / 1e6 for claude-haiku-4-5.
	if res.CostUSD == nil || *res.CostUSD != 65.0/1_000_000 {
		t.Errorf("cost = %v, want the price-book cost", res.CostUSD)
	}
	if rec.paths[0] != "POST /v1/messages" || rec.headers[0].Get("x-api-key") != testKey || rec.headers[0].Get("anthropic-version") != "2023-06-01" || rec.headers[0].Get("Authorization") != "" {
		t.Errorf("request = %s %v, want POST /v1/messages with x-api-key and anthropic-version", rec.paths[0], rec.headers[0])
	}
	b := rec.bodies[0]
	tools, _ := b["tools"].([]any)
	tool, _ := tools[0].(map[string]any)
	choice, _ := b["tool_choice"].(map[string]any)
	if b["system"] != "first\n\nsecond" || b["max_tokens"] != float64(4096) || b["temperature"] != float64(0) ||
		tool["name"] != "Convention_Extraction_v1" || tool["description"] != "Return the result as Convention.Extraction v1." ||
		choice["type"] != "tool" || choice["name"] != "Convention_Extraction_v1" {
		t.Errorf("body = %v, want joined system, max_tokens 4096, temperature 0, sanitized forced tool", b)
	}
	if msgs, _ := b["messages"].([]any); len(msgs) != 1 {
		t.Errorf("messages = %v, want only the user message", msgs)
	}
	if sch, _ := json.Marshal(tool["input_schema"]); !strings.Contains(string(sch), `"additionalProperties":false`) {
		t.Errorf("input_schema = %s, want the strict schema", sch)
	}
}

func TestStructured_explicitTuning(t *testing.T) {
	t.Parallel()
	c, rec := setup(t, func(_ int, w http.ResponseWriter) { _, _ = io.WriteString(w, toolUse(`{"candidates":[]}`)) })
	req := request()
	req.MaxTokens, req.Temperature = 1000, new(0.4)
	if _, err := c.Structured(t.Context(), req); err != nil {
		t.Fatal(err)
	}
	if b := rec.bodies[0]; b["max_tokens"] != float64(1000) || b["temperature"] != 0.4 {
		t.Errorf("body = %v, want max_tokens 1000 and temperature 0.4", b)
	}
}

func TestStructured_noToolCallIsReprompted(t *testing.T) {
	t.Parallel()
	c, rec := setup(t, func(n int, w http.ResponseWriter) {
		if n == 1 {
			_, _ = io.WriteString(w, `{"content":[{"type":"text","text":"sorry"}],"usage":{"input_tokens":1,"output_tokens":1}}`)
			return
		}
		_, _ = io.WriteString(w, toolUse(`{"candidates":[]}`))
	})
	res, err := c.Structured(t.Context(), request())
	if err != nil || res.Attempts != 2 {
		t.Fatalf("Structured() = %+v, %v; want success on attempt 2", res, err)
	}
	msgs, _ := rec.bodies[1]["messages"].([]any)
	if len(msgs) != 3 {
		t.Fatalf("second request messages = %v, want user, assistant, user", msgs)
	}
	if first, _ := msgs[1].(map[string]any); first["role"] != "assistant" {
		t.Errorf("messages[1] = %v, want the assistant's raw output", first)
	}
}

func TestStructured_errors(t *testing.T) {
	t.Parallel()
	c, rec := setup(t, func(_ int, w http.ResponseWriter) {
		w.WriteHeader(401)
		_, _ = io.WriteString(w, `{"error":{"type":"authentication_error","message":"invalid x-api-key `+testKey+`"}}`)
	})
	_, err := c.Structured(t.Context(), request())
	if err == nil || strings.Contains(err.Error(), testKey) || !strings.Contains(err.Error(), "Anthropic messages returned 401: invalid x-api-key [REDACTED]") {
		t.Errorf("Structured() error = %v, want 401 with the key redacted", err)
	}
	if len(rec.paths) != 1 {
		t.Errorf("requests = %d, want 1", len(rec.paths))
	}
}

func TestListModels(t *testing.T) {
	t.Parallel()
	c, rec := setup(t, func(_ int, w http.ResponseWriter) {
		_, _ = io.WriteString(w, `{"data":[{"id":"claude-b","display_name":"Claude B"},{"id":"claude-a","display_name":"Claude A"}],"has_more":true,"last_id":"claude-a"}`)
	})
	got, err := c.ListModels(t.Context(), llm.Anthropic)
	if err != nil {
		t.Fatalf("ListModels() error = %v", err)
	}
	b, _ := json.Marshal(got)
	if want := `[{"id":"claude-b","provider":"anthropic","label":"Claude B"},{"id":"claude-a","provider":"anthropic","label":"Claude A"}]`; string(b) != want {
		t.Errorf("ListModels() = %s, want %s", b, want)
	}
	if len(rec.paths) != 1 || rec.paths[0] != "GET /v1/models" || rec.headers[0].Get("x-api-key") != testKey {
		t.Errorf("requests = %v, want one GET /v1/models with the key", rec.paths)
	}
}

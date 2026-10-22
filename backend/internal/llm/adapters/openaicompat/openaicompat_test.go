package openaicompat_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/llm"
	"dev-digest-go/backend/internal/llm/adapters/openaicompat"
	"dev-digest-go/backend/internal/platform/retry"
)

const (
	testKey = "sk-live-secret-0001"
	schema  = `{"type":"object","required":["candidates"],"properties":{"candidates":{"type":"array","maxItems":3,"items":{"type":"object","required":["rule"],"properties":{"rule":{"type":"string"}}}}}}`
	valid   = `{"candidates":[{"rule":"r"}]}`
)

type keys map[string]string

func (k keys) Get(name string) string { return k[name] }

// stub records every request and answers with the scripted handler.
type stub struct {
	mu      sync.Mutex
	paths   []string
	headers []http.Header
	bodies  []map[string]any
	handle  func(n int, w http.ResponseWriter, r *http.Request)
	srv     *httptest.Server
	hc      *http.Client
}

func newStub(t *testing.T, handle func(n int, w http.ResponseWriter, r *http.Request)) *stub {
	t.Helper()
	s := &stub{handle: handle}
	s.srv = httptest.NewTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		s.mu.Lock()
		s.paths = append(s.paths, r.Method+" "+r.URL.Path)
		s.headers = append(s.headers, r.Header.Clone())
		s.bodies = append(s.bodies, body)
		n := len(s.paths)
		s.mu.Unlock()
		s.handle(n, w, r)
	}))
	s.hc = s.srv.Client() // starts the server and sets its URL
	return s
}

func (s *stub) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.paths)
}

func (s *stub) url() string { return s.srv.URL + "/v1" }

func (s *stub) openai() *openaicompat.Adapter {
	return openaicompat.NewOpenAI(openaicompat.Options{BaseURL: s.url(), HTTPClient: s.hc})
}

func (s *stub) openrouter() *openaicompat.Adapter {
	return openaicompat.NewOpenRouter(openaicompat.Options{BaseURL: s.url(), HTTPClient: s.hc})
}

func reply(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, body)
}

func completion(content, usage string) string {
	c, _ := json.Marshal(content)
	return `{"choices":[{"message":{"content":` + string(c) + `}}]` + usage + `}`
}

const usage = `,"usage":{"prompt_tokens":11,"completion_tokens":7}`

func client(t *testing.T, p llm.Provider, ad llm.Adapter) *llm.Client {
	t.Helper()
	c := llm.New(llm.Options{
		Keys:     keys{"OPENAI_API_KEY": testKey, "OPENROUTER_API_KEY": testKey},
		Adapters: map[llm.Provider]llm.Adapter{p: ad},
		Logger:   slog.New(slog.DiscardHandler),
		Prices:   llm.NewPriceBook(llm.PriceBookOptions{}),
		Retry:    retry.Options{BaseDelay: time.Millisecond, MaxDelay: 5 * time.Millisecond, Jitter: func(time.Duration) time.Duration { return 0 }},
	})
	t.Cleanup(c.Close)
	return c
}

func request(p llm.Provider, model string) llm.Request {
	return llm.Request{
		Provider:   p,
		Model:      model,
		Messages:   []llm.Message{{Role: llm.System, Content: "sys"}, {Role: llm.User, Content: "hi"}},
		SchemaName: "Test",
		Schema:     json.RawMessage(schema),
	}
}

func TestOpenAI_requestShape(t *testing.T) {
	t.Parallel()
	st := newStub(t, func(_ int, w http.ResponseWriter, _ *http.Request) { reply(w, 200, completion(valid, usage)) })
	c := client(t, llm.OpenAI, st.openai())
	req := request(llm.OpenAI, "gpt-4.1")
	req.Temperature, req.MaxTokens = new(0.1), 500
	res, err := c.Structured(t.Context(), req)
	if err != nil {
		t.Fatalf("Structured() error = %v", err)
	}
	if res.TokensIn != 11 || res.TokensOut != 7 || res.Attempts != 1 || string(res.Data) != valid {
		t.Errorf("Structured() = %+v, want 11/7 tokens, 1 attempt", res)
	}
	// (11 * 2.0 + 7 * 8.0) / 1e6 for gpt-4.1.
	if res.CostUSD == nil || *res.CostUSD != 78.0/1_000_000 {
		t.Errorf("Structured() cost = %v, want the price-book cost", res.CostUSD)
	}
	if st.paths[0] != "POST /v1/chat/completions" || st.headers[0].Get("Authorization") != "Bearer "+testKey {
		t.Errorf("request = %s with Authorization %q, want POST /v1/chat/completions with the bearer key", st.paths[0], st.headers[0].Get("Authorization"))
	}
	body := st.bodies[0]
	rf, _ := body["response_format"].(map[string]any)
	js, _ := rf["json_schema"].(map[string]any)
	if rf["type"] != "json_schema" || js["strict"] != true || js["name"] != "Test" || body["temperature"] != 0.1 || body["max_tokens"] != float64(500) || body["model"] != "gpt-4.1" {
		t.Errorf("body = %v, want json_schema strict Test, temperature 0.1, max_tokens 500", body)
	}
	if _, ok := body["usage"]; ok {
		t.Error("OpenAI body has usage, want it only for OpenRouter")
	}
	sch, _ := json.Marshal(js["schema"])
	if strings.Contains(string(sch), "maxItems") || !strings.Contains(string(sch), `"additionalProperties":false`) {
		t.Errorf("schema sent = %s, want the strict form without array limits", sch)
	}
	msgs, _ := body["messages"].([]any)
	if len(msgs) != 2 {
		t.Errorf("messages = %v, want the system and user message", msgs)
	}
}

func TestOpenAI_temperatureAndCaps(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		model        string
		temp         *float64
		max          int
		wantTemp     any
		wantMax      any
		wantMaxCompl any
	}{
		{"default temperature 0", "gpt-4.1", nil, 0, float64(0), nil, nil},
		{"gpt-5.4 reasoning", "gpt-5.4", new(0.3), 6000, nil, nil, float64(6000)},
		{"o3-mini reasoning", "o3-mini", nil, 100, nil, nil, float64(100)},
		{"reasoning without cap", "o1", nil, 0, nil, nil, nil},
		{"non-reasoning cap", "gpt-4o-mini", new(0.7), 50, 0.7, float64(50), nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			st := newStub(t, func(_ int, w http.ResponseWriter, _ *http.Request) { reply(w, 200, completion(valid, usage)) })
			req := request(llm.OpenAI, tc.model)
			req.Temperature, req.MaxTokens = tc.temp, tc.max
			if _, err := client(t, llm.OpenAI, st.openai()).Structured(t.Context(), req); err != nil {
				t.Fatalf("Structured() error = %v", err)
			}
			b := st.bodies[0]
			if b["temperature"] != tc.wantTemp || b["max_tokens"] != tc.wantMax || b["max_completion_tokens"] != tc.wantMaxCompl {
				t.Errorf("body for %s = %v, want temperature %v, max_tokens %v, max_completion_tokens %v", tc.model, b, tc.wantTemp, tc.wantMax, tc.wantMaxCompl)
			}
		})
	}
}

func TestOpenAI_missingContentAndUsage(t *testing.T) {
	t.Parallel()
	st := newStub(t, func(n int, w http.ResponseWriter, _ *http.Request) {
		if n == 1 {
			reply(w, 200, `{"choices":[{"message":{"content":null}}],"usage":null}`)
			return
		}
		reply(w, 200, completion(valid, ""))
	})
	ad := st.openai()
	res, err := ad.Complete(t.Context(), testKey, llm.Attempt{Model: "gpt-4.1", Messages: []llm.Message{{Role: llm.User, Content: "x"}}, SchemaName: "T", Schema: json.RawMessage(schema)})
	if err != nil || res.Raw != "" || res.Usage != nil {
		t.Errorf("Complete(null content) = %+v, %v; want empty raw and no usage", res, err)
	}
	res, err = ad.Complete(t.Context(), testKey, llm.Attempt{Model: "gpt-4.1", Messages: []llm.Message{{Role: llm.User, Content: "x"}}, SchemaName: "T", Schema: json.RawMessage(schema)})
	if err != nil || res.Raw != valid || res.Usage != nil {
		t.Errorf("Complete(no usage) = %+v, %v; want the content and no usage", res, err)
	}
}

func TestOpenRouter_requestShapeAndCost(t *testing.T) {
	t.Parallel()
	st := newStub(t, func(_ int, w http.ResponseWriter, _ *http.Request) {
		reply(w, 200, completion(valid, `,"usage":{"prompt_tokens":5,"completion_tokens":3,"cost":0.0123}`))
	})
	c := client(t, llm.OpenRouter, st.openrouter())

	req := request(llm.OpenRouter, "deepseek/deepseek-r1")
	req.Temperature, req.MaxTokens = new(0.1), 300
	res, err := c.Structured(t.Context(), req)
	if err != nil {
		t.Fatalf("Structured() error = %v", err)
	}
	b := st.bodies[0]
	if _, ok := b["temperature"]; ok {
		t.Errorf("deepseek-r1 body has temperature %v, want none", b["temperature"])
	}
	if u, _ := b["usage"].(map[string]any); u["include"] != true || b["max_tokens"] != float64(300) {
		t.Errorf("body = %v, want usage.include and max_tokens 300", b)
	}
	if res.CostUSD == nil || *res.CostUSD != 0.0123 {
		t.Errorf("cost = %v, want the provider's usage.cost 0.0123", res.CostUSD)
	}

	req.Model = "openai/gpt-4.1"
	if _, err := c.Structured(t.Context(), req); err != nil {
		t.Fatalf("Structured() error = %v", err)
	}
	if st.bodies[1]["temperature"] != 0.1 {
		t.Errorf("openai/gpt-4.1 body temperature = %v, want 0.1", st.bodies[1]["temperature"])
	}

	req.Temperature = nil
	if _, err := c.Structured(t.Context(), req); err != nil {
		t.Fatalf("Structured() error = %v", err)
	}
	if _, ok := st.bodies[2]["temperature"]; ok {
		t.Error("OpenRouter body has a temperature the caller did not set")
	}
}

func TestOpenRouter_noTemperatureModels(t *testing.T) {
	t.Parallel()
	st := newStub(t, func(_ int, w http.ResponseWriter, _ *http.Request) { reply(w, 200, completion(valid, usage)) })
	c := client(t, llm.OpenRouter, st.openrouter())
	models := map[string]bool{ // model -> temperature sent
		"openai/o3-mini": false, "openai/o1": false, "openai/GPT-5": false, "openai/gpt-5.4-mini": false,
		"deepseek/deepseek-reasoner": false, "deepseek/deepseek-r1-0528": false, "deepseek/deepseek-r1:free": false,
		"openai/gpt-4o": true, "deepseek/deepseek-v4-flash": true, "gpt-50": true, "openai/o3x": true,
	}
	for model, want := range models {
		req := request(llm.OpenRouter, model)
		req.Temperature = new(0.2)
		before := st.count()
		if _, err := c.Structured(t.Context(), req); err != nil {
			t.Fatalf("Structured(%s) error = %v", model, err)
		}
		_, sent := st.bodies[before]["temperature"]
		if sent != want {
			t.Errorf("temperature sent for %s = %v, want %v", model, sent, want)
		}
	}
}

func TestOpenRouter_noChoices(t *testing.T) {
	t.Parallel()
	st := newStub(t, func(_ int, w http.ResponseWriter, _ *http.Request) {
		reply(w, 200, `{"choices":[],"error":{"message":"rate limited upstream"}}`)
	})
	c := client(t, llm.OpenRouter, st.openrouter())
	req := request(llm.OpenRouter, "openai/gpt-4.1")
	req.Reprompts = 2
	_, err := c.Structured(t.Context(), req)
	if err == nil || err.Error() != "OpenRouter returned no choices for Test: rate limited upstream" {
		t.Errorf("Structured() error = %v, want the no-choices text", err)
	}
	if got := st.count(); got != 1 {
		t.Errorf("requests = %d, want 1 (no reprompt)", got)
	}
}

func TestOpenAI_statusErrors(t *testing.T) {
	t.Parallel()
	t.Run("401 once, message scrubbed", func(t *testing.T) {
		st := newStub(t, func(_ int, w http.ResponseWriter, _ *http.Request) {
			reply(w, 401, `{"error":{"message":"Incorrect API key provided: `+testKey+`."}}`)
		})
		_, err := client(t, llm.OpenAI, st.openai()).Structured(t.Context(), request(llm.OpenAI, "gpt-4.1"))
		if err == nil || strings.Contains(err.Error(), testKey) || !strings.Contains(err.Error(), "OpenAI chat completions returned 401: Incorrect API key provided: [REDACTED].") {
			t.Errorf("Structured() error = %v, want 401 with the key redacted", err)
		}
		if got := st.count(); got != 1 {
			t.Errorf("requests = %d, want 1", got)
		}
	})
	t.Run("503 twice then 200", func(t *testing.T) {
		st := newStub(t, func(n int, w http.ResponseWriter, _ *http.Request) {
			if n <= 2 {
				reply(w, 503, `{}`)
				return
			}
			reply(w, 200, completion(valid, usage))
		})
		if _, err := client(t, llm.OpenAI, st.openai()).Structured(t.Context(), request(llm.OpenAI, "gpt-4.1")); err != nil {
			t.Fatalf("Structured() error = %v", err)
		}
		if got := st.count(); got != 3 {
			t.Errorf("requests = %d, want 3", got)
		}
	})
	t.Run("openrouter retries twice", func(t *testing.T) {
		st := newStub(t, func(_ int, w http.ResponseWriter, _ *http.Request) { reply(w, 429, `{}`) })
		_, err := client(t, llm.OpenRouter, st.openrouter()).Structured(t.Context(), request(llm.OpenRouter, "openai/gpt-4.1"))
		if err == nil || err.Error() != "OpenRouter chat completions returned 429" {
			t.Errorf("Structured() error = %v, want the status-only text", err)
		}
		if got := st.count(); got != 3 {
			t.Errorf("requests = %d, want 3 (first try and 2 retries)", got)
		}
	})
	t.Run("openai retries three times", func(t *testing.T) {
		st := newStub(t, func(_ int, w http.ResponseWriter, _ *http.Request) { reply(w, 500, `{}`) })
		if _, err := client(t, llm.OpenAI, st.openai()).Structured(t.Context(), request(llm.OpenAI, "gpt-4.1")); err == nil {
			t.Error("Structured() error = nil, want the 500")
		}
		if got := st.count(); got != 4 {
			t.Errorf("requests = %d, want 4", got)
		}
	})
}

func TestOpenAI_requestTimeout(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		st := newStub(t, func(_ int, _ http.ResponseWriter, r *http.Request) { <-r.Context().Done() })
		req := request(llm.OpenAI, "gpt-4.1")
		req.Timeout = time.Second
		start := time.Now()
		_, err := client(t, llm.OpenAI, st.openai()).Structured(t.Context(), req)
		if err == nil || !strings.Contains(err.Error(), "timed out after 1000ms") {
			t.Fatalf("Structured() error = %v, want the request timeout", err)
		}
		if got := st.count(); got != 1 || time.Since(start) != time.Second {
			t.Errorf("requests = %d after %v, want 1 after 1s", got, time.Since(start))
		}
	})
}

func TestOpenAI_listModels(t *testing.T) {
	t.Parallel()
	st := newStub(t, func(_ int, w http.ResponseWriter, _ *http.Request) {
		reply(w, 200, `{"data":[{"id":"gpt-4.1","created":11},{"id":"o3-mini","created":12},{"id":"dall-e-3","created":13},{"id":"text-embedding-3-small","created":14}]}`)
	})
	got, err := client(t, llm.OpenAI, st.openai()).ListModels(t.Context(), llm.OpenAI)
	if err != nil {
		t.Fatalf("ListModels() error = %v", err)
	}
	b, _ := json.Marshal(got)
	if want := `[{"id":"gpt-4.1","provider":"openai","created":11},{"id":"o3-mini","provider":"openai","created":12}]`; string(b) != want {
		t.Errorf("ListModels() = %s, want %s", b, want)
	}
	if st.paths[0] != "GET /v1/models" || st.headers[0].Get("Authorization") != "Bearer "+testKey {
		t.Errorf("request = %s with %q", st.paths[0], st.headers[0].Get("Authorization"))
	}
}

func TestOpenRouter_listModels(t *testing.T) {
	t.Parallel()
	st := newStub(t, func(_ int, w http.ResponseWriter, _ *http.Request) {
		reply(w, 200, `{"data":[
			{"id":"a","name":"A","context_length":4096,"pricing":{"prompt":"0.000001","completion":"0.000003"}},
			{"id":"b","name":"B"},
			{"id":"c","context_length":8192,"pricing":{"prompt":"0.0000005","completion":"0.000001"}},
			{"id":"d","pricing":{"prompt":"-1","completion":"-1"}}]}`)
	})
	got, err := client(t, llm.OpenRouter, st.openrouter()).ListModels(t.Context(), llm.OpenRouter)
	if err != nil {
		t.Fatalf("ListModels() error = %v", err)
	}
	b, _ := json.Marshal(got)
	var gotAny, wantAny any
	_ = json.Unmarshal(b, &gotAny)
	_ = json.Unmarshal([]byte(`[
		{"id":"c","provider":"openrouter","label":null,"pricing":{"promptPerM":0.5,"completionPerM":1},"contextLength":8192},
		{"id":"a","provider":"openrouter","label":"A","pricing":{"promptPerM":1,"completionPerM":3},"contextLength":4096},
		{"id":"b","provider":"openrouter","label":"B","pricing":null,"contextLength":null},
		{"id":"d","provider":"openrouter","label":null,"pricing":null,"contextLength":null}]`), &wantAny)
	if diff := cmp.Diff(wantAny, gotAny); diff != "" {
		t.Errorf("ListModels() mismatch (-want +got):\n%s", diff)
	}
}

func TestOpenRouter_listModelsHangs(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		st := newStub(t, func(_ int, _ http.ResponseWriter, r *http.Request) { <-r.Context().Done() })
		start := time.Now()
		_, err := client(t, llm.OpenRouter, st.openrouter()).ListModels(t.Context(), llm.OpenRouter)
		if err == nil || time.Since(start) > 16*time.Second {
			t.Errorf("ListModels() = %v after %v, want a failure within 16s", err, time.Since(start))
		}
		if got := st.count(); got != 1 {
			t.Errorf("requests = %d, want 1 (no retry)", got)
		}
	})
}

func TestOpenAI_listModelsRetries(t *testing.T) {
	t.Parallel()
	st := newStub(t, func(n int, w http.ResponseWriter, _ *http.Request) {
		if n == 1 {
			reply(w, 502, `{}`)
			return
		}
		reply(w, 200, `{"data":[{"id":"gpt-4.1"}]}`)
	})
	got, err := client(t, llm.OpenAI, st.openai()).ListModels(t.Context(), llm.OpenAI)
	if err != nil || len(got) != 1 || st.count() != 2 {
		t.Errorf("ListModels() = %v, %v after %d requests; want 1 model after 2", got, err, st.count())
	}
}

func TestOpenAI_cancelStopsRequest(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		st := newStub(t, func(_ int, _ http.ResponseWriter, r *http.Request) { <-r.Context().Done() })
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan error, 1)
		go func() {
			_, err := client(t, llm.OpenAI, st.openai()).Structured(ctx, request(llm.OpenAI, "gpt-4.1"))
			done <- err
		}()
		synctest.Wait()
		cancel()
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Errorf("Structured() error = %v, want context.Canceled", err)
		}
		if got := st.count(); got != 1 {
			t.Errorf("requests = %d, want 1", got)
		}
	})
}

func TestOpenAI_redirectIsNotFollowed(t *testing.T) {
	t.Parallel()
	st := newStub(t, func(_ int, w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/elsewhere" {
			reply(w, 200, completion(valid, usage))
			return
		}
		w.Header().Set("Location", "/elsewhere")
		w.WriteHeader(http.StatusTemporaryRedirect)
	})
	c := client(t, llm.OpenAI, st.openai())
	if _, err := c.Structured(t.Context(), request(llm.OpenAI, "gpt-4.1")); err == nil {
		t.Errorf("Structured() after a 307 = nil error, want a failure")
	}
	if got := st.count(); got != 1 {
		t.Errorf("requests = %d (%v), want 1: the redirect must not be followed with the key and body", got, st.paths)
	}
}

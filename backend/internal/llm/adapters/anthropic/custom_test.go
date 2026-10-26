package anthropic_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/llm"
	"dev-digest-go/backend/internal/llm/adapters/anthropic"
	"dev-digest-go/backend/internal/llm/custom"
)

const customID = "custom:claude-gw"

// gateway records every request and answers with the scripted handler.
type gateway struct {
	mu      sync.Mutex
	paths   []string
	headers []http.Header
	bodies  []map[string]any
	srv     *httptest.Server
	hc      *http.Client
}

func newGateway(t *testing.T, handle func(w http.ResponseWriter, r *http.Request)) *gateway {
	t.Helper()
	g := &gateway{}
	g.srv = httptest.NewTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		g.mu.Lock()
		g.paths = append(g.paths, r.Method+" "+r.URL.Path)
		g.headers = append(g.headers, r.Header.Clone())
		g.bodies = append(g.bodies, body)
		g.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		handle(w, r)
	}))
	g.hc = g.srv.Client()
	return g
}

func (g *gateway) count() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.paths)
}

func (g *gateway) custom(mod ...func(*anthropic.CustomOptions)) *anthropic.CustomAdapter {
	opts := anthropic.CustomOptions{ID: customID, Name: "Claude GW", BaseURL: g.srv.URL + "/v1", Key: testKey, HTTPClient: g.hc}
	for _, m := range mod {
		m(&opts)
	}
	return anthropic.NewCustom(opts)
}

func answer(body string) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, body) }
}

func fail(status int, body string) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}
}

func attempt(mode custom.Mode) llm.Attempt {
	temp := 0.3
	return llm.Attempt{
		Model: "claude-haiku-4-5",
		Messages: []llm.Message{
			{Role: llm.System, Content: "first"}, {Role: llm.System, Content: "second"}, {Role: llm.User, Content: "hi"},
		},
		SchemaName:  "Convention.Extraction v1",
		Schema:      json.RawMessage(schema),
		Temperature: &temp,
		MaxTokens:   700,
		Mode:        mode,
	}
}

func statusOf(err error) int {
	var sc interface{ StatusCode() int }
	if errors.As(err, &sc) {
		return sc.StatusCode()
	}
	return 0
}

func TestCustom_authHeaders(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		mod  func(*anthropic.CustomOptions)
		want map[string]string // header -> value; "" = must be absent
	}{
		{"x-api-key is the default", func(*anthropic.CustomOptions) {}, map[string]string{"X-Api-Key": testKey, "Authorization": "", "Anthropic-Version": "2023-06-01"}},
		{"own auth header", func(o *anthropic.CustomOptions) { o.AuthMode, o.AuthHeader = custom.AuthHeader, "api-key" }, map[string]string{"Api-Key": testKey, "X-Api-Key": "", "Authorization": ""}},
		{"bearer", func(o *anthropic.CustomOptions) { o.AuthMode = custom.AuthBearer }, map[string]string{"Authorization": "Bearer " + testKey, "X-Api-Key": ""}},
		{"no key sends no auth header", func(o *anthropic.CustomOptions) { o.Key = "" }, map[string]string{"X-Api-Key": "", "Authorization": "", "Anthropic-Version": "2023-06-01"}},
		{"no key with own header", func(o *anthropic.CustomOptions) {
			o.Key, o.AuthMode, o.AuthHeader = "", custom.AuthHeader, "api-key"
		}, map[string]string{"Api-Key": "", "X-Api-Key": ""}},
		{"two extra headers", func(o *anthropic.CustomOptions) {
			o.Headers = map[string]string{"X-Team": "core", "x-region": "eu"}
		}, map[string]string{"X-Team": "core", "X-Region": "eu", "X-Api-Key": testKey, "Anthropic-Version": "2023-06-01"}},
		{"extra anthropic-version wins", func(o *anthropic.CustomOptions) {
			o.Headers = map[string]string{"anthropic-version": "2024-01-01"}
		}, map[string]string{"Anthropic-Version": "2024-01-01"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			g := newGateway(t, answer(toolUse(`{"a":1}`)))
			if _, err := g.custom(tc.mod).Complete(t.Context(), "ignored-key", attempt(custom.ModeStrict)); err != nil {
				t.Fatalf("Complete() error = %v", err)
			}
			if g.paths[0] != "POST /v1/messages" {
				t.Errorf("request = %q, want POST /v1/messages", g.paths[0])
			}
			for h, want := range tc.want {
				if got := g.headers[0].Get(h); got != want {
					t.Errorf("header %s = %q, want %q", h, got, want)
				}
			}
			if v := g.headers[0].Values("Anthropic-Version"); len(v) != 1 {
				t.Errorf("anthropic-version sent %d times (%v), want once", len(v), v)
			}
		})
	}
}

func TestCustom_configurationErrors(t *testing.T) {
	t.Parallel()
	six := map[string]string{"A": "1", "B": "2", "C": "3", "D": "4", "E": "5", "F": "6"}
	tests := []struct {
		name string
		mod  func(*anthropic.CustomOptions)
	}{
		{"six headers", func(o *anthropic.CustomOptions) { o.Headers = six }},
		{"reserved name", func(o *anthropic.CustomOptions) { o.Headers = map[string]string{"Cookie": "x"} }},
		{"name equals the default auth header", func(o *anthropic.CustomOptions) { o.Headers = map[string]string{"X-API-Key": "x"} }},
		{"CRLF in value", func(o *anthropic.CustomOptions) { o.Headers = map[string]string{"X-A": "a\r\nX-B: b"} }},
		{"unknown auth mode", func(o *anthropic.CustomOptions) { o.AuthMode = "basic" }},
		{"no client", func(o *anthropic.CustomOptions) { o.HTTPClient = nil }},
		{"link-local base URL", func(o *anthropic.CustomOptions) { o.BaseURL = "http://169.254.169.254/v1" }},
		{"bad id", func(o *anthropic.CustomOptions) { o.ID = "claude-gw" }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			g := newGateway(t, answer(toolUse(`{}`)))
			ad := g.custom(tc.mod)
			_, err := ad.Complete(t.Context(), "", attempt(custom.ModeStrict))
			_, lerr := ad.ListModels(t.Context(), "")
			if err == nil || lerr == nil {
				t.Errorf("Complete() / ListModels() errors = %v / %v, want both to fail", err, lerr)
			}
			if n := g.count(); n != 0 {
				t.Errorf("requests = %d, want none after a configuration error", n)
			}
		})
	}
}

func TestCustom_bodyPerMode(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		mode      custom.Mode
		wantTools bool
	}{
		{"strict", custom.ModeStrict, true},
		{"empty mode is strict", "", true},
		{"prompt_only", custom.ModePromptOnly, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			g := newGateway(t, answer(toolUse(`{"a":1}`)))
			if _, err := g.custom().Complete(t.Context(), "", attempt(tc.mode)); err != nil {
				t.Fatalf("Complete() error = %v", err)
			}
			body := g.bodies[0]
			tools, hasTools := body["tools"].([]any)
			choice, hasChoice := body["tool_choice"].(map[string]any)
			if hasTools != tc.wantTools || hasChoice != tc.wantTools {
				t.Fatalf("tools = %v, tool_choice = %v; want present = %v", body["tools"], body["tool_choice"], tc.wantTools)
			}
			if tc.wantTools {
				if len(tools) != 1 || choice["type"] != "tool" || choice["name"] != "Convention_Extraction_v1" {
					t.Errorf("tools = %v, tool_choice = %v; want one forced tool Convention_Extraction_v1", tools, choice)
				}
			}
			if body["system"] != "first\n\nsecond" || body["model"] != "claude-haiku-4-5" || body["max_tokens"] != float64(700) || body["temperature"] != 0.3 {
				t.Errorf("body = %v, want the joined system prompt, verbatim model, max_tokens 700, temperature 0.3", body)
			}
			if _, has := body["stream"]; has {
				t.Error("body asks for streaming, want a non-streaming request")
			}
		})
	}
}

// The strict body of a custom provider is the built-in Anthropic body.
func TestCustom_strictBodyEqualsBuiltIn(t *testing.T) {
	t.Parallel()
	builtIn := newGateway(t, answer(toolUse(`{}`)))
	gw := newGateway(t, answer(toolUse(`{}`)))
	ad := anthropic.New(anthropic.Options{BaseURL: builtIn.srv.URL + "/v1", HTTPClient: builtIn.hc})
	if _, err := ad.Complete(t.Context(), testKey, attempt("")); err != nil {
		t.Fatalf("Complete() error = %v", err)
	}
	if _, err := gw.custom().Complete(t.Context(), "", attempt(custom.ModeStrict)); err != nil {
		t.Fatalf("custom Complete() error = %v", err)
	}
	if diff := cmp.Diff(builtIn.bodies[0], gw.bodies[0]); diff != "" {
		t.Errorf("strict body differs from the built-in body (-builtin +custom):\n%s", diff)
	}
}

func TestCustom_jsonObjectIsRefused(t *testing.T) {
	t.Parallel()
	g := newGateway(t, answer(toolUse(`{}`)))
	if _, err := g.custom().Complete(t.Context(), "", attempt(custom.ModeJSONObject)); err == nil {
		t.Error("Complete() in json_object mode = nil error, want a refusal: the mode is OpenAI-compatible only")
	}
	if _, err := g.custom().Complete(t.Context(), "", attempt("bogus")); err == nil {
		t.Error("Complete() in an unknown mode = nil error, want a refusal")
	}
	if n := g.count(); n != 0 {
		t.Errorf("requests = %d, want none", n)
	}
}

func TestCustom_rawOutputPerMode(t *testing.T) {
	t.Parallel()
	const split = `{"content":[{"type":"text","text":"Here: {\"a\":"},{"type":"tool_use","id":"t","name":"X","input":{"z":1}},{"type":"text","text":"1}"}],"usage":{"input_tokens":2,"output_tokens":3}}`
	tests := []struct {
		name string
		mode custom.Mode
		body string
		want string
	}{
		{"strict reads the tool input compact", custom.ModeStrict, split, `{"z":1}`},
		{"strict without a tool call is empty", custom.ModeStrict, `{"content":[{"type":"text","text":"prose"}]}`, ""},
		{"prompt_only joins the text blocks in order", custom.ModePromptOnly, split, `Here: {"a":1}`},
		{"prompt_only ignores a tool call alone", custom.ModePromptOnly, `{"content":[{"type":"tool_use","input":{"z":1}}]}`, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			g := newGateway(t, answer(tc.body))
			res, err := g.custom().Complete(t.Context(), "", attempt(tc.mode))
			if err != nil {
				t.Fatalf("Complete() error = %v", err)
			}
			if res.Raw != tc.want {
				t.Errorf("Raw = %q, want %q", res.Raw, tc.want)
			}
		})
	}
}

func TestCustom_cost(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		usage string
		want  *float64
	}{
		{"cost", `{"input_tokens":2,"output_tokens":3,"cost":0.004}`, new(0.004)},
		{"zero", `{"input_tokens":2,"output_tokens":3,"cost":0}`, new(0.0)},
		{"negative", `{"input_tokens":2,"output_tokens":3,"cost":-1}`, nil},
		{"string", `{"input_tokens":2,"output_tokens":3,"cost":"0.1"}`, nil},
		{"infinite", `{"input_tokens":2,"output_tokens":3,"cost":1e999}`, nil},
		{"absent", `{"input_tokens":2,"output_tokens":3}`, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			g := newGateway(t, answer(`{"content":[{"type":"tool_use","input":{}}],"usage":`+tc.usage+`}`))
			res, err := g.custom().Complete(t.Context(), "", attempt(custom.ModeStrict))
			if err != nil {
				t.Fatalf("Complete() error = %v", err)
			}
			if diff := cmp.Diff(tc.want, res.CostUSD); diff != "" {
				t.Errorf("CostUSD mismatch (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(&llm.Usage{TokensIn: 2, TokensOut: 3}, res.Usage); diff != "" {
				t.Errorf("Usage mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestCustom_statusErrorsKeepTheirStatus(t *testing.T) {
	t.Parallel()
	for _, status := range []int{400, 401, 404, 422, 429, 500, 529} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			t.Parallel()
			g := newGateway(t, fail(status, `{"error":{"message":"tools are not supported"}}`))
			_, err := g.custom().Complete(t.Context(), "", attempt(custom.ModeStrict))
			if got := statusOf(err); got != status {
				t.Fatalf("Complete() StatusCode = %d (%v), want %d", got, err, status)
			}
			if !strings.Contains(err.Error(), "Claude GW messages returned") {
				t.Errorf("error = %q, want the provider name and status", err)
			}
			if status == 401 {
				if !strings.HasSuffix(err.Error(), "returned 401: API key rejected by the provider") {
					t.Errorf("error = %q, want the fixed 401 message", err)
				}
			} else if !strings.Contains(err.Error(), "tools are not supported") {
				t.Errorf("error = %q, want the gateway message", err)
			}
		})
	}
}

func TestCustom_secretsAreScrubbedFromErrors(t *testing.T) {
	t.Parallel()
	const hdr = "team-secret-value-9"
	tests := []struct {
		name string
		body string
	}{
		{"key echoed", `{"error":{"message":"bad key ` + testKey + `"}}`},
		{"header value echoed", `{"error":{"message":"bad header ` + hdr + `"}}`},
		{"masked key echoed", `{"error":{"message":"invalid x-api-key sk-ant-****0002"}}`},
		{"secret straddles the cut", `{"error":{"message":"` + strings.Repeat("x", 290) + testKey + hdr + `"}}`},
		{"partial key echoed with plain dots", `{"error":{"message":"invalid key ` + testKey[:8] + `...` + testKey[len(testKey)-4:] + `"}}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			g := newGateway(t, fail(400, tc.body))
			ad := g.custom(func(o *anthropic.CustomOptions) { o.Headers = map[string]string{"X-Team": hdr} })
			_, err := ad.Complete(t.Context(), "", attempt(custom.ModeStrict))
			_, lerr := ad.ListModels(t.Context(), "")
			for _, e := range []error{err, lerr} {
				if statusOf(e) != 400 {
					t.Fatalf("error = %v, want a 400 status error", e)
				}
				for _, secret := range []string{testKey, hdr, "sk-ant", "0002", "team-secret", testKey[:8], testKey[len(testKey)-4:]} {
					if strings.Contains(e.Error(), secret) {
						t.Errorf("error %q contains %q", e, secret)
					}
				}
			}
		})
	}
}

func TestCustom_authRejectionsGetAFixedMessage(t *testing.T) { // security r1 L1
	t.Parallel()
	const echoed = `{"error":{"message":"invalid x-api-key gwkey-AB...wxyz"}}`
	for _, tc := range []struct {
		name   string
		status int
		key    string
		want   string
	}{
		{"401 with a key", 401, testKey, "Claude GW messages returned 401: API key rejected by the provider"},
		{"403 with a key", 403, testKey, "Claude GW messages returned 403: API key rejected by the provider"},
		{"403 without a key", 403, "", "Claude GW messages returned 403: The gateway rejected the request without a key (HTTP 403)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			g := newGateway(t, fail(tc.status, echoed))
			ad := g.custom(func(o *anthropic.CustomOptions) { o.Key = tc.key })
			_, err := ad.Complete(t.Context(), "", attempt(custom.ModeStrict))
			if err == nil || err.Error() != tc.want || statusOf(err) != tc.status {
				t.Errorf("Complete() error = %v (status %d), want %q with status %d", err, statusOf(err), tc.want, tc.status)
			}
		})
	}
}

func TestCustom_transportErrorHasNoBaseURL(t *testing.T) { // security r1 INFO: *url.Error is unwrapped
	t.Parallel()
	cause := errors.New("refused by the address guard")
	hc := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) { return nil, cause })}
	ad := anthropic.NewCustom(anthropic.CustomOptions{ID: customID, Name: "Claude GW", BaseURL: "https://gateway.test/tenant-path/v1", Key: testKey, HTTPClient: hc})
	for name, call := range map[string]func() error{
		"complete": func() error { _, err := ad.Complete(t.Context(), "", attempt(custom.ModeStrict)); return err },
		"models":   func() error { _, err := ad.ListModels(t.Context(), ""); return err },
	} {
		err := call()
		if err == nil || !errors.Is(err, cause) {
			t.Fatalf("%s error = %v, want it to wrap the cause", name, err)
		}
		if strings.Contains(err.Error(), "gateway.test") || strings.Contains(err.Error(), "tenant-path") {
			t.Errorf("%s error %q holds the base URL", name, err)
		}
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestCustom_redirectIsNotFollowed(t *testing.T) {
	t.Parallel()
	target := newGateway(t, answer(toolUse(`{}`)))
	gw := newGateway(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Location", target.srv.URL+"/v1/messages")
		w.WriteHeader(http.StatusTemporaryRedirect)
	})
	follower := &http.Client{Transport: gw.hc.Transport}
	ad := gw.custom(func(o *anthropic.CustomOptions) { o.HTTPClient = follower })
	if _, err := ad.Complete(t.Context(), "", attempt(custom.ModeStrict)); statusOf(err) != http.StatusTemporaryRedirect {
		t.Errorf("Complete() error = %v, want a 307 status error", err)
	}
	if _, err := ad.ListModels(t.Context(), ""); statusOf(err) != http.StatusTemporaryRedirect {
		t.Errorf("ListModels() error = %v, want a 307 status error", err)
	}
	if n := target.count(); n != 0 {
		t.Errorf("redirect target received %d requests, want none (it would get the key)", n)
	}
	if follower.CheckRedirect != nil {
		t.Error("the caller's client was modified, want it left untouched")
	}
}

func TestCustom_policy(t *testing.T) {
	t.Parallel()
	g := newGateway(t, answer(toolUse(`{}`)))
	want := llm.Policy{RequestTimeout: 120_000_000_000, Retries: 3, ListTimeout: 30_000_000_000, ListRetries: 3}
	if diff := cmp.Diff(want, g.custom().Policy()); diff != "" {
		t.Errorf("Policy() mismatch (-want +got):\n%s", diff)
	}
	if g.custom().RequiresKey() {
		t.Error("RequiresKey() = true, want false: a gateway may need no key")
	}
}

func TestCustom_listModels(t *testing.T) {
	t.Parallel()
	g := newGateway(t, answer(`{"data":[{"id":"b","display_name":"Bee"},{"id":"a"},{"id":"b"},{"id":""}]}`))
	got, err := g.custom().ListModels(t.Context(), "")
	if err != nil {
		t.Fatalf("ListModels() error = %v", err)
	}
	bee := "Bee"
	want := []llm.ModelInfo{{ID: "b", Provider: customID, Label: &bee}, {ID: "a", Provider: customID}}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("ListModels() mismatch (-want +got):\n%s", diff)
	}
	if g.paths[0] != "GET /v1/models" || g.headers[0].Get("X-Api-Key") != testKey || g.headers[0].Get("Anthropic-Version") != "2023-06-01" {
		t.Errorf("request = %s with headers %v, want GET /v1/models with the key and version", g.paths[0], g.headers[0])
	}
}

func TestCustom_listModelsUnavailable(t *testing.T) {
	t.Parallel()
	for _, status := range []int{404, 405, 401, 500} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			t.Parallel()
			g := newGateway(t, fail(status, `{"error":{"message":"nope"}}`))
			_, err := g.custom().ListModels(t.Context(), "")
			if got := statusOf(err); got != status {
				t.Errorf("ListModels() StatusCode = %d (%v), want %d", got, err, status)
			}
		})
	}
}

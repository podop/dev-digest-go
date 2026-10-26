package openaicompat_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/llm"
	"dev-digest-go/backend/internal/llm/adapters/openaicompat"
	"dev-digest-go/backend/internal/llm/custom"
)

const customID = "custom:synapse"

// custom builds the custom adapter against the stub; mod adjusts the options.
func (s *stub) custom(mod ...func(*openaicompat.CustomOptions)) *openaicompat.CustomAdapter {
	opts := openaicompat.CustomOptions{ID: customID, Name: "Synapse", BaseURL: s.url(), Key: testKey, HTTPClient: s.hc}
	for _, m := range mod {
		m(&opts)
	}
	return openaicompat.NewCustom(opts)
}

func attempt(mode custom.Mode) llm.Attempt {
	temp := 0.2
	return llm.Attempt{
		Model:       "gpt-4.1",
		Messages:    []llm.Message{{Role: llm.System, Content: "sys"}, {Role: llm.User, Content: "hi"}},
		SchemaName:  "Test",
		Schema:      json.RawMessage(schema),
		Temperature: &temp,
		MaxTokens:   500,
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

func answer(content, usage string) func(int, http.ResponseWriter, *http.Request) {
	return func(_ int, w http.ResponseWriter, _ *http.Request) { reply(w, 200, completion(content, usage)) }
}

func TestCustom_authHeaders(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		mod  func(*openaicompat.CustomOptions)
		want map[string]string // header -> value; "" = must be absent
	}{
		{"bearer is the default", func(*openaicompat.CustomOptions) {}, map[string]string{"Authorization": "Bearer " + testKey, "X-Api-Key": ""}},
		{"explicit bearer", func(o *openaicompat.CustomOptions) { o.AuthMode = custom.AuthBearer }, map[string]string{"Authorization": "Bearer " + testKey}},
		{"header mode with its header", func(o *openaicompat.CustomOptions) {
			o.AuthMode, o.AuthHeader = custom.AuthHeader, "api-key"
		}, map[string]string{"Api-Key": testKey, "Authorization": ""}},
		{"header mode defaults to x-api-key", func(o *openaicompat.CustomOptions) { o.AuthMode = custom.AuthHeader }, map[string]string{"X-Api-Key": testKey, "Authorization": ""}},
		{"no key sends no auth header (bearer)", func(o *openaicompat.CustomOptions) { o.Key = "" }, map[string]string{"Authorization": "", "X-Api-Key": ""}},
		{"no key sends no auth header (header)", func(o *openaicompat.CustomOptions) {
			o.Key, o.AuthMode, o.AuthHeader = "", custom.AuthHeader, "api-key"
		}, map[string]string{"Authorization": "", "Api-Key": ""}},
		{"extra headers are sent", func(o *openaicompat.CustomOptions) {
			o.Headers = map[string]string{"X-Team": "core", "x-region": "eu"}
		}, map[string]string{"Authorization": "Bearer " + testKey, "X-Team": "core", "X-Region": "eu"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			st := newStub(t, answer(valid, ""))
			if _, err := st.custom(tc.mod).Complete(t.Context(), "ignored-key", attempt(custom.ModeStrict)); err != nil {
				t.Fatalf("Complete() error = %v", err)
			}
			if st.paths[0] != "POST /v1/chat/completions" {
				t.Errorf("request = %q, want POST /v1/chat/completions", st.paths[0])
			}
			for h, want := range tc.want {
				if got := st.headers[0].Get(h); got != want {
					t.Errorf("header %s = %q, want %q", h, got, want)
				}
			}
			if got := st.headers[0].Get("Content-Type"); got != "application/json" {
				t.Errorf("Content-Type = %q, want application/json", got)
			}
		})
	}
}

func TestCustom_keyArgumentIsIgnored(t *testing.T) {
	t.Parallel()
	st := newStub(t, answer(valid, ""))
	ad := st.custom(func(o *openaicompat.CustomOptions) { o.Key = "" })
	if _, err := ad.Complete(t.Context(), "stray-built-in-key", attempt(custom.ModeStrict)); err != nil {
		t.Fatalf("Complete() error = %v", err)
	}
	if _, err := ad.ListModels(t.Context(), "stray-built-in-key"); err != nil {
		t.Fatalf("ListModels() error = %v", err)
	}
	for i, h := range st.headers {
		if h.Get("Authorization") != "" {
			t.Errorf("request %d carried Authorization %q, want none: the key comes from the options only", i, h.Get("Authorization"))
		}
	}
}

func TestCustom_headerValidation(t *testing.T) {
	t.Parallel()
	five := map[string]string{"A": "1", "B": "2", "C": "3", "D": "4", "E": "5"}
	six := map[string]string{"A": "1", "B": "2", "C": "3", "D": "4", "E": "5", "F": "6"}
	tests := []struct {
		name    string
		mod     func(*openaicompat.CustomOptions)
		wantErr bool
	}{
		{"five headers", func(o *openaicompat.CustomOptions) { o.Headers = five }, false},
		{"six headers", func(o *openaicompat.CustomOptions) { o.Headers = six }, true},
		{"reserved name", func(o *openaicompat.CustomOptions) { o.Headers = map[string]string{"Authorization": "x"} }, true},
		{"content type", func(o *openaicompat.CustomOptions) { o.Headers = map[string]string{"content-type": "x"} }, true},
		{"name equals the auth header", func(o *openaicompat.CustomOptions) {
			o.AuthMode, o.AuthHeader, o.Headers = custom.AuthHeader, "api-key", map[string]string{"API-KEY": "x"}
		}, true},
		{"not a token", func(o *openaicompat.CustomOptions) { o.Headers = map[string]string{"bad name": "x"} }, true},
		{"CRLF in value", func(o *openaicompat.CustomOptions) { o.Headers = map[string]string{"X-A": "a\r\nX-B: b"} }, true},
		{"empty value", func(o *openaicompat.CustomOptions) { o.Headers = map[string]string{"X-A": ""} }, true},
		{"same name in two cases", func(o *openaicompat.CustomOptions) { o.Headers = map[string]string{"X-A": "1", "x-a": "2"} }, true},
		{"unknown auth mode", func(o *openaicompat.CustomOptions) { o.AuthMode = "basic" }, true},
		{"bad auth header", func(o *openaicompat.CustomOptions) { o.AuthMode, o.AuthHeader = custom.AuthHeader, "a b" }, true},
		{"no client", func(o *openaicompat.CustomOptions) { o.HTTPClient = nil }, true},
		{"bad id", func(o *openaicompat.CustomOptions) { o.ID = "synapse" }, true},
		{"link-local base URL", func(o *openaicompat.CustomOptions) { o.BaseURL = "http://169.254.169.254/v1" }, true},
		{"unspecified base URL", func(o *openaicompat.CustomOptions) { o.BaseURL = "http://0.0.0.0/v1" }, true},
		{"base URL with credentials", func(o *openaicompat.CustomOptions) { o.BaseURL = "http://u:p@gateway.test/v1" }, true},
		{"base URL with another scheme", func(o *openaicompat.CustomOptions) { o.BaseURL = "ftp://gateway.test/v1" }, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			st := newStub(t, answer(valid, ""))
			ad := st.custom(tc.mod)
			_, err := ad.Complete(t.Context(), "", attempt(custom.ModeStrict))
			_, lerr := ad.ListModels(t.Context(), "")
			if tc.wantErr {
				if err == nil || lerr == nil {
					t.Errorf("Complete() / ListModels() errors = %v / %v, want both to fail", err, lerr)
				}
				if n := st.count(); n != 0 {
					t.Errorf("requests = %d, want none after a configuration error", n)
				}
				if err != nil && strings.Contains(err.Error(), "CRLF") {
					t.Errorf("error = %v, want no header value", err)
				}
				return
			}
			if err != nil || lerr != nil {
				t.Errorf("Complete() / ListModels() errors = %v / %v, want none", err, lerr)
			}
			if tc.name == "five headers" {
				for h, want := range five {
					if got := st.headers[0].Get(h); got != want {
						t.Errorf("header %s = %q, want %q", h, got, want)
					}
				}
			}
		})
	}
}

func TestCustom_errorsNameNoHeaderValue(t *testing.T) {
	t.Parallel()
	st := newStub(t, answer(valid, ""))
	ad := st.custom(func(o *openaicompat.CustomOptions) { o.Headers = map[string]string{"X-A": "bad\nvalue-secret"} })
	_, err := ad.Complete(t.Context(), "", attempt(custom.ModeStrict))
	if err == nil || strings.Contains(err.Error(), "value-secret") {
		t.Errorf("Complete() error = %v, want a configuration error without the header value", err)
	}
}

func TestCustom_bodyPerMode(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		mode       custom.Mode
		wantFormat map[string]any // nil = no response_format
	}{
		{"strict", custom.ModeStrict, map[string]any{"type": "json_schema"}},
		{"empty mode is strict", "", map[string]any{"type": "json_schema"}},
		{"json_object", custom.ModeJSONObject, map[string]any{"type": "json_object"}},
		{"prompt_only", custom.ModePromptOnly, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			st := newStub(t, answer(valid, ""))
			if _, err := st.custom().Complete(t.Context(), "", attempt(tc.mode)); err != nil {
				t.Fatalf("Complete() error = %v", err)
			}
			body := st.bodies[0]
			rf, present := body["response_format"].(map[string]any)
			if tc.wantFormat == nil {
				if present {
					t.Errorf("response_format = %v, want none", rf)
				}
			} else {
				if tc.wantFormat["type"] == "json_object" && len(rf) != 1 {
					t.Errorf("response_format = %v, want exactly {type: json_object}: no json_schema", rf)
				}
				if rf["type"] != tc.wantFormat["type"] {
					t.Errorf("response_format.type = %v, want %v", rf["type"], tc.wantFormat["type"])
				}
				if js, _ := rf["json_schema"].(map[string]any); tc.wantFormat["type"] == "json_schema" && (js["strict"] != true || js["name"] != "Test") {
					t.Errorf("json_schema = %v, want strict, named Test", js)
				}
			}
			for _, k := range []string{"tools", "tool_choice", "stream", "usage", "session_id"} {
				if _, has := body[k]; has {
					t.Errorf("body has %q = %v, want it absent", k, body[k])
				}
			}
			if body["model"] != "gpt-4.1" || body["temperature"] != 0.2 || body["max_tokens"] != float64(500) {
				t.Errorf("body = %v, want the verbatim model, temperature 0.2 and max_tokens 500", body)
			}
		})
	}
}

// The strict body of a custom provider is the built-in OpenAI body, byte for byte.
func TestCustom_strictBodyEqualsBuiltInOpenAI(t *testing.T) {
	t.Parallel()
	for _, model := range []string{"gpt-4.1", "o3-mini", "llama3"} {
		builtIn := newStub(t, answer(valid, ""))
		gw := newStub(t, answer(valid, ""))
		at := attempt(custom.ModeStrict)
		at.Model = model
		at2 := attempt("")
		at2.Model = model
		if _, err := builtIn.openai().Complete(t.Context(), testKey, at2); err != nil {
			t.Fatalf("OpenAI Complete() error = %v", err)
		}
		if _, err := gw.custom().Complete(t.Context(), "", at); err != nil {
			t.Fatalf("custom Complete() error = %v", err)
		}
		if diff := cmp.Diff(builtIn.bodies[0], gw.bodies[0]); diff != "" {
			t.Errorf("strict body for %s differs from the OpenAI body (-openai +custom):\n%s", model, diff)
		}
	}
}

func TestCustom_resultAndCost(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		usage    string
		wantCost *float64
		wantUse  *llm.Usage
	}{
		{"cost", `,"usage":{"prompt_tokens":11,"completion_tokens":7,"cost":0.0123}`, new(0.0123), &llm.Usage{TokensIn: 11, TokensOut: 7}},
		{"zero cost", `,"usage":{"prompt_tokens":1,"completion_tokens":1,"cost":0}`, new(0.0), &llm.Usage{TokensIn: 1, TokensOut: 1}},
		{"negative cost", `,"usage":{"prompt_tokens":1,"completion_tokens":1,"cost":-0.5}`, nil, &llm.Usage{TokensIn: 1, TokensOut: 1}},
		{"string cost", `,"usage":{"prompt_tokens":1,"completion_tokens":1,"cost":"0.5"}`, nil, &llm.Usage{TokensIn: 1, TokensOut: 1}},
		{"null cost", `,"usage":{"prompt_tokens":1,"completion_tokens":1,"cost":null}`, nil, &llm.Usage{TokensIn: 1, TokensOut: 1}},
		{"object cost", `,"usage":{"prompt_tokens":1,"completion_tokens":1,"cost":{"total":1}}`, nil, &llm.Usage{TokensIn: 1, TokensOut: 1}},
		{"infinite cost", `,"usage":{"prompt_tokens":1,"completion_tokens":1,"cost":1e999}`, nil, &llm.Usage{TokensIn: 1, TokensOut: 1}},
		{"no cost", `,"usage":{"prompt_tokens":3,"completion_tokens":4}`, nil, &llm.Usage{TokensIn: 3, TokensOut: 4}},
		{"no usage", ``, nil, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			st := newStub(t, answer(valid, tc.usage))
			res, err := st.custom().Complete(t.Context(), "", attempt(custom.ModeStrict))
			if err != nil {
				t.Fatalf("Complete() error = %v", err)
			}
			if res.Raw != valid {
				t.Errorf("Raw = %q, want %q", res.Raw, valid)
			}
			if diff := cmp.Diff(tc.wantCost, res.CostUSD); diff != "" {
				t.Errorf("CostUSD mismatch (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(tc.wantUse, res.Usage); diff != "" {
				t.Errorf("Usage mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestCustom_noChoicesIsAnEmptyAnswer(t *testing.T) {
	t.Parallel()
	st := newStub(t, func(_ int, w http.ResponseWriter, _ *http.Request) { reply(w, 200, `{"choices":[]}`) })
	res, err := st.custom().Complete(t.Context(), "", attempt(custom.ModeStrict))
	if err != nil || res.Raw != "" {
		t.Errorf("Complete() = %q, %v; want an empty answer for the re-prompt loop, no error", res.Raw, err)
	}
}

func TestCustom_statusErrorsKeepTheirStatus(t *testing.T) {
	t.Parallel()
	for _, status := range []int{400, 401, 404, 422, 429, 500, 503} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			t.Parallel()
			st := newStub(t, func(_ int, w http.ResponseWriter, _ *http.Request) {
				reply(w, status, `{"error":{"message":"response_format json_schema is not supported"}}`)
			})
			_, err := st.custom().Complete(t.Context(), "", attempt(custom.ModeStrict))
			if got := statusOf(err); got != status {
				t.Fatalf("Complete() StatusCode = %d (error %v), want %d", got, err, status)
			}
			if !strings.Contains(err.Error(), "Synapse chat completions returned") || !strings.Contains(err.Error(), "json_schema is not supported") {
				t.Errorf("error = %q, want the provider name, status and the gateway message", err)
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
		{"masked key echoed", `{"error":{"message":"Incorrect API key provided: sk-live-****0001"}}`},
		{"secret straddles the cut", `{"error":{"message":"` + strings.Repeat("x", 290) + testKey + hdr + `"}}`},
		{"json escaped secret", `{"error":{"message":"` + strings.ReplaceAll(testKey, "-", `-`) + `"}}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			st := newStub(t, func(_ int, w http.ResponseWriter, _ *http.Request) { reply(w, 401, tc.body) })
			ad := st.custom(func(o *openaicompat.CustomOptions) { o.Headers = map[string]string{"X-Team": hdr} })
			_, err := ad.Complete(t.Context(), "", attempt(custom.ModeStrict))
			_, lerr := ad.ListModels(t.Context(), "")
			for _, e := range []error{err, lerr} {
				if statusOf(e) != 401 {
					t.Fatalf("error = %v, want a 401 status error", e)
				}
				for _, secret := range []string{testKey, hdr, "sk-live", "0001", "team-secret"} {
					if strings.Contains(e.Error(), secret) {
						t.Errorf("error %q contains %q", e, secret)
					}
				}
			}
		})
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

var errGuard = errors.New("refused by the address guard")

func TestCustom_transportErrorIsScrubbedButKeepsItsCause(t *testing.T) {
	t.Parallel()
	hc := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return nil, fmt.Errorf("dial tcp: %s %s: %w", testKey, r.Header.Get("X-Team"), errGuard)
	})}
	ad := openaicompat.NewCustom(openaicompat.CustomOptions{
		ID: customID, BaseURL: "https://gateway.test/v1", Key: testKey, Headers: map[string]string{"X-Team": "team-9"}, HTTPClient: hc,
	})
	_, err := ad.Complete(t.Context(), "", attempt(custom.ModeStrict))
	if err == nil || !errors.Is(err, errGuard) {
		t.Fatalf("Complete() error = %v, want it to wrap the guard error (errors.Is)", err)
	}
	if strings.Contains(err.Error(), testKey) || strings.Contains(err.Error(), "team-9") {
		t.Errorf("error %q contains a secret", err)
	}
}

func TestCustom_redirectIsNotFollowed(t *testing.T) {
	t.Parallel()
	target := newStub(t, answer(valid, ""))
	gw := newStub(t, func(_ int, w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Location", target.srv.URL+"/v1/chat/completions")
		w.WriteHeader(http.StatusTemporaryRedirect)
	})
	// A caller's client that follows redirects must not make the adapter follow them.
	follower := &http.Client{Transport: gw.hc.Transport}
	ad := gw.custom(func(o *openaicompat.CustomOptions) { o.HTTPClient = follower })
	_, err := ad.Complete(t.Context(), "", attempt(custom.ModeStrict))
	if got := statusOf(err); got != http.StatusTemporaryRedirect {
		t.Errorf("Complete() StatusCode = %d (%v), want 307", got, err)
	}
	if _, err := ad.ListModels(t.Context(), ""); statusOf(err) != http.StatusTemporaryRedirect {
		t.Errorf("ListModels() error = %v, want a 307 status error", err)
	}
	if n := target.count(); n != 0 {
		t.Errorf("redirect target received %d requests, want none", n)
	}
	if follower.CheckRedirect != nil {
		t.Error("the caller's client was modified, want it left untouched")
	}
}

func TestCustom_hugeResponseIsAProviderError(t *testing.T) {
	t.Parallel()
	st := newStub(t, func(_ int, w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"choices":[],"pad":%q}`, strings.Repeat("a", 17<<20))
	})
	if _, err := st.custom().Complete(t.Context(), "", attempt(custom.ModeStrict)); err == nil {
		t.Error("Complete() with a 17 MiB body = nil error, want a provider error")
	}
}

func TestCustom_policy(t *testing.T) {
	t.Parallel()
	st := newStub(t, answer(valid, ""))
	want := llm.Policy{RequestTimeout: 120_000_000_000, Retries: 3, ListTimeout: 30_000_000_000, ListRetries: 3}
	if diff := cmp.Diff(want, st.custom().Policy()); diff != "" {
		t.Errorf("Policy() mismatch (-want +got):\n%s", diff)
	}
	own := llm.Policy{RequestTimeout: 5_000_000_000, Retries: 1, ListTimeout: 2_000_000_000, ListRetries: 0}
	if diff := cmp.Diff(own, st.custom(func(o *openaicompat.CustomOptions) { o.Policy = own }).Policy()); diff != "" {
		t.Errorf("Policy() with options mismatch (-want +got):\n%s", diff)
	}
	if st.custom().RequiresKey() {
		t.Error("RequiresKey() = true, want false: a gateway may need no key")
	}
}

func TestCustom_listModels(t *testing.T) {
	t.Parallel()
	st := newStub(t, func(_ int, w http.ResponseWriter, _ *http.Request) {
		reply(w, 200, `{"data":[{"id":"zeta"},{"id":"alpha","created":5},{"id":"zeta"},{"id":" "},{"id":""},{"id":"gpt-4.1","pricing":{"prompt":"1","completion":"2"}}]}`)
	})
	got, err := st.custom().ListModels(t.Context(), "")
	if err != nil {
		t.Fatalf("ListModels() error = %v", err)
	}
	var ids []string
	for _, m := range got {
		ids = append(ids, m.ID)
		if m.Provider != customID || m.Pricing != nil {
			t.Errorf("model %+v, want provider %s and no pricing (prices a gateway lists are ignored)", m, customID)
		}
	}
	if want := []string{"zeta", "alpha", "gpt-4.1"}; !slices.Equal(ids, want) {
		t.Errorf("ListModels() ids = %v, want %v (upstream order, no duplicates or blanks)", ids, want)
	}
	if st.paths[0] != "GET /v1/models" || st.headers[0].Get("Authorization") != "Bearer "+testKey {
		t.Errorf("request = %s with Authorization %q, want GET /v1/models with the bearer key", st.paths[0], st.headers[0].Get("Authorization"))
	}
}

func TestCustom_listModelsUnavailable(t *testing.T) {
	t.Parallel()
	for _, status := range []int{404, 405, 401, 500} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			t.Parallel()
			st := newStub(t, func(_ int, w http.ResponseWriter, _ *http.Request) { reply(w, status, `{"error":{"message":"nope"}}`) })
			_, err := st.custom().ListModels(t.Context(), "")
			if got := statusOf(err); got != status {
				t.Errorf("ListModels() StatusCode = %d (%v), want %d", got, err, status)
			}
		})
	}
}

func TestCustom_listModelsUnparsable(t *testing.T) {
	t.Parallel()
	st := newStub(t, func(_ int, w http.ResponseWriter, _ *http.Request) { reply(w, 200, `<html>`) })
	if _, err := st.custom().ListModels(t.Context(), ""); err == nil || statusOf(err) != 0 {
		t.Errorf("ListModels() error = %v, want a non-status error for an unparsable body", err)
	}
}

func TestCustom_cancelStopsRequest(t *testing.T) {
	t.Parallel()
	started := make(chan struct{})
	st := newStub(t, func(_ int, _ http.ResponseWriter, r *http.Request) {
		close(started)
		<-r.Context().Done()
	})
	ctx, cancel := context.WithCancel(t.Context())
	errc := make(chan error, 1)
	go func() {
		_, err := st.custom().Complete(ctx, "", attempt(custom.ModeStrict))
		errc <- err
	}()
	<-started
	cancel()
	if err := <-errc; !errors.Is(err, context.Canceled) {
		t.Errorf("Complete() error = %v, want context.Canceled", err)
	}
}

package settingshttp_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
)

func strp(s string) *string { return &s }

// modelsHits counts the requests the stub saw on /or/models.
func (u *upstream) modelsHits() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	n := 0
	for _, p := range u.paths {
		if p == "/or/models" {
			n++
		}
	}
	return n
}

func TestTestConnection_checkedModel(t *testing.T) { // AC4, FR5, EC2
	tests := []struct {
		name         string
		defaultModel string // JSON of the PUT body value, "" for unset
		want         map[string]any
	}{
		{name: "no default checks the reference model", want: map[string]any{
			"provider": "openrouter", "ok": true, "message": "OK — 2 models available",
			"model": "deepseek/deepseek-v4-flash", "model_reachable": true}},
		{name: "default for the provider is checked", defaultModel: `{"provider":"openrouter","model":"a/y"}`, want: map[string]any{
			"provider": "openrouter", "ok": true, "message": "OK — 2 models available", "model": "a/y", "model_reachable": false}},
		{name: "default that is listed is reachable", defaultModel: `{"provider":"openrouter","model":"a/x"}`, want: map[string]any{
			"provider": "openrouter", "ok": true, "message": "OK — 2 models available", "model": "a/x", "model_reachable": true}},
		{name: "default of another provider is ignored", defaultModel: `{"provider":"openai","model":"gpt-5"}`, want: map[string]any{
			"provider": "openrouter", "ok": true, "message": "OK — 2 models available",
			"model": "deepseek/deepseek-v4-flash", "model_reachable": true}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := newStack(t, stackOpts{seed: true})
			s.up.mu.Lock()
			s.up.orIDs = []string{"a/x", "deepseek/deepseek-v4-flash"}
			s.up.mu.Unlock()
			if tc.defaultModel != "" {
				if rec := s.call(http.MethodPut, "/settings", `{"default_model":`+tc.defaultModel+`}`); rec.Code != http.StatusOK {
					t.Fatalf("PUT default_model status = %d (%s)", rec.Code, rec.Body)
				}
			}
			rec := s.call(http.MethodPost, "/settings/test-connection", `{"provider":"openrouter","key":"or-key"}`)
			if got := withoutLatency(t, rec); rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", rec.Code)
			} else if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("POST test-connection mismatch (-want +got):\n%s", diff)
			}
			if n := s.up.modelsHits(); n != 1 {
				t.Errorf("stub saw %d GET /models, want exactly 1", n)
			}
			s.up.mu.Lock()
			defer s.up.mu.Unlock()
			for _, p := range s.up.paths {
				if p != "/or/models" {
					t.Errorf("stub saw a request to %q, want only /or/models", p)
				}
			}
		})
	}
}

func TestTestConnection_unprovisionedDatabaseUsesReferenceModel(t *testing.T) { // A3
	s := newStack(t, stackOpts{seed: false})
	rec := s.call(http.MethodPost, "/settings/test-connection", `{"provider":"openrouter","key":"or-key"}`)
	got := withoutLatency(t, rec)
	if got["model"] != "deepseek/deepseek-v4-flash" || got["ok"] != true {
		t.Errorf("POST test-connection = %v, want ok with the reference model", got)
	}
	if !strings.Contains(s.logs.String(), "level=WARN") {
		t.Errorf("logs = %q, want a WARN for the missing workspace", s.logs)
	}
}

func TestTestConnection_failuresHaveNullExtras(t *testing.T) { // AC5, EC1, NFR1
	const key = "or-secret-key-123"
	var otherHits atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { otherHits.Add(1) }))
	t.Cleanup(other.Close)

	tests := []struct {
		name  string
		setup func(u *upstream)
	}{
		{"redirect to another host", func(u *upstream) { u.orRedirect = other.URL + "/models" }},
		{"server error", func(u *upstream) { u.orStatus = http.StatusInternalServerError }},
		{"timeout", func(u *upstream) { u.orDelay = 2 * time.Second }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := newStack(t, stackOpts{seed: true, openRouterTimeout: 100 * time.Millisecond})
			s.up.mu.Lock()
			tc.setup(s.up)
			s.up.mu.Unlock()

			rec := s.call(http.MethodPost, "/settings/test-connection", `{"provider":"openrouter","key":"`+key+`"}`)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", rec.Code)
			}
			got := jsonMap(t, rec)
			if got["ok"] != false {
				t.Errorf("ok = %v, want false (%s)", got["ok"], rec.Body)
			}
			for _, field := range []string{"latency_ms", "model", "model_reachable"} {
				if v, has := got[field]; !has || v != nil {
					t.Errorf("%s = %v (present %v), want present and null", field, v, has)
				}
			}
			for _, field := range []string{"login", "scopes"} {
				if _, has := got[field]; has {
					t.Errorf("%s is present on an LLM answer: %s", field, rec.Body)
				}
			}
			if strings.Contains(rec.Body.String(), key) || strings.Contains(s.logs.String(), key) {
				t.Errorf("the key leaked: response %s, logs %s", rec.Body, s.logs)
			}
			if n := otherHits.Load(); n != 0 {
				t.Errorf("the redirect target received %d requests, want 0", n)
			}
			if !strings.Contains(s.logs.String(), "ok=false") {
				t.Errorf("logs = %q, want the NFR6 line with ok=false", s.logs)
			}
		})
	}
}

func TestTestConnection_githubScopes(t *testing.T) { // AC6, FR16, EC3
	tests := []struct {
		name       string
		scopes     *string
		wantScopes any
	}{
		{"scopes listed", strp("repo, read:org"), []any{"repo", "read:org"}},
		{"header present and empty", strp(""), []any{}},
		{"header absent", nil, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := newStack(t, stackOpts{seed: true})
			s.up.mu.Lock()
			s.up.ghScopes = tc.scopes
			s.up.mu.Unlock()
			rec := s.call(http.MethodPost, "/settings/test-connection", `{"provider":"github","key":"ghp_test"}`)
			want := map[string]any{"provider": "github", "ok": true, "message": "Connected as @octo", "login": "octo", "scopes": tc.wantScopes}
			if got := withoutLatency(t, rec); rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", rec.Code)
			} else if diff := cmp.Diff(want, got); diff != "" {
				t.Errorf("POST test-connection mismatch (-want +got):\n%s", diff)
			}
			if strings.Contains(rec.Body.String(), "ghp_test") || strings.Contains(s.logs.String(), "ghp_test") {
				t.Errorf("the token leaked: response %s, logs %s", rec.Body, s.logs)
			}
		})
	}
}

func TestPutSettings_defaultModelAndSpendCap(t *testing.T) { // AC7, EC10
	s := newStack(t, stackOpts{seed: true})
	get := func() map[string]any { return jsonMap(t, s.call(http.MethodGet, "/settings", "")) }

	put := func(body string) int { return s.call(http.MethodPut, "/settings", body).Code }
	if code := put(`{"default_model":{"provider":"openrouter","model":"a/x"},"monthly_spend_cap_usd":50}`); code != http.StatusOK {
		t.Fatalf("PUT status = %d, want 200", code)
	}
	got := get()
	if diff := cmp.Diff(map[string]any{"provider": "openrouter", "model": "a/x"}, got["default_model"]); diff != "" {
		t.Errorf("default_model mismatch (-want +got):\n%s", diff)
	}
	if got["monthly_spend_cap_usd"] != 50.0 {
		t.Errorf("monthly_spend_cap_usd = %v, want 50", got["monthly_spend_cap_usd"])
	}

	if code := put(`{"monthly_spend_cap_usd":0}`); code != http.StatusOK || get()["monthly_spend_cap_usd"] != 0.0 {
		t.Errorf("a cap of 0 was not stored: status %d, value %v", code, get()["monthly_spend_cap_usd"])
	}

	for _, body := range []string{`{"monthly_spend_cap_usd":-1}`, `{"monthly_spend_cap_usd":1000000.01}`,
		`{"monthly_spend_cap_usd":1.234}`, `{"monthly_spend_cap_usd":"50"}`, `{"default_model":{"provider":"foo"}}`} {
		if code := put(body); code != http.StatusUnprocessableEntity {
			t.Errorf("PUT %s status = %d, want 422", body, code)
		}
	}
	if got := get(); got["monthly_spend_cap_usd"] != 0.0 || got["default_model"] == nil {
		t.Errorf("rejected bodies changed the stored settings: %v", got)
	}

	if code := put(`{"default_model":null,"monthly_spend_cap_usd":null}`); code != http.StatusOK {
		t.Fatalf("PUT null status = %d, want 200", code)
	}
	got = get()
	for _, k := range []string{"default_model", "monthly_spend_cap_usd"} {
		if v, has := got[k]; !has || v != nil {
			t.Errorf("%s = %v (present %v) after null, want present and null", k, v, has)
		}
	}
}

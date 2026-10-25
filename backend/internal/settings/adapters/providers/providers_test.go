package providers_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/platform/retry"
	"dev-digest-go/backend/internal/settings/adapters/providers"
	"dev-digest-go/backend/internal/settings/app"
)

func stub(t *testing.T, h http.HandlerFunc) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(h)
	t.Cleanup(s.Close)
	return s
}

func models(w http.ResponseWriter, ids ...string) {
	var b strings.Builder
	b.WriteString(`{"data":[`)
	for i, id := range ids {
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString(`{"id":"` + id + `"}`)
	}
	b.WriteString(`]}`)
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(b.String()))
}

var fastRetry = retry.Options{Retries: 3, BaseDelay: time.Millisecond, MaxDelay: 2 * time.Millisecond}

func TestCountModels_openAIFiltersIDs(t *testing.T) {
	s := stub(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" || r.Header.Get("Authorization") != "Bearer sk-1" {
			t.Errorf("request = %s auth %q, want GET /models with the bearer key", r.URL.Path, r.Header.Get("Authorization"))
		}
		models(w, "gpt-4.1", "text-embedding-3-small", "o1-mini", "o3", "whisper-1", "chatgpt-4o")
	})
	c := providers.New(providers.Options{OpenAIBaseURL: s.URL})
	got, err := c.CountModels(t.Context(), app.OpenAI, "sk-1")
	if err != nil || got != 3 {
		t.Errorf("CountModels(openai) = %d, %v, want 3, nil", got, err)
	}
}

func TestCountModels_anthropicHeadersAndCount(t *testing.T) {
	s := stub(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-api-key") != "ak-1" || r.Header.Get("anthropic-version") == "" {
			t.Errorf("headers x-api-key=%q anthropic-version=%q", r.Header.Get("x-api-key"), r.Header.Get("anthropic-version"))
		}
		models(w, "claude-a", "claude-b")
	})
	c := providers.New(providers.Options{AnthropicBaseURL: s.URL})
	got, err := c.CountModels(t.Context(), app.Anthropic, "ak-1")
	if err != nil || got != 2 {
		t.Errorf("CountModels(anthropic) = %d, %v, want 2, nil", got, err)
	}
}

func TestCountModels_openRouter(t *testing.T) {
	t.Run("counts every model", func(t *testing.T) {
		s := stub(t, func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "Bearer or-1" {
				t.Errorf("Authorization = %q", r.Header.Get("Authorization"))
			}
			models(w, "a/b", "c/d", "e/f")
		})
		c := providers.New(providers.Options{OpenRouterBaseURL: s.URL})
		got, err := c.CountModels(t.Context(), app.OpenRouter, "or-1")
		if err != nil || got != 3 {
			t.Errorf("CountModels(openrouter) = %d, %v, want 3, nil", got, err)
		}
	})
	t.Run("non-2xx has the status only and is not retried", func(t *testing.T) {
		var calls atomic.Int32
		s := stub(t, func(w http.ResponseWriter, r *http.Request) {
			calls.Add(1)
			http.Error(w, `{"error":{"message":"bad key or-1"}}`, http.StatusServiceUnavailable)
		})
		c := providers.New(providers.Options{OpenRouterBaseURL: s.URL, Retry: &fastRetry})
		_, err := c.CountModels(t.Context(), app.OpenRouter, "or-1")
		if err == nil || err.Error() != "OpenRouter /models returned 503" {
			t.Errorf("CountModels(openrouter) error = %v, want %q", err, "OpenRouter /models returned 503")
		}
		if n := calls.Load(); n != 1 {
			t.Errorf("calls = %d, want 1 (no retry)", n)
		}
	})
}

func TestCountModels_authFailureCarriesFixedMessage(t *testing.T) {
	s := stub(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"Incorrect API key provided: sk-abcdef"}}`))
	})
	c := providers.New(providers.Options{OpenAIBaseURL: s.URL, Retry: &fastRetry})
	_, err := c.CountModels(t.Context(), app.OpenAI, "sk-abcdef")
	if err == nil {
		t.Fatal("CountModels() error = nil, want 401 error")
	}
	if got, want := err.Error(), "OpenAI /models returned 401: API key rejected by the provider"; got != want {
		t.Errorf("error = %q, want %q", got, want)
	}
}

func TestCountModels_retriesTransientThenSucceeds(t *testing.T) {
	var calls atomic.Int32
	s := stub(t, func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) < 3 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		models(w, "gpt-4")
	})
	c := providers.New(providers.Options{OpenAIBaseURL: s.URL, Retry: &fastRetry})
	got, err := c.CountModels(t.Context(), app.OpenAI, "k")
	if err != nil || got != 1 {
		t.Errorf("CountModels() = %d, %v, want 1, nil", got, err)
	}
	if n := calls.Load(); n != 3 {
		t.Errorf("calls = %d, want 3", n)
	}
}

func TestCountModels_authErrorIsNotRetried(t *testing.T) {
	var calls atomic.Int32
	s := stub(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusForbidden)
	})
	c := providers.New(providers.Options{AnthropicBaseURL: s.URL, Retry: &fastRetry})
	if _, err := c.CountModels(t.Context(), app.Anthropic, "k"); err == nil {
		t.Error("CountModels() error = nil, want 403 error")
	}
	if n := calls.Load(); n != 1 {
		t.Errorf("calls = %d, want 1", n)
	}
}

func TestCountModels_timeout(t *testing.T) {
	release := make(chan struct{})
	s := stub(t, func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	})
	t.Cleanup(func() { close(release) })
	c := providers.New(providers.Options{OpenRouterBaseURL: s.URL, OpenRouterTimeout: 50 * time.Millisecond})
	if _, err := c.CountModels(t.Context(), app.OpenRouter, "k"); err == nil {
		t.Error("CountModels() error = nil, want a timeout error")
	}
}

func TestCountModels_badBodyAndUnknownProvider(t *testing.T) {
	s := stub(t, func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(`<html>`)) })
	c := providers.New(providers.Options{OpenRouterBaseURL: s.URL})
	if _, err := c.CountModels(t.Context(), app.OpenRouter, "k"); err == nil {
		t.Error("CountModels(bad body) error = nil, want an error")
	}
	if _, err := c.CountModels(t.Context(), app.GitHub, "k"); err == nil {
		t.Error("CountModels(github) error = nil, want an error")
	}
}

func TestCountModels_redirectIsNotFollowed(t *testing.T) {
	var reached atomic.Int32
	other := stub(t, func(w http.ResponseWriter, r *http.Request) {
		reached.Add(1)
		models(w, "claude-a")
	})
	s := stub(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+"/models", http.StatusTemporaryRedirect)
	})
	c := providers.New(providers.Options{AnthropicBaseURL: s.URL, Retry: &fastRetry})
	if _, err := c.CountModels(t.Context(), app.Anthropic, "ak-secret"); err == nil {
		t.Errorf("CountModels(anthropic) after a 307 = nil error, want a failure")
	}
	if n := reached.Load(); n != 0 {
		t.Errorf("redirect target got %d request(s), want 0: the key must not follow a redirect", n)
	}
}

func TestListModels(t *testing.T) {
	tests := []struct {
		name      string
		provider  app.Provider
		opts      func(url string) providers.Options
		wantQuery string
		wantAuth  func(*http.Request) bool
		ids       []string
		want      app.ModelList
	}{
		{
			name:      "openai keeps every id and counts the chat ones",
			provider:  app.OpenAI,
			opts:      func(u string) providers.Options { return providers.Options{OpenAIBaseURL: u} },
			wantQuery: "",
			wantAuth:  func(r *http.Request) bool { return r.Header.Get("Authorization") == "Bearer k-1" },
			ids:       []string{"gpt-4.1", "whisper-1", "o3"},
			want:      app.ModelList{IDs: []string{"gpt-4.1", "whisper-1", "o3"}, Count: 2},
		},
		{
			name:      "anthropic asks for one page of 1000",
			provider:  app.Anthropic,
			opts:      func(u string) providers.Options { return providers.Options{AnthropicBaseURL: u} },
			wantQuery: "limit=1000",
			wantAuth: func(r *http.Request) bool {
				return r.Header.Get("x-api-key") == "k-1" && r.Header.Get("anthropic-version") != ""
			},
			ids:  []string{"claude-a", "claude-b"},
			want: app.ModelList{IDs: []string{"claude-a", "claude-b"}, Count: 2},
		},
		{
			name:      "openrouter lists everything",
			provider:  app.OpenRouter,
			opts:      func(u string) providers.Options { return providers.Options{OpenRouterBaseURL: u} },
			wantQuery: "",
			wantAuth:  func(r *http.Request) bool { return r.Header.Get("Authorization") == "Bearer k-1" },
			ids:       []string{"a/b", "c/d", "e/f"},
			want:      app.ModelList{IDs: []string{"a/b", "c/d", "e/f"}, Count: 3},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			s := stub(t, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.Method != http.MethodGet || r.URL.Path != "/models" || r.URL.RawQuery != tc.wantQuery || !tc.wantAuth(r) {
					t.Errorf("request = %s %s?%s, want GET /models?%s with the key", r.Method, r.URL.Path, r.URL.RawQuery, tc.wantQuery)
				}
				models(w, tc.ids...)
			})
			c := providers.New(tc.opts(s.URL))
			got, err := c.ListModels(t.Context(), tc.provider, "k-1")
			if err != nil {
				t.Fatalf("ListModels(%s) error = %v, want nil", tc.provider, err)
			}
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("ListModels(%s) mismatch (-want +got):\n%s", tc.provider, diff)
			}
			if n := calls.Load(); n != 1 {
				t.Errorf("ListModels(%s) made %d requests, want 1", tc.provider, n)
			}
		})
	}
}

func TestListModels_redirectNotFollowedAndKeyNotInError(t *testing.T) {
	const key = "sk-secret-key"
	for _, tc := range []struct {
		provider app.Provider
		opts     func(url string) providers.Options
	}{
		{app.OpenAI, func(u string) providers.Options { return providers.Options{OpenAIBaseURL: u, Retry: &fastRetry} }},
		{app.Anthropic, func(u string) providers.Options { return providers.Options{AnthropicBaseURL: u, Retry: &fastRetry} }},
		{app.OpenRouter, func(u string) providers.Options { return providers.Options{OpenRouterBaseURL: u, Retry: &fastRetry} }},
	} {
		t.Run(string(tc.provider), func(t *testing.T) {
			var reached, calls atomic.Int32
			other := stub(t, func(w http.ResponseWriter, r *http.Request) {
				reached.Add(1)
				models(w, "m")
			})
			s := stub(t, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				http.Redirect(w, r, other.URL+"/models", http.StatusFound)
			})
			c := providers.New(tc.opts(s.URL))
			got, err := c.ListModels(t.Context(), tc.provider, key)
			if err == nil {
				t.Fatalf("ListModels(%s) after a 302 = %+v, nil error, want a failure", tc.provider, got)
			}
			if strings.Contains(err.Error(), key) {
				t.Errorf("ListModels(%s) error %q contains the key", tc.provider, err)
			}
			if n := reached.Load(); n != 0 {
				t.Errorf("redirect target got %d request(s), want 0: the key must not follow a redirect", n)
			}
			if n := calls.Load(); n != 1 {
				t.Errorf("ListModels(%s) made %d requests to the provider, want 1", tc.provider, n)
			}
		})
	}
}

func TestListModels_failuresDoNotLeakKey(t *testing.T) {
	const key = "sk-secret-key"
	s := stub(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":{"message":"bad key ` + key + `"}}`))
	})
	c := providers.New(providers.Options{AnthropicBaseURL: s.URL, Retry: &fastRetry})
	_, err := c.ListModels(t.Context(), app.Anthropic, key)
	if err == nil || strings.Contains(err.Error(), key) {
		t.Errorf("ListModels(anthropic) error = %v, want a 403 error without the key", err)
	}
	if _, err := c.ListModels(t.Context(), app.GitHub, key); err == nil {
		t.Error("ListModels(github) error = nil, want an error")
	}
}

// TestListModels_errorTextNeverCarriesKeyFragments covers NFR1 ("no key value or part of one in any
// response"): providers echo masked copies of the key (prefix and last 4) in their error text.
func TestListModels_errorTextNeverCarriesKeyFragments(t *testing.T) {
	const key = "sk-proj-AbCdEfGhIjKlMnOpQrSt9z8y7x6w"
	tests := []struct {
		name     string
		provider app.Provider
		status   int
		body     string
		want     string
		banned   []string
	}{
		{
			name: "openai 401 masked echo with prefix and last 4", provider: app.OpenAI, status: http.StatusUnauthorized,
			body:   `{"error":{"message":"Incorrect API key provided: sk-proj-********************7x6w. You can find your API key at https://platform.openai.com/account/api-keys."}}`,
			want:   "OpenAI /models returned 401: API key rejected by the provider",
			banned: []string{"sk-", "7x6w", "proj", "*"},
		},
		{
			name: "openai 401 unicode mask", provider: app.OpenAI, status: http.StatusUnauthorized,
			body:   `{"error":{"message":"Incorrect API key provided: sk-proj-****…7x6w"}}`,
			want:   "OpenAI /models returned 401: API key rejected by the provider",
			banned: []string{"sk-", "7x6w", "…"},
		},
		{
			name: "openai 401 full key echo", provider: app.OpenAI, status: http.StatusUnauthorized,
			body:   `{"error":{"message":"Incorrect API key provided: ` + key + `"}}`,
			want:   "OpenAI /models returned 401: API key rejected by the provider",
			banned: []string{key, "sk-", "AbCd"},
		},
		{
			name: "anthropic 403 full key echo", provider: app.Anthropic, status: http.StatusForbidden,
			body:   `{"error":{"type":"permission_error","message":"bad key ` + key + `"}}`,
			want:   "Anthropic /models returned 403: API key rejected by the provider",
			banned: []string{key, "sk-"},
		},
		{
			name: "openai 500 keeps its text minus the full key", provider: app.OpenAI, status: http.StatusInternalServerError,
			body:   `{"error":{"message":"upstream failed for ` + key + ` twice"}}`,
			want:   "OpenAI /models returned 500: upstream failed for [REDACTED] twice",
			banned: []string{key, "sk-"},
		},
		{
			name: "anthropic 500 masks a masked echo and a last-4 fragment", provider: app.Anthropic, status: http.StatusInternalServerError,
			body:   `{"error":{"message":"overloaded; key sk-ant-****…7x6w seen, tail 7x6w9 retry later"}}`,
			want:   "Anthropic /models returned 500: overloaded; key [REDACTED] seen, tail [REDACTED]9 retry later",
			banned: []string{"sk-", "7x6w", "…"},
		},
		{
			name: "openai 500 keeps plain text", provider: app.OpenAI, status: http.StatusInternalServerError,
			body: `{"error":{"message":"The server had an error while processing your request"}}`,
			want: "OpenAI /models returned 500: The server had an error while processing your request",
		},
		{
			name: "openai 500 words are not mistaken for key prefixes", provider: app.OpenAI, status: http.StatusInternalServerError,
			body: `{"error":{"message":"task-queue is full"}}`,
			want: "OpenAI /models returned 500: task-queue is full",
		},
		{
			name: "openrouter 401 carries only the fixed message", provider: app.OpenRouter, status: http.StatusUnauthorized,
			body:   `{"error":{"message":"No auth credentials found sk-or-v1-****abcd"}}`,
			want:   "OpenRouter /models returned 401: API key rejected by the provider",
			banned: []string{"sk-", "abcd"},
		},
		{
			name: "openrouter 500 carries only the status", provider: app.OpenRouter, status: http.StatusInternalServerError,
			body: `{"error":{"message":"` + key + `"}}`,
			want: "OpenRouter /models returned 500",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := stub(t, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			})
			c := providers.New(providers.Options{
				OpenAIBaseURL: s.URL, AnthropicBaseURL: s.URL, OpenRouterBaseURL: s.URL, Retry: &fastRetry,
			})
			_, err := c.ListModels(t.Context(), tc.provider, key)
			if err == nil {
				t.Fatalf("ListModels(%v) error = nil, want a %d error", tc.provider, tc.status)
			}
			got := err.Error()
			if got != tc.want {
				t.Errorf("ListModels(%v) error = %q, want %q", tc.provider, got, tc.want)
			}
			for _, b := range tc.banned {
				if strings.Contains(got, b) {
					t.Errorf("ListModels(%v) error = %q, must not contain %q", tc.provider, got, b)
				}
			}
		})
	}
}

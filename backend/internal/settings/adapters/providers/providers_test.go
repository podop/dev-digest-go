package providers_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

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

func TestCountModels_authFailureCarriesScrubbedMessage(t *testing.T) {
	s := stub(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"Incorrect API key provided: sk-abcdef"}}`))
	})
	c := providers.New(providers.Options{OpenAIBaseURL: s.URL, Retry: &fastRetry})
	_, err := c.CountModels(t.Context(), app.OpenAI, "sk-abcdef")
	if err == nil {
		t.Fatal("CountModels() error = nil, want 401 error")
	}
	if got, want := err.Error(), "OpenAI /models returned 401: Incorrect API key provided: [REDACTED]"; got != want {
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

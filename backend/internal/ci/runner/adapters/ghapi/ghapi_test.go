package ghapi_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"dev-digest-go/backend/internal/ci/runner/adapters/ghapi"
	"dev-digest-go/backend/internal/ci/runner/app"
)

const token = "ghp_0123456789abcdef"

func serve(t *testing.T, h http.HandlerFunc) *ghapi.Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return ghapi.New(ghapi.Options{BaseURL: srv.URL})
}

var pr = app.PullRef{Owner: "o", Repo: "r", Number: 3}

func TestErrorBody_RedactedBeforeTheCutAndCutOnACharacter(t *testing.T) {
	// the token straddles byte 1000, and the 1000th byte is inside a three-byte character
	body := strings.Repeat("a", 990) + token + strings.Repeat("€", 100)
	c := serve(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = io.WriteString(w, body)
	})
	_, err := c.FetchDiff(t.Context(), token, pr)
	var apiErr *app.APIError
	if !errors.As(err, &apiErr) || apiErr.Status != 502 {
		t.Fatalf("FetchDiff() error = %v, want an *APIError with status 502", err)
	}
	msg := apiErr.Message
	if strings.Contains(msg, token) || strings.Contains(msg, "ghp_") {
		t.Errorf("message holds the token or a piece of it: %q", msg)
	}
	text := msg[strings.Index(msg, "): 502 ")+len("): 502 "):]
	if len(text) > 1000 || !strings.Contains(text, "[REDACTED]") || !utf8.ValidString(text) {
		t.Errorf("body text is %d bytes (valid UTF-8: %v), want at most 1000, valid, with [REDACTED]", len(text), utf8.ValidString(text))
	}
}

func TestFetchDiff_TransportErrorHasNoToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := srv.URL
	srv.Close() // connection refused
	c := ghapi.New(ghapi.Options{BaseURL: url})
	_, err := c.FetchDiff(t.Context(), token, pr)
	if err == nil || strings.Contains(err.Error(), token) || !strings.HasPrefix(err.Error(), "GitHub API error fetching PR diff ("+url+"/repos/o/r/pulls/3): ") {
		t.Errorf("FetchDiff() error = %v, want a transport error naming the URL and not the token", err)
	}
}

func TestPost_BodyIsNotHTMLEscaped(t *testing.T) {
	var got string
	c := serve(t, func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got = string(b)
		w.WriteHeader(http.StatusCreated)
	})
	if err := c.PostComment(t.Context(), token, pr, "a <b> & c"); err != nil {
		t.Fatal(err)
	}
	if want := `{"body":"a <b> & c"}`; got != want {
		t.Errorf("request body = %s, want %s", got, want)
	}
}

func TestRequestsHonourTheContext(t *testing.T) {
	c := serve(t, func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
	})
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := c.FetchDiff(ctx, token, pr); err == nil {
		t.Error("FetchDiff() error = nil, want a context error")
	}
	if time.Since(start) > 3*time.Second {
		t.Error("FetchDiff() did not return when the context expired")
	}
}

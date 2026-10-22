package github_test

import (
	"encoding/base64"
	"errors"
	"net/http"
	"strings"
	"testing"

	"dev-digest-go/backend/internal/platform/github"
	"dev-digest-go/backend/internal/platform/retry"
)

func TestGetContent(t *testing.T) {
	t.Parallel()
	b64 := func(s string) string {
		enc := base64.StdEncoding.EncodeToString([]byte(s))
		// GitHub wraps the payload in lines of 60 characters.
		var sb strings.Builder
		for len(enc) > 60 {
			sb.WriteString(enc[:60] + "\n")
			enc = enc[60:]
		}
		sb.WriteString(enc + "\n")
		return sb.String()
	}
	long := strings.Repeat("héllo wörld ", 20)
	tests := []struct {
		name      string
		max       int64
		status    int
		body      any
		wantFound bool
		want      string
	}{
		{"70000 reported bytes over the cap", 65536, 200, map[string]any{"type": "file", "size": 70000, "encoding": "base64", "content": "!!not base64!!"}, false, ""},
		{"exactly the cap", 65536, 200, map[string]any{"type": "file", "size": 65536, "encoding": "base64", "content": b64("ok")}, true, "ok"},
		{"404", 65536, 404, map[string]any{"message": "Not Found"}, false, ""},
		{"directory", 65536, 200, []any{map[string]any{"type": "file", "name": "a"}}, false, ""},
		{"symlink", 65536, 200, map[string]any{"type": "symlink", "size": 3, "content": "x"}, false, ""},
		{"submodule", 65536, 200, map[string]any{"type": "submodule", "size": 0}, false, ""},
		{"no content", 65536, 200, map[string]any{"type": "file", "size": 3, "encoding": "none", "content": nil}, false, ""},
		{"base64 file with line breaks", 65536, 200, map[string]any{"type": "file", "size": len(long), "encoding": "base64", "content": b64(long)}, true, long},
		{"empty file", 65536, 200, map[string]any{"type": "file", "size": 0, "encoding": "base64", "content": ""}, true, ""},
		{"text encoding taken as is", 65536, 200, map[string]any{"type": "file", "size": 5, "encoding": "utf-8", "content": "plain"}, true, "plain"},
		{"no cap", 0, 200, map[string]any{"type": "file", "size": 70000, "encoding": "base64", "content": b64("big")}, true, "big"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := newStub(t, func(w http.ResponseWriter, r *http.Request) { writeJSON(w, tc.status, tc.body) })
			got, found, err := newClient(s, github.Options{}).GetContent(t.Context(), "acme", "api", "docs/a.md", "abc123", tc.max)
			if err != nil {
				t.Fatalf("GetContent(max %d) error: %v", tc.max, err)
			}
			if found != tc.wantFound || string(got) != tc.want {
				t.Errorf("GetContent(max %d) = %q, %v, want %q, %v", tc.max, got, found, tc.want, tc.wantFound)
			}
			if s.count() != 1 {
				t.Fatalf("GetContent(max %d) made %d requests, want 1", tc.max, s.count())
			}
			req, _ := s.request(0)
			if req.URL.Path != "/repos/acme/api/contents/docs/a.md" || req.URL.Query().Get("ref") != "abc123" {
				t.Errorf("request = %q, want /repos/acme/api/contents/docs/a.md?ref=abc123", req.URL.String())
			}
			if got := req.Header.Get("Authorization"); got != "Bearer "+testToken {
				t.Errorf("Authorization = %q, want the bearer token", got)
			}
		})
	}
}

func TestGetContent_keepsInvalidUTF8Bytes(t *testing.T) {
	t.Parallel()
	raw := []byte{'a', 0xff, 0xe2, 0x82, 'z'}
	s := newStub(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"type": "file", "size": len(raw), "encoding": "base64", "content": base64.StdEncoding.EncodeToString(raw)})
	})
	got, found, err := newClient(s, github.Options{}).GetContent(t.Context(), "o", "r", "f.bin", "abc123", 100)
	if err != nil || !found || string(got) != string(raw) {
		t.Errorf("GetContent() = %x, %v, %v, want %x, true, nil", got, found, err, raw)
	}
}

func TestGetContent_escapesThePath(t *testing.T) {
	t.Parallel()
	s := newStub(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 404, map[string]any{"message": "Not Found"})
	})
	_, found, err := newClient(s, github.Options{}).GetContent(t.Context(), "o", "r", "d i/r?x#y/f%2e.md", "abc123", 10)
	if err != nil || found {
		t.Fatalf("GetContent() = _, %v, %v, want _, false, nil", found, err)
	}
	req, _ := s.request(0)
	if got, want := req.URL.EscapedPath(), "/repos/o/r/contents/d%20i/r%3Fx%23y/f%252e.md"; got != want {
		t.Errorf("escaped path = %q, want %q", got, want)
	}
	if req.URL.Fragment != "" || req.URL.Query().Get("ref") != "abc123" {
		t.Errorf("request URL = %q, want ref=abc123 and no fragment", req.URL.String())
	}
}

func TestGetContent_errors(t *testing.T) {
	t.Parallel()
	t.Run("no token", func(t *testing.T) {
		t.Parallel()
		s := newStub(t, func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, map[string]any{}) })
		c := newClient(s, github.Options{Token: func() string { return "" }})
		if _, _, err := c.GetContent(t.Context(), "o", "r", "a.md", "abc123", 10); !errors.Is(err, github.ErrNoToken) {
			t.Errorf("GetContent() error = %v, want ErrNoToken", err)
		}
		if s.count() != 0 {
			t.Errorf("GetContent() made %d requests without a token, want 0", s.count())
		}
	})
	t.Run("403 is an error and carries no token", func(t *testing.T) {
		t.Parallel()
		s := newStub(t, func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, 403, map[string]any{"message": "denied " + testToken})
		})
		_, _, err := newClient(s, github.Options{}).GetContent(t.Context(), "o", "r", "a.md", "abc123", 10)
		var se *github.StatusError
		if !errors.As(err, &se) || se.Status != 403 || strings.Contains(err.Error(), testToken) {
			t.Errorf("GetContent() error = %v, want StatusError 403 without the token", err)
		}
	})
	t.Run("500 is retried then fails", func(t *testing.T) {
		t.Parallel()
		s := newStub(t, func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 500, map[string]any{"message": "boom"}) })
		c := newClient(s, github.Options{Retry: &retry.Options{Retries: 2, BaseDelay: 1, MaxDelay: 2}})
		if _, _, err := c.GetContent(t.Context(), "o", "r", "a.md", "abc123", 10); err == nil {
			t.Error("GetContent() on 500 error = nil, want an error")
		}
		if s.count() != 3 {
			t.Errorf("GetContent() on 500 made %d requests, want 3 (1 + 2 retries)", s.count())
		}
	})
}

func TestGetContent_neverFollowsARedirect(t *testing.T) {
	t.Parallel()
	elsewhere := newStub(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"type": "file", "size": 1, "encoding": "utf-8", "content": "x"})
	})
	s := newStub(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, elsewhere.URL+"/steal", http.StatusFound)
	})
	_, found, err := newClient(s, github.Options{}).GetContent(t.Context(), "o", "r", "a.md", "abc123", 10)
	var se *github.StatusError
	if found || !errors.As(err, &se) || se.Status != http.StatusFound {
		t.Errorf("GetContent() = _, %v, %v, want _, false, StatusError 302", found, err)
	}
	if n := elsewhere.count(); n != 0 {
		t.Errorf("the redirect target received %d requests, want 0", n)
	}
	if n := s.count(); n != 1 {
		t.Errorf("GetContent() made %d requests, want 1 (3xx is not retried)", n)
	}
}

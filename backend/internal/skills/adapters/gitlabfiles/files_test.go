package gitlabfiles_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/google/go-cmp/cmp"

	glclient "dev-digest-go/backend/internal/platform/gitlab"
	"dev-digest-go/backend/internal/platform/retry"
	"dev-digest-go/backend/internal/skills/adapters/gitlabfiles"
	"dev-digest-go/backend/internal/skills/app"
)

const secret = "glpat-skill-import-secret"

type seen struct {
	uri   string
	token string
}

// stub is a TLS files API that records what it was asked and answers by handler.
type stub struct {
	*httptest.Server
	mu   sync.Mutex
	reqs []seen
}

func newStub(t *testing.T, h http.HandlerFunc) *stub {
	t.Helper()
	s := &stub{}
	s.Server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.reqs = append(s.reqs, seen{uri: r.RequestURI, token: r.Header.Get("PRIVATE-TOKEN")})
		s.mu.Unlock()
		h(w, r)
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *stub) requests() []seen {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]seen(nil), s.reqs...)
}

func (s *stub) adapter(binding glclient.Binding) *gitlabfiles.Adapter {
	c := glclient.New(glclient.Options{HTTP: s.Client(), Retry: &retry.Options{}})
	return gitlabfiles.New(c, func(_ context.Context, key string) (glclient.Binding, bool, error) {
		if key != binding.Host {
			return glclient.Binding{}, false, nil
		}
		return binding, true, nil
	})
}

func (s *stub) binding(token string) glclient.Binding {
	return glclient.Binding{ID: "c1", Name: "corp", BaseURL: s.URL, Host: "git.corp.example", Token: token}
}

func TestAdapter_MatchReturnsTheConnectionWithoutItsToken(t *testing.T) {
	s := newStub(t, nil)
	a := s.adapter(s.binding(secret))

	got, ok, err := a.Match(t.Context(), "git.corp.example")
	if err != nil || !ok {
		t.Fatalf("Match() = (%v, %v, %v), want the connection", got, ok, err)
	}
	want := app.GitLabFileConn{ID: "c1", Name: "corp", BaseURL: s.URL, Host: "git.corp.example"}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Match() mismatch (-want +got):\n%s", diff)
	}
	if _, ok, err := a.Match(t.Context(), "other.example"); ok || err != nil {
		t.Errorf("Match(other host) = (_, %v, %v), want no connection", ok, err)
	}
}

func TestAdapter_MatchResolveFailure(t *testing.T) {
	boom := errors.New("db down")
	a := gitlabfiles.New(glclient.New(glclient.Options{HTTP: http.DefaultClient}),
		func(context.Context, string) (glclient.Binding, bool, error) { return glclient.Binding{}, false, boom })
	if _, _, err := a.Match(t.Context(), "h"); !errors.Is(err, boom) {
		t.Errorf("Match() error = %v, want %v", err, boom)
	}
}

func TestAdapter_RawSendsTheTokenToTheConnectionsRestRoot(t *testing.T) {
	s := newStub(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("# skill")) })
	a := s.adapter(s.binding(secret))
	conn, _, _ := a.Match(t.Context(), "git.corp.example")

	got, err := a.Raw(t.Context(), conn, "acme/skills", "skills/a.md", "feature/x", 1024)
	if err != nil {
		t.Fatalf("Raw() error = %v", err)
	}
	if string(got) != "# skill" {
		t.Errorf("Raw() = %q, want the file", got)
	}
	want := []seen{{uri: "/api/v4/projects/acme%2Fskills/repository/files/skills%2Fa.md/raw?ref=feature%2Fx", token: secret}}
	if diff := cmp.Diff(want, s.requests(), cmp.AllowUnexported(seen{})); diff != "" {
		t.Errorf("requests mismatch (-want +got):\n%s", diff)
	}
}

func TestAdapter_RawWithoutATokenIsAnonymous(t *testing.T) {
	s := newStub(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("x")) })
	a := s.adapter(s.binding(""))
	conn, _, _ := a.Match(t.Context(), "git.corp.example")

	if _, err := a.Raw(t.Context(), conn, "g/p", "a.md", "main", 10); err != nil {
		t.Fatalf("Raw() error = %v", err)
	}
	if r := s.requests(); len(r) != 1 || r[0].token != "" {
		t.Errorf("requests = %+v, want one without a token", r)
	}
}

func TestAdapter_RawReadsTheTokenOfEveryCall(t *testing.T) {
	s := newStub(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("x")) })
	b := s.binding("first")
	c := glclient.New(glclient.Options{HTTP: s.Client(), Retry: &retry.Options{}})
	a := gitlabfiles.New(c, func(context.Context, string) (glclient.Binding, bool, error) { return b, true, nil })
	conn, _, _ := a.Match(t.Context(), b.Host)

	b.Token = "rotated"
	if _, err := a.Raw(t.Context(), conn, "g/p", "a.md", "main", 10); err != nil {
		t.Fatalf("Raw() error = %v", err)
	}
	if r := s.requests(); len(r) != 1 || r[0].token != "rotated" {
		t.Errorf("requests = %+v, want the rotated token", r)
	}
}

func TestAdapter_RawConnectionGone(t *testing.T) {
	tests := []struct {
		name string
		now  func(glclient.Binding) (glclient.Binding, bool)
	}{
		{"removed", func(glclient.Binding) (glclient.Binding, bool) { return glclient.Binding{}, false }},
		{"replaced", func(b glclient.Binding) (glclient.Binding, bool) { b.ID = "c2"; return b, true }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := newStub(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("x")) })
			b := s.binding(secret)
			calls := 0
			a := gitlabfiles.New(glclient.New(glclient.Options{HTTP: s.Client()}),
				func(context.Context, string) (glclient.Binding, bool, error) {
					calls++
					if calls == 1 {
						return b, true, nil
					}
					nb, ok := tc.now(b)
					return nb, ok, nil
				})
			conn, _, _ := a.Match(t.Context(), b.Host)

			_, err := a.Raw(t.Context(), conn, "g/p", "a.md", "main", 10)
			if !errors.Is(err, app.ErrGitLabFileConnectionGone) {
				t.Errorf("Raw() error = %v, want ErrGitLabFileConnectionGone", err)
			}
			if n := len(s.requests()); n != 0 {
				t.Errorf("Raw() sent %d requests, want none", n)
			}
		})
	}
}

func TestAdapter_RawErrors(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
		want   error
	}{
		{"not found", 404, `{"message":"404 File Not Found"}`, app.ErrGitLabFileNotFound},
		{"unauthorized", 401, `{"message":"401 Unauthorized"}`, app.ErrGitLabFileRejected},
		{"forbidden", 403, `{"message":"403 Forbidden"}`, app.ErrGitLabFileRejected},
		{"moved", 302, ``, app.ErrGitLabFileMoved},
		{"moved permanently", 301, ``, app.ErrGitLabFileMoved},
		{"rate limited", 429, `{"message":"slow down"}`, app.ErrGitLabFileRateLimited},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := newStub(t, func(w http.ResponseWriter, _ *http.Request) {
				if tc.status/100 == 3 {
					w.Header().Set("Location", "https://elsewhere.invalid/x")
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			})
			a := s.adapter(s.binding(secret))
			conn, _, _ := a.Match(t.Context(), "git.corp.example")

			_, err := a.Raw(t.Context(), conn, "g/p", "a.md", "main", 100)
			if !errors.Is(err, tc.want) {
				t.Fatalf("Raw() error = %v, want %v", err, tc.want)
			}
			if strings.Contains(err.Error(), secret) {
				t.Errorf("Raw() error %q contains the token", err)
			}
			if n := len(s.requests()); n != 1 {
				t.Errorf("Raw() made %d requests, want 1 (a redirect is not followed)", n)
			}
		})
	}
}

func TestAdapter_RawOtherStatus(t *testing.T) {
	s := newStub(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(500)
		_, _ = w.Write([]byte(`{"message":"boom ` + secret + `"}`))
	})
	a := s.adapter(s.binding(secret))
	conn, _, _ := a.Match(t.Context(), "git.corp.example")

	_, err := a.Raw(t.Context(), conn, "g/p", "a.md", "main", 100)
	var se *app.GitLabFileStatusError
	if !errors.As(err, &se) || se.Status != 500 {
		t.Fatalf("Raw() error = %v, want GitLabFileStatusError{500}", err)
	}
	if strings.Contains(err.Error(), secret) {
		t.Errorf("Raw() error %q contains the token", err)
	}
}

func TestAdapter_RawTooLarge(t *testing.T) {
	s := newStub(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(strings.Repeat("a", 101))) })
	a := s.adapter(s.binding(secret))
	conn, _, _ := a.Match(t.Context(), "git.corp.example")

	got, err := a.Raw(t.Context(), conn, "g/p", "a.md", "main", 100)
	if !errors.Is(err, app.ErrGitLabFileTooLarge) || got != nil {
		t.Errorf("Raw() = (%d bytes, %v), want ErrGitLabFileTooLarge", len(got), err)
	}
}

func TestAdapter_RawTransportErrorIsPassedThroughWithoutTheToken(t *testing.T) {
	s := newStub(t, func(w http.ResponseWriter, _ *http.Request) {})
	b := s.binding(secret)
	s.Close() // the port now refuses connections
	a := s.adapter(b)
	conn, _, _ := a.Match(t.Context(), b.Host)

	_, err := a.Raw(t.Context(), conn, "g/p", "a.md", "main", 100)
	if err == nil {
		t.Fatal("Raw() error = nil, want a transport failure")
	}
	if strings.Contains(err.Error(), secret) {
		t.Errorf("Raw() error %q contains the token", err)
	}
}

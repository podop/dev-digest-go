package gitlab_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	cigitlab "dev-digest-go/backend/internal/ci/adapters/gitlab"
	"dev-digest-go/backend/internal/ci/app"
	glclient "dev-digest-go/backend/internal/platform/gitlab"
	"dev-digest-go/backend/internal/platform/retry"
)

// testToken is a recognisable personal access token (NFR6).
const testToken = "glpat-AbCdEfGhIjKlMnOpQrSt"

const testHost = "gitlab.test"

type seen struct {
	Method, URI string
	Header      http.Header
	Body        string
}

// stub is a recording GitLab TLS double; the platform client trusts its certificate.
type stub struct {
	*httptest.Server
	mu   sync.Mutex
	reqs []seen
}

func newStub(t *testing.T, handler http.HandlerFunc) *stub {
	t.Helper()
	s := &stub{}
	s.Server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		s.mu.Lock()
		s.reqs = append(s.reqs, seen{r.Method, r.RequestURI, r.Header.Clone(), string(body)})
		s.mu.Unlock()
		handler(w, r)
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *stub) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.reqs)
}

func (s *stub) req(i int) seen {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.reqs[i]
}

// client builds a platform client that trusts the stub and never retries, so a test counts the
// attempts of one adapter call.
func (s *stub) client() *glclient.Client {
	return glclient.New(glclient.Options{HTTP: s.Client(), Retry: &retry.Options{}})
}

// resolve answers a binding for testHost pointing at the stub, with token.
func (s *stub) resolve(token string) glclient.ResolveFunc {
	return func(_ context.Context, host string) (glclient.Binding, bool, error) {
		if host != testHost {
			return glclient.Binding{}, false, nil
		}
		return glclient.Binding{ID: "c1", Name: "Corp", BaseURL: s.URL, Host: host, Token: token}, true, nil
	}
}

func (s *stub) publisher() *cigitlab.Publisher {
	return cigitlab.NewPublisher(s.client(), s.resolve(testToken))
}

func (s *stub) reader(storage *http.Client) *cigitlab.CI {
	return cigitlab.NewCI(s.client(), s.resolve(testToken), storage)
}

var ref = app.GitLabRef{Host: testHost, ProjectID: 7}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func okJSON(v any) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, 200, v) }
}

func status(code int, message string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, code, map[string]any{"message": message}) }
}

// gitLabError asserts that err is a *app.GitLabError and returns it.
func gitLabError(t *testing.T, err error) *app.GitLabError {
	t.Helper()
	var ge *app.GitLabError
	if !errors.As(err, &ge) {
		t.Fatalf("error = %v (%T), want *app.GitLabError", err, err)
	}
	return ge
}

// assertNoToken fails when text shows the token or a run of it.
func assertNoToken(t *testing.T, what, text string) {
	t.Helper()
	for _, frag := range []string{testToken, testToken[:12], testToken[8:]} {
		if strings.Contains(text, frag) {
			t.Errorf("%s %q contains part of the token", what, text)
		}
	}
}

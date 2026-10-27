package peers_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/intent/adapters/peers"
	"dev-digest-go/backend/internal/intent/app"
	"dev-digest-go/backend/internal/intent/domain"
	"dev-digest-go/backend/internal/platform/gitcli"
	"dev-digest-go/backend/internal/platform/gitlab"
	"dev-digest-go/backend/internal/platform/retry"
)

const (
	glToken = "glpat-secrettoken0123456789"
	glSHA   = "0123456789abcdef0123456789abcdef01234567"
)

// glStub is a GitLab that records the requests (path + query, token header) and answers with its handler.
type glStub struct {
	srv  *httptest.Server
	mu   sync.Mutex
	reqs []string
	toks []string
}

func newGLStub(t *testing.T, h func(w http.ResponseWriter, r *http.Request)) *glStub {
	t.Helper()
	s := &glStub{}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.reqs = append(s.reqs, r.URL.RequestURI())
		s.toks = append(s.toks, r.Header.Get("PRIVATE-TOKEN"))
		s.mu.Unlock()
		h(w, r)
	}))
	t.Cleanup(s.srv.Close)
	return s
}

func (s *glStub) requests() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.reqs...)
}

func (s *glStub) tokens() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.toks...)
}

// client is the real GitLab client without retries (one request per call).
func (s *glStub) client() *gitlab.Client {
	return gitlab.New(gitlab.Options{HTTP: s.srv.Client(), Retry: &retry.Options{}})
}

// resolver serves one connection for the repository's host key and records the keys it was asked for.
type resolver struct {
	mu    sync.Mutex
	asked []string
	b     gitlab.Binding
	ok    bool
	err   error
}

func (r *resolver) resolve(_ context.Context, hostKey string) (gitlab.Binding, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.asked = append(r.asked, hostKey)
	return r.b, r.ok && hostKey == r.b.Host, r.err
}

func (r *resolver) keys() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.asked...)
}

// connected is a resolver whose connection "corp" (token glToken unless given) lives at base.
func connected(base, tok string) *resolver {
	return &resolver{ok: true, b: gitlab.Binding{ID: "c1", Name: "corp", BaseURL: base, Host: gitlabRepo.Host, Token: tok}}
}

func issueAnswer(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/api/v4/projects/42/issues/7":
		writeJSON(w, 200, map[string]any{"iid": 7, "title": "Rate limits", "description": "Add them.", "state": "opened"})
	case "/api/v4/projects/42/issues/8":
		writeJSON(w, 200, map[string]any{"iid": 8, "title": "No body", "description": nil, "state": "closed"})
	default:
		writeJSON(w, 404, map[string]any{"message": "404 Not found"})
	}
}

func TestTickets_GitLabIssuesAreReadByProjectIDWithTheConnectionsToken(t *testing.T) { // AC24, FR27
	gl := newGLStub(t, issueAnswer)
	res := connected(gl.srv.URL, glToken)
	gh := newStub(t, issueHandler)
	tickets := peers.NewTickets(gh.client(token), nil).WithGitLab(gl.client(), res.resolve)

	tests := []struct {
		name   string
		number float64
		want   app.TicketRead
	}{
		{"issue with a description", 7, app.TicketRead{OK: true, Title: "Rate limits", Body: "Add them."}},
		{"issue without a description", 8, app.TicketRead{OK: true, Title: "No body"}},
		{"missing or invisible issue", 9, app.TicketRead{Reason: "404 Not found"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := tickets.Read(t.Context(), gitlabRepo, tc.number)
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("Read(%v) mismatch (-want +got):\n%s", tc.number, diff)
			}
		})
	}

	wantReqs := []string{"/api/v4/projects/42/issues/7", "/api/v4/projects/42/issues/8", "/api/v4/projects/42/issues/9"}
	if diff := cmp.Diff(wantReqs, gl.requests()); diff != "" {
		t.Errorf("GitLab requests mismatch (-want +got):\n%s", diff)
	}
	for _, tok := range gl.tokens() {
		if tok != glToken {
			t.Errorf("PRIVATE-TOKEN = %q, want the connection's token", tok)
		}
	}
	for _, key := range res.keys() {
		if key != "git.example.com:8443" {
			t.Errorf("connection looked up by %q, want the repository's host key", key)
		}
	}
	if reqs := gh.requests(); len(reqs) != 0 {
		t.Errorf("GitHub requests = %v, want none for a GitLab repository", reqs)
	}
}

func TestTickets_GitLabUnresolvedCasesMakeNoRequest(t *testing.T) { // AC24, EC21, FR23
	tests := []struct {
		name   string
		res    *resolver
		repo   domain.Repo
		number float64
		want   string
	}{
		{"no token", connected("", ""), gitlabRepo, 7, "GitLab connection corp has no token"},
		{"blank token", connected("", "  "), gitlabRepo, 7, "GitLab connection corp has no token"},
		{"not connected", &resolver{}, gitlabRepo, 7, "No GitLab connection for git.example.com:8443"},
		{"connections cannot be read", &resolver{err: errors.New("db password=hunter2")}, gitlabRepo, 7, "GitLab connections could not be read"},
		{"no project id", connected("", glToken), domain.Repo{Provider: "gitlab", Host: gitlabRepo.Host}, 7, "No GitLab connection for git.example.com:8443"},
		{"number that cannot name an issue", connected("", glToken), gitlabRepo, 0, "404 Not found"},
		{"absurd number", connected("", glToken), gitlabRepo, 1e300, "404 Not found"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gl := newGLStub(t, issueAnswer)
			gh := newStub(t, issueHandler)
			if tc.res.b.BaseURL == "" {
				tc.res.b.BaseURL = gl.srv.URL
			}
			got := peers.NewTickets(gh.client(token), nil).WithGitLab(gl.client(), tc.res.resolve).Read(t.Context(), tc.repo, tc.number)

			if want := (app.TicketRead{Reason: tc.want}); !cmp.Equal(got, want) {
				t.Errorf("Read() = %+v, want %+v", got, want)
			}
			if reqs := append(gl.requests(), gh.requests()...); len(reqs) != 0 {
				t.Errorf("requests = %v, want none", reqs)
			}
		})
	}
}

// failingReads is a GitLab client that fails with err.
type failingReads struct{ err error }

func (f failingReads) Issue(context.Context, gitlab.Conn, int64, int) (gitlab.Issue, error) {
	return gitlab.Issue{}, f.err
}

func (f failingReads) RawFile(context.Context, gitlab.Conn, string, string, string, int) ([]byte, error) {
	return nil, f.err
}

func TestTickets_GitLabFailureTextIsRedactedAndBounded(t *testing.T) { // FR27
	redact := func(s string) string { return strings.ReplaceAll(s, "sk-provider-key", "[REDACTED]") }
	text := "dial tcp: " + glToken + " sk-provider-key " + strings.Repeat("é", 800)
	tickets := peers.NewTickets(failingIssues{}, redact).
		WithGitLab(failingReads{err: errors.New(text)}, connected("https://git.example.com:8443", glToken).resolve)

	got := tickets.Read(t.Context(), gitlabRepo, 7)

	if strings.Contains(got.Reason, glToken) || strings.Contains(got.Reason, "sk-provider-key") ||
		!strings.HasPrefix(got.Reason, "dial tcp: [REDACTED] [REDACTED] ") {
		t.Errorf("Reason = %.80q…, want the connection token and the redactor's secrets removed", got.Reason)
	}
	if len(got.Reason) > 1000 || !strings.HasSuffix(got.Reason, "é") {
		t.Errorf("Reason is %d bytes ending %q, want at most 1000 bytes cut at a character", len(got.Reason), got.Reason[len(got.Reason)-3:])
	}
}

func TestTickets_GitLabStatusMessageIsTheReason(t *testing.T) {
	gl := newGLStub(t, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, 403, map[string]any{"message": "403 Forbidden"})
	})
	tickets := peers.NewTickets(failingIssues{}, nil).WithGitLab(gl.client(), connected(gl.srv.URL, glToken).resolve)

	got := tickets.Read(t.Context(), gitlabRepo, 7)

	if want := (app.TicketRead{Reason: "403 Forbidden"}); !cmp.Equal(got, want) {
		t.Errorf("Read(403) = %+v, want %+v", got, want)
	}
}

func TestTickets_GitHubRepositoryNeverAsksGitLab(t *testing.T) {
	gl := newGLStub(t, issueAnswer)
	res := connected(gl.srv.URL, glToken)
	gh := newStub(t, issueHandler)
	tickets := peers.NewTickets(gh.client(token), nil).WithGitLab(gl.client(), res.resolve)

	got := tickets.Read(t.Context(), repo, 471)

	if !got.OK {
		t.Errorf("Read(GitHub) = %+v, want the GitHub issue", got)
	}
	if reqs := gl.requests(); len(reqs) != 0 || len(res.keys()) != 0 {
		t.Errorf("GitLab requests = %v, lookups = %v, want none for a GitHub repository", reqs, res.keys())
	}
}

// ---- docs ----

// glFiles answers the files API from a table of path -> content.
func glFiles(files map[string]string) func(w http.ResponseWriter, r *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		const prefix = "/api/v4/projects/42/repository/files/"
		rest, ok := strings.CutPrefix(r.URL.EscapedPath(), prefix)
		path, _ := strings.CutSuffix(rest, "/raw")
		content, found := files[strings.ReplaceAll(path, "%2F", "/")]
		if !ok || !found || r.URL.Query().Get("ref") != glSHA {
			writeJSON(w, 404, map[string]any{"message": "404 File Not Found"})
			return
		}
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte(content))
	}
}

// absentGit is a clone without the blob; bigGit has it over the cap.
type absentGit struct{ err error }

func (g absentGit) BlobAt(context.Context, string, string, string, string, int64) ([]byte, error) {
	return nil, g.err
}

func TestDocs_GitLabFallsBackToTheFilesAPIOfTheSameProject(t *testing.T) { // AC25, FR29, EC22
	big := strings.Repeat("x", 70_000)
	gl := newGLStub(t, glFiles(map[string]string{"docs/a.md": "# From GitLab\n", "docs/big.md": big, "docs/exact.md": strings.Repeat("y", domain.DocFetchMaxBytes)}))
	res := connected(gl.srv.URL, glToken)
	gh := newStub(t, contentsHandler(map[string]string{"docs/a.md": "From GitHub"}))
	docs := peers.NewDocs(absentGit{err: errors.New("no such path")}, gh.client(token)).WithGitLab(gl.client(), res.resolve)

	tests := []struct {
		name string
		path string
		want app.DocRead
	}{
		{"file only on GitLab", "docs/a.md", app.DocRead{OK: true, Content: "# From GitLab\n"}},
		{"file of exactly the cap", "docs/exact.md", app.DocRead{OK: true, Content: strings.Repeat("y", domain.DocFetchMaxBytes)}},
		{"70,000-byte file only on GitLab", "docs/big.md", app.DocRead{Reason: "not found"}},
		{"missing file", "docs/none.md", app.DocRead{Reason: "not found"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := docs.Read(t.Context(), gitlabRepo, glSHA, tc.path)
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("Read(%q) mismatch (-want +got):\n%s", tc.path, diff)
			}
		})
	}

	want := []string{
		"/api/v4/projects/42/repository/files/docs%2Fa.md/raw?ref=" + glSHA,
		"/api/v4/projects/42/repository/files/docs%2Fexact.md/raw?ref=" + glSHA,
		"/api/v4/projects/42/repository/files/docs%2Fbig.md/raw?ref=" + glSHA,
		"/api/v4/projects/42/repository/files/docs%2Fnone.md/raw?ref=" + glSHA,
	}
	if diff := cmp.Diff(want, gl.requests()); diff != "" {
		t.Errorf("GitLab requests mismatch (-want +got):\n%s", diff)
	}
	for _, tok := range gl.tokens() {
		if tok != glToken {
			t.Errorf("PRIVATE-TOKEN = %q, want the connection's token", tok)
		}
	}
	if reqs := gh.requests(); len(reqs) != 0 {
		t.Errorf("GitHub requests = %v, want none for a GitLab repository", reqs)
	}
}

func TestDocs_GitLabCloneStillComesFirst(t *testing.T) { // FR29: the order of Phase 5 FR5016 is unchanged
	gl := newGLStub(t, glFiles(map[string]string{"docs/a.md": "# From GitLab\n", "docs/big.md": "x"}))
	res := connected(gl.srv.URL, glToken)

	t.Run("a blob in the clone is used without a request", func(t *testing.T) {
		docs := peers.NewDocs(&recordingGit{}, noContents{}).WithGitLab(gl.client(), res.resolve)
		got := docs.Read(t.Context(), gitlabRepo, glSHA, "docs/a.md")
		if want := (app.DocRead{OK: true, Content: "# From the clone\n"}); !cmp.Equal(got, want) {
			t.Errorf("Read(clone doc) = %+v, want %+v", got, want)
		}
	})
	t.Run("a clone blob over the cap fails with its size and no request", func(t *testing.T) {
		docs := peers.NewDocs(absentGit{err: &gitcli.BlobSizeError{Size: 70_000, Max: domain.DocFetchMaxBytes}}, noContents{}).WithGitLab(gl.client(), res.resolve)
		got := docs.Read(t.Context(), gitlabRepo, glSHA, "docs/big.md")
		want := app.DocRead{Reason: "File 'docs/big.md' at " + glSHA + " is too large (70000 bytes > 65536)"}
		if !cmp.Equal(got, want) {
			t.Errorf("Read(clone doc over the cap) = %+v, want %+v", got, want)
		}
	})
	if reqs := gl.requests(); len(reqs) != 0 {
		t.Errorf("GitLab requests = %v, want none", reqs)
	}
}

func TestDocs_GitLabUnresolvedCasesAreNotFoundWithoutARequest(t *testing.T) { // AC25, FR23, FR29
	tests := []struct {
		name string
		res  *resolver
		sha  string
		repo domain.Repo
		path string
	}{
		{"no token", connected("", ""), glSHA, gitlabRepo, "docs/a.md"},
		{"not connected", &resolver{}, glSHA, gitlabRepo, "docs/a.md"},
		{"connections cannot be read", &resolver{err: errors.New("down")}, glSHA, gitlabRepo, "docs/a.md"},
		{"head that is not a commit", connected("", glToken), "main", gitlabRepo, "docs/a.md"},
		{"no project id", connected("", glToken), glSHA, domain.Repo{Provider: "gitlab", Host: gitlabRepo.Host}, "docs/a.md"},
		{"unsupported extension", connected("", glToken), glSHA, gitlabRepo, "docs/a.exe"},
		{"unsafe path", connected("", glToken), glSHA, gitlabRepo, "docs/../a.md"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gl := newGLStub(t, glFiles(map[string]string{"docs/a.md": "x"}))
			gh := newStub(t, contentsHandler(map[string]string{"docs/a.md": "From GitHub"}))
			if tc.res.b.BaseURL == "" {
				tc.res.b.BaseURL = gl.srv.URL
			}
			docs := peers.NewDocs(absentGit{err: errors.New("no clone")}, gh.client(token)).WithGitLab(gl.client(), tc.res.resolve)

			got := docs.Read(t.Context(), tc.repo, tc.sha, tc.path)

			if got.OK {
				t.Errorf("Read() = %+v, want a failure", got)
			}
			if reqs := append(gl.requests(), gh.requests()...); len(reqs) != 0 {
				t.Errorf("requests = %v, want none", reqs)
			}
		})
	}
}

func TestDocs_GitLabWithoutWiringIsNotFound(t *testing.T) {
	gh := newStub(t, contentsHandler(map[string]string{"docs/a.md": "From GitHub"}))
	docs := peers.NewDocs(absentGit{err: errors.New("no clone")}, gh.client(token))

	got := docs.Read(t.Context(), gitlabRepo, glSHA, "docs/a.md")

	if want := (app.DocRead{Reason: "not found"}); !cmp.Equal(got, want) {
		t.Errorf("Read(unwired) = %+v, want %+v", got, want)
	}
	if reqs := gh.requests(); len(reqs) != 0 {
		t.Errorf("GitHub requests = %v, want none for a GitLab repository", reqs)
	}
}

func TestDocs_GitLabFailureReasonDoesNotLeakTheToken(t *testing.T) {
	docs := peers.NewDocs(absentGit{err: errors.New("no clone")}, noContents{}).
		WithGitLab(failingReads{err: errors.New("boom " + glToken)}, connected("https://git.example.com:8443", glToken).resolve)

	got := docs.Read(t.Context(), gitlabRepo, glSHA, "docs/a.md")

	if want := (app.DocRead{Reason: "not found"}); !cmp.Equal(got, want) {
		t.Errorf("Read(failing GitLab) = %+v, want %+v", got, want)
	}
}

func TestDocs_GitHubRepositoryNeverAsksGitLab(t *testing.T) {
	gl := newGLStub(t, glFiles(map[string]string{"docs/a.md": "x"}))
	res := connected(gl.srv.URL, glToken)
	gh := newStub(t, contentsHandler(map[string]string{"docs/a.md": "From GitHub"}))
	docs := peers.NewDocs(absentGit{err: errors.New("no clone")}, gh.client(token)).WithGitLab(gl.client(), res.resolve)

	got := docs.Read(t.Context(), repo, glSHA, "docs/a.md")

	if want := (app.DocRead{OK: true, Content: "From GitHub"}); !cmp.Equal(got, want) {
		t.Errorf("Read(GitHub) = %+v, want %+v", got, want)
	}
	if reqs := gl.requests(); len(reqs) != 0 || len(res.keys()) != 0 {
		t.Errorf("GitLab requests = %v, lookups = %v, want none for a GitHub repository", reqs, res.keys())
	}
}

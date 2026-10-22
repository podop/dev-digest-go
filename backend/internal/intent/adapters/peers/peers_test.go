package peers_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/intent/adapters/peers"
	"dev-digest-go/backend/internal/intent/app"
	"dev-digest-go/backend/internal/intent/domain"
	"dev-digest-go/backend/internal/platform/gitcli"
	"dev-digest-go/backend/internal/platform/github"
	"dev-digest-go/backend/internal/platform/retry"
)

const token = "ghp_secrettoken0123456789"

var repo = domain.Repo{ID: "r", Owner: "acme", Name: "api"}

// stub is a GitHub that records the paths it was asked for and answers with its handler.
type stub struct {
	srv  *httptest.Server
	mu   sync.Mutex
	reqs []string
}

func newStub(t *testing.T, h func(w http.ResponseWriter, r *http.Request)) *stub {
	t.Helper()
	s := &stub{}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.reqs = append(s.reqs, r.URL.RequestURI())
		s.mu.Unlock()
		h(w, r)
	}))
	t.Cleanup(s.srv.Close)
	return s
}

func (s *stub) requests() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.reqs...)
}

func (s *stub) client(tok string) *github.Client {
	noRetry := retry.Options{Retries: 0}
	return github.New(github.Options{Token: func() string { return tok }, BaseURL: s.srv.URL, Retry: &noRetry})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// ---- docs: a real clone fixture and a stub GitHub (AC5023) ----

// hermeticGit keeps git away from the developer's configuration.
func hermeticGit(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_AUTHOR_NAME", "t")
	t.Setenv("GIT_AUTHOR_EMAIL", "t@example.com")
	t.Setenv("GIT_COMMITTER_NAME", "t")
	t.Setenv("GIT_COMMITTER_EMAIL", "t@example.com")
}

// cloneFixture creates the clone acme/api under a fresh root with the given files committed, and
// returns the git client and the head commit.
func cloneFixture(t *testing.T, files map[string]string) (*gitcli.Client, string) {
	t.Helper()
	hermeticGit(t)
	root := t.TempDir()
	git, err := gitcli.New(gitcli.Options{CloneRoot: root})
	if err != nil {
		t.Fatal(err)
	}
	dir, err := git.ClonePath("acme", "api")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) string {
		t.Helper()
		cmd := exec.CommandContext(t.Context(), "git", args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	run("init", "-q", "-b", "main")
	for path, content := range files {
		full := filepath.Join(dir, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	run("add", "-A")
	run("commit", "-q", "-m", "docs")
	return git, run("rev-parse", "HEAD")
}

func contentsHandler(files map[string]string) func(w http.ResponseWriter, r *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/repos/acme/api/contents/")
		content, ok := files[path]
		if !ok {
			writeJSON(w, http.StatusNotFound, map[string]string{"message": "Not Found"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"type": "file", "size": len(content), "encoding": "base64", "content": base64.StdEncoding.EncodeToString([]byte(content)),
		})
	}
}

func TestDocs_Read(t *testing.T) { // AC5023
	big := strings.Repeat("x", 70_000)
	git, sha := cloneFixture(t, map[string]string{"docs/in-clone.md": "# In the clone\n", "docs/big.md": big})
	gh := newStub(t, contentsHandler(map[string]string{"docs/only-on-github.md": "From GitHub"}))
	docs := peers.NewDocs(git, gh.client(token))

	tests := []struct {
		name         string
		path         string
		want         app.DocRead
		wantGitHub   []string
		wantNoGitHub bool
	}{
		{"file in the clone", "docs/in-clone.md", app.DocRead{OK: true, Content: "# In the clone\n"}, nil, true},
		{"file over the cap", "docs/big.md", app.DocRead{Reason: "File 'docs/big.md' at " + sha + " is too large (70000 bytes > 65536)"}, nil, true},
		{"path only on GitHub", "docs/only-on-github.md", app.DocRead{OK: true, Content: "From GitHub"},
			[]string{"/repos/acme/api/contents/docs/only-on-github.md?ref=" + sha}, false},
		{"path absent everywhere", "docs/nowhere.md", app.DocRead{Reason: "not found"},
			[]string{"/repos/acme/api/contents/docs/nowhere.md?ref=" + sha}, false},
		{"path traversal", "docs/../x.md", app.DocRead{Reason: "unsafe path"}, nil, true},
		{"not a doc extension", "src/main.go", app.DocRead{Reason: "unsupported file type 'src/main.go'"}, nil, true},
		{"absolute path", "/etc/passwd.md", app.DocRead{Reason: "unsafe path"}, nil, true},
		{"leading dash", "-rf.md", app.DocRead{Reason: "unsafe path"}, nil, true},
		{"backslash", `docs\a.md`, app.DocRead{Reason: "unsafe path"}, nil, true},
		{"NUL", "docs/a\x00.md", app.DocRead{Reason: "unsafe path"}, nil, true},
		{"git directory segment", "docs/.git/config.md", app.DocRead{Reason: "unsafe path"}, nil, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			before := len(gh.requests())

			got := docs.Read(t.Context(), repo, sha, tc.path)

			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("Read(%q) mismatch (-want +got):\n%s", tc.path, diff)
			}
			if diff := cmp.Diff(tc.wantGitHub, gh.requests()[before:], cmpEmpty); diff != "" {
				t.Errorf("Read(%q) GitHub requests mismatch (-want +got):\n%s", tc.path, diff)
			}
		})
	}
}

var cmpEmpty = cmp.Transformer("nilToEmpty", func(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
})

func TestDocs_AHeadThatIsNotACommitFallsBackToGitHub(t *testing.T) {
	git, _ := cloneFixture(t, map[string]string{"docs/a.md": "clone"})
	gh := newStub(t, contentsHandler(map[string]string{"docs/a.md": "github"}))
	docs := peers.NewDocs(git, gh.client(token))

	for _, sha := range []string{"main", "--help", "abc", strings.Repeat("a", 41)} {
		if got := docs.Read(t.Context(), repo, sha, "docs/a.md"); !got.OK || got.Content != "github" {
			t.Errorf("Read(head %q) = %+v, want the GitHub content (no clone read for a head that is not 7 to 40 hex digits)", sha, got)
		}
	}
}

func TestDocs_GitHubFailuresAreNotFound(t *testing.T) {
	git, sha := cloneFixture(t, map[string]string{"docs/a.md": "clone"})
	tests := []struct {
		name    string
		token   string
		handler func(w http.ResponseWriter, r *http.Request)
	}{
		{"error status", token, func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(w, http.StatusForbidden, map[string]string{"message": "rate limited"})
		}},
		{"no token", "", contentsHandler(nil)},
		{"directory listing", token, func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(w, http.StatusOK, []map[string]string{{"type": "file"}})
		}},
		{"reported size over the cap", token, func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(w, http.StatusOK, map[string]any{"type": "file", "size": 70000, "encoding": "base64", "content": ""})
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gh := newStub(t, tc.handler)
			docs := peers.NewDocs(git, gh.client(tc.token))

			got := docs.Read(t.Context(), repo, sha, "docs/missing.md")

			if want := (app.DocRead{Reason: "not found"}); got != want {
				t.Errorf("Read() = %+v, want %+v", got, want)
			}
		})
	}
}

func TestDocs_ACancelledContextDoesNotAskGitHub(t *testing.T) {
	git, sha := cloneFixture(t, map[string]string{"docs/a.md": "clone"})
	gh := newStub(t, contentsHandler(map[string]string{"docs/missing.md": "x"}))
	docs := peers.NewDocs(git, gh.client(token))
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	got := docs.Read(ctx, repo, sha, "docs/missing.md")

	if got.OK || len(gh.requests()) != 0 {
		t.Errorf("Read(cancelled) = %+v with %d GitHub requests, want a failure and none", got, len(gh.requests()))
	}
}

// fakeGit returns whatever it is told, ignoring the cap, to reach the cut of long text.
type fakeGit struct{ blob []byte }

func (f fakeGit) BlobAt(context.Context, string, string, string, string, int64) ([]byte, error) {
	return f.blob, nil
}

type noContents struct{}

func (noContents) GetContent(context.Context, string, string, string, string, int64) ([]byte, bool, error) {
	return nil, false, nil
}

func TestDocs_TextOverTheCapInUnitsIsCutAndMarkedTruncated(t *testing.T) {
	// 40,000 astral characters are 160,000 bytes but 80,000 UTF-16 units: over 65,536 units.
	docs := peers.NewDocs(fakeGit{blob: []byte(strings.Repeat("😀", 40_000))}, noContents{})

	got := docs.Read(t.Context(), repo, "0123456789abcdef", "docs/a.md")

	if !got.OK || !got.Truncated || len([]rune(got.Content)) != 32_768 {
		t.Errorf("Read() = ok %v, truncated %v, %d runes, want a cut to 65,536 units (32,768 pairs)", got.OK, got.Truncated, len([]rune(got.Content)))
	}
}

func TestDocs_InvalidUTF8IsDecodedWithReplacement(t *testing.T) {
	docs := peers.NewDocs(fakeGit{blob: []byte("a\xe2\x82b")}, noContents{})

	got := docs.Read(t.Context(), repo, "0123456789abcdef", "docs/a.md")

	if want := "a�b"; got.Content != want {
		t.Errorf("Read().Content = %q, want %q (one replacement per maximal subpart)", got.Content, want)
	}
}

// ---- tickets (AC5024) ----

func issueHandler(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/repos/acme/api/issues/471":
		writeJSON(w, http.StatusOK, map[string]any{"number": 471, "title": "Flooding", "body": "Callers flood the webhooks.", "state": "open"})
	case "/repos/acme/api/issues/5":
		writeJSON(w, http.StatusOK, map[string]any{"number": 5, "title": "No body", "body": nil, "state": "open"})
	case "/repos/acme/api/issues/401":
		writeJSON(w, http.StatusUnauthorized, map[string]string{"message": "Bad credentials for " + token})
	default:
		writeJSON(w, http.StatusNotFound, map[string]string{"message": "Not Found"})
	}
}

func TestTickets_Read(t *testing.T) { // AC5024
	gh := newStub(t, issueHandler)
	tests := []struct {
		name   string
		token  string
		number float64
		want   app.TicketRead
		calls  int
	}{
		{"existing issue", token, 471, app.TicketRead{OK: true, Title: "Flooding", Body: "Callers flood the webhooks."}, 1},
		{"issue without a body", token, 5, app.TicketRead{OK: true, Title: "No body"}, 1},
		{"missing issue", token, 9, app.TicketRead{Reason: "Not Found"}, 1},
		{"no token", "", 471, app.TicketRead{Reason: "GITHUB_TOKEN is not configured"}, 0},
		{"token in GitHub's message is redacted", token, 401, app.TicketRead{Reason: "Bad credentials for [REDACTED]"}, 1},
		{"infinite number", token, math.Inf(1), app.TicketRead{Reason: "Not Found"}, 0},
		{"unsafe integer", token, 9007199254740993, app.TicketRead{Reason: "Not Found"}, 0},
		{"zero", token, 0, app.TicketRead{Reason: "Not Found"}, 0},
		{"fraction", token, 1.5, app.TicketRead{Reason: "Not Found"}, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			before := len(gh.requests())
			tickets := peers.NewTickets(gh.client(tc.token), nil)

			got := tickets.Read(t.Context(), repo, tc.number)

			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("Read(%v) mismatch (-want +got):\n%s", tc.number, diff)
			}
			if n := len(gh.requests()) - before; n != tc.calls {
				t.Errorf("Read(%v) made %d GitHub requests, want %d", tc.number, n, tc.calls)
			}
		})
	}
}

type failingIssues struct{ err error }

func (f failingIssues) GetIssue(context.Context, string, string, int) (github.Issue, error) {
	return github.Issue{}, f.err
}

func TestTickets_OtherFailuresAreRedactedAndBounded(t *testing.T) {
	redact := func(s string) string { return strings.ReplaceAll(s, token, "[REDACTED]") }
	tickets := peers.NewTickets(failingIssues{err: &netError{"dial tcp: lookup api.github.com: " + token + strings.Repeat("é", 800)}}, redact)

	got := tickets.Read(t.Context(), repo, 7)

	if strings.Contains(got.Reason, token) || !strings.HasPrefix(got.Reason, "dial tcp: lookup api.github.com: [REDACTED]") {
		t.Errorf("Reason = %.80q…, want the token redacted", got.Reason)
	}
	if len(got.Reason) > 1000 || !strings.HasSuffix(got.Reason, "é") {
		t.Errorf("Reason is %d bytes ending %q, want at most 1000 bytes cut at a character", len(got.Reason), got.Reason[len(got.Reason)-3:])
	}
}

type netError struct{ s string }

func (e *netError) Error() string { return e.s }

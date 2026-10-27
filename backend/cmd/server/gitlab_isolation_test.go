package main

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/config"
	"dev-digest-go/backend/internal/llm"
)

// GitLab A component tests of the isolation guarantees (plan S36, spec AC22-AC24 and AC37): a
// GitLab repository never reaches GitHub and the other way round, no stored token leaks, the GitHub
// behaviour and the request guards are unchanged, and a repository whose connection was deleted
// is served from storage without a single outbound request. GitLab is a TLS stub on loopback
// (allowed by custom.ForbiddenAddr) whose certificate is the extra CA of the server and of git; the
// repository content is served by `git http-backend`. Helpers are prefixed gi (the other GitLab
// component tests share the package).

const (
	giToken      = "glpat-AbCdEfGhIjKlMnOpQrSt"
	giReadmePAT  = "glpat-ZyXwVuTsRqPoNmLkJiHg"
	giReadmeDT   = "gldt-0123456789abcdefghij"
	giProjectID  = 7
	giBrokenID   = 8
	giPath       = "acme/web"
	giBrokenPath = "acme/broken"
	giHeadSHA    = "0a1b2c3d4e5f60718293a4b5c6d7e8f901234567"
	giEvil       = "http://evil.example"
)

// ---- stub ----

// giReq is one request the GitLab stub saw.
type giReq struct {
	Method, Path, Query, Auth, Private string
	Body                               []byte
}

// giServer plays a self-hosted GitLab: the REST and GraphQL endpoints the slice calls for the
// projects acme/web (7) and acme/broken (8), and git over HTTPS for the repositories under gitRoot.
type giServer struct {
	t       testing.TB
	srv     *httptest.Server
	caFile  string
	gitRoot string

	mu   sync.Mutex
	reqs []giReq
	// hook, when set, may answer a request itself and report that it did (before the defaults).
	hook func(w http.ResponseWriter, r *http.Request) bool
	// gitErr, when set, makes every git request fail with a git "ERR" packet carrying this text.
	gitErr string
}

// giStart starts the stub, writes its certificate as the extra CA and as the system bundle git is
// given (this host may have none), and serves git repositories from a fresh directory.
func giStart(t *testing.T) *giServer {
	t.Helper()
	g := &giServer{t: t, gitRoot: t.TempDir()}
	g.srv = httptest.NewUnstartedServer(http.HandlerFunc(g.serveHTTP))
	g.srv.TLS = &tls.Config{MinVersion: tls.VersionTLS12}
	g.srv.StartTLS()
	t.Cleanup(g.srv.Close)

	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: g.srv.Certificate().Raw})
	g.caFile = filepath.Join(t.TempDir(), "gitlab-ca.pem")
	if err := os.WriteFile(g.caFile, pemBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SSL_CERT_FILE", g.caFile)
	return g
}

// hostKey is the connection's host key: the loopback address with the stub's port.
func (g *giServer) hostKey() string { return strings.TrimPrefix(g.srv.URL, "https://") }
func (g *giServer) baseURL() string { return g.srv.URL }

func (g *giServer) setHook(f func(w http.ResponseWriter, r *http.Request) bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.hook = f
}

// failing makes every non-git request whose path contains sub answer status with message.
func (g *giServer) failing(sub string, status int, message string) {
	g.setHook(func(w http.ResponseWriter, r *http.Request) bool {
		if !strings.Contains(r.URL.Path, sub) {
			return false
		}
		giJSON(w, status, map[string]any{"message": message})
		return true
	})
}

// giBasic is the Basic credential git sends for the token.
var giBasic = base64.StdEncoding.EncodeToString([]byte("oauth2:" + giToken))

// giEcho is a server message that repeats the token, like a careless proxy would.
var giEcho = fmt.Sprintf("401 Unauthorized: token %s was rejected", giToken)

// giEchoGit repeats the token in both of its spellings: git sends it as a Basic credential.
var giEchoGit = giEcho + " (credential " + giBasic + ")"

func (g *giServer) setGitErr(msg string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.gitErr = msg
}

func (g *giServer) requests() []giReq {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]giReq(nil), g.reqs...)
}

func (g *giServer) count() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.reqs)
}

// since lists the requests after the first n.
func (g *giServer) since(n int) []giReq {
	all := g.requests()
	if n > len(all) {
		return nil
	}
	return all[n:]
}

func (g *giServer) serveHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	g.mu.Lock()
	g.reqs = append(g.reqs, giReq{Method: r.Method, Path: r.URL.Path, Query: r.URL.RawQuery,
		Auth: r.Header.Get("Authorization"), Private: r.Header.Get("PRIVATE-TOKEN"), Body: body})
	hook, gitErr := g.hook, g.gitErr
	g.mu.Unlock()
	r.Body = io.NopCloser(strings.NewReader(string(body)))

	if hook != nil && hook(w, r) {
		return
	}
	if strings.Contains(r.URL.Path, ".git/") {
		g.serveGit(w, r, gitErr)
		return
	}
	g.serveAPI(w, r)
}

// serveGit answers the smart-HTTP protocol with git's own CGI program, or with an "ERR" packet.
func (g *giServer) serveGit(w http.ResponseWriter, r *http.Request, gitErr string) {
	if gitErr != "" {
		line := "ERR " + gitErr
		w.Header().Set("Content-Type", "application/x-git-upload-pack-advertisement")
		_, _ = fmt.Fprintf(w, "001e# service=git-upload-pack\n0000%04x%s", len(line)+4, line)
		return
	}
	out, err := exec.CommandContext(g.t.Context(), "git", "--exec-path").Output()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	h := &cgi.Handler{
		Path: filepath.Join(strings.TrimSpace(string(out)), "git-http-backend"),
		Env: []string{"GIT_PROJECT_ROOT=" + g.gitRoot, "GIT_HTTP_EXPORT_ALL=1",
			"GIT_CONFIG_GLOBAL=" + os.DevNull, "GIT_CONFIG_SYSTEM=" + os.DevNull},
	}
	h.ServeHTTP(w, r)
}

// project is the GitLab project JSON of id/path (nil: unknown).
func (g *giServer) project(key string) map[string]any {
	var id int
	var path string
	switch key {
	case "7", giPath:
		id, path = giProjectID, giPath
	case "8", giBrokenPath:
		id, path = giBrokenID, giBrokenPath
	default:
		return nil
	}
	return map[string]any{"id": id, "path": path[strings.LastIndex(path, "/")+1:], "path_with_namespace": path,
		"default_branch": "main", "web_url": g.baseURL() + "/" + path, "namespace": map[string]any{"full_path": "acme"}}
}

// mr is the merge request the stub lists.
func (g *giServer) mr(withDescription bool) map[string]any {
	m := map[string]any{"iid": 3, "title": "Add feature", "state": "opened", "draft": false,
		"source_branch": "feat", "target_branch": "main", "sha": giHeadSHA,
		"source_project_id": giProjectID, "target_project_id": giProjectID,
		"created_at": "2026-10-19T10:00:00Z", "updated_at": "2026-10-20T10:00:00Z", "merged_at": nil, "closed_at": nil,
		"web_url": g.baseURL() + "/" + giPath + "/-/merge_requests/3", "author": map[string]any{"username": "octo"}}
	if withDescription {
		m["description"] = "Fixes #12 and adds rate limiting."
	}
	return m
}

func giJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (g *giServer) serveAPI(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/v4")
	switch {
	case r.URL.Path == "/api/graphql" && r.Method == http.MethodPost:
		giJSON(w, http.StatusOK, map[string]any{"data": map[string]any{"projects": map[string]any{"nodes": []any{
			map[string]any{"id": "gid://gitlab/Project/7", "mergeRequests": map[string]any{"nodes": []any{
				map[string]any{"iid": "3", "diffStatsSummary": map[string]any{"additions": 7, "deletions": 1, "fileCount": 1}}}}}}}}})
	case path == "/user":
		giJSON(w, http.StatusOK, map[string]any{"username": "ada"})
	case path == "/personal_access_tokens/self":
		giJSON(w, http.StatusOK, map[string]any{"scopes": []string{"read_api", "read_repository"}, "expires_at": nil})
	case path == "/metadata":
		giJSON(w, http.StatusOK, map[string]any{"version": "17.5.1"})
	case strings.HasPrefix(path, "/projects/"):
		g.serveProject(w, strings.TrimPrefix(path, "/projects/"))
	default:
		giJSON(w, http.StatusNotFound, map[string]any{"message": "404 Not Found"})
	}
}

func (g *giServer) serveProject(w http.ResponseWriter, rest string) {
	key, sub, _ := strings.Cut(rest, "/")
	if strings.HasPrefix(rest, "acme/") { // a path-form key holds one slash
		parts := strings.SplitN(rest, "/", 3)
		key, sub = parts[0]+"/"+parts[1], ""
		if len(parts) == 3 {
			sub = parts[2]
		}
	}
	p := g.project(key)
	if p == nil {
		giJSON(w, http.StatusNotFound, map[string]any{"message": "404 Project Not Found"})
		return
	}
	switch {
	case sub == "":
		giJSON(w, http.StatusOK, p)
	case sub == "merge_requests" && key == "7":
		giJSON(w, http.StatusOK, []any{g.mr(false)})
	case sub == "merge_requests":
		giJSON(w, http.StatusOK, []any{})
	case sub == "merge_requests/3" && key == "7":
		giJSON(w, http.StatusOK, g.mr(true))
	case sub == "merge_requests/3/diffs" && key == "7":
		giJSON(w, http.StatusOK, []any{map[string]any{"old_path": "a.ts", "new_path": "a.ts", "diff": "@@ -1 +1,7 @@\n-a\n+b\n+c\n+d\n+e\n+f\n+g\n+h\n"}})
	case sub == "merge_requests/3/commits" && key == "7":
		giJSON(w, http.StatusOK, []any{map[string]any{"id": "c0ffee", "message": "feat: x", "author_name": "Octo", "committed_date": "2026-10-19T10:00:00Z"}})
	case sub == "issues/12":
		giJSON(w, http.StatusOK, map[string]any{"iid": 12, "title": "Bug", "description": "Details", "state": "opened"})
	default:
		giJSON(w, http.StatusNotFound, map[string]any{"message": "404 Not Found"})
	}
}

// ---- stack ----

// giEnv is a running server with its GitLab stub, git wrapper and scripted model.
type giEnv struct {
	t       *testing.T
	gl      *giServer
	s       *stack
	sc      *scripted
	gitCall string // the git wrapper's call log
}

// giOpts tunes giBoot.
type giOpts struct {
	files    map[string]string // content of acme/web (nil: the onboarding fixture)
	handlers map[string]callFn // scripted model answers per schema (others answer like the mock)
	spec     func(*spec)       // further changes of the spec
	noIntel  bool              // leave repo-intel off
}

// giBoot starts the GitLab stub with acme/web and acme/broken repositories and a server that trusts
// it. No connection exists yet.
func giBoot(t *testing.T, o giOpts) *giEnv {
	t.Helper()
	e := &giEnv{t: t, gl: giStart(t)}
	files := o.files
	if files == nil {
		files = tourFixture
	}
	makeFilesRemote(t, e.gl.gitRoot, "acme", "web", files)
	makeFilesRemote(t, e.gl.gitRoot, "acme", "broken", map[string]string{"README.md": "# broken\n"})

	binary, calls := gitWrapper(t, false)
	e.gitCall = calls
	e.sc = routed(o.handlers)
	sp := spec{gitBinary: binary, llmAdapters: e.sc.forAll(), now: fixedClock}
	sp.config = func(c *config.Config) {
		c.ExtraCAFile = e.gl.caFile
		if !o.noIntel {
			c.RepoIntelEnabled = true
		}
	}
	if o.spec != nil {
		o.spec(&sp)
	}
	e.s = startStack(t, sp)
	return e
}

// connect creates the connection to the stub with token and returns its id.
func (e *giEnv) connect(token string) string {
	e.t.Helper()
	body := map[string]any{"name": "Local", "base_url": e.gl.baseURL()}
	if token != "" {
		body["token"] = token
	}
	resp := e.s.post("/settings/gitlab-connections", body)
	if resp.Status != http.StatusCreated {
		e.t.Fatalf("POST /settings/gitlab-connections = %d %s, want 201", resp.Status, resp.Body)
	}
	return idOf(e.t, resp)
}

// addRepo adds the GitLab project path and waits for its clone job to finish; it returns the repo id.
func (e *giEnv) addRepo(path string) string {
	e.t.Helper()
	before := len(jobRows(e.t, e.s.db.DB, "clone"))
	resp := e.s.post("/repos", map[string]string{"url": e.gl.baseURL() + "/" + path})
	if resp.Status != http.StatusCreated {
		e.t.Fatalf("POST /repos %s = %d %s, want 201", path, resp.Status, resp.Body)
	}
	e.s.waitJobs("clone", before+1)
	return idOf(e.t, resp)
}

// pullID syncs the repo's list and returns the id of its merge request !3.
func (e *giEnv) pullID(repo string) string {
	e.t.Helper()
	resp := e.s.get("/repos/" + repo + "/pulls")
	if resp.Status != http.StatusOK {
		e.t.Fatalf("GET /repos/%s/pulls = %d %s, want 200", repo, resp.Status, resp.Body)
	}
	for _, p := range list(e.t, resp) {
		if p["number"] == float64(3) {
			return p["id"].(string)
		}
	}
	e.t.Fatalf("GET /repos/%s/pulls holds no !3: %s", repo, resp.Body)
	return ""
}

// gitArgv returns the git wrapper's log without the GIT_CONFIG_* environment lines: what git was
// given on its command line.
func (e *giEnv) gitArgv() string {
	e.t.Helper()
	raw, err := os.ReadFile(e.gitCall)
	if errors.Is(err, os.ErrNotExist) {
		return ""
	}
	if err != nil {
		e.t.Fatal(err)
	}
	var keep []string
	for _, line := range strings.Split(string(raw), "\n") {
		if !strings.HasPrefix(line, "GIT_CONFIG_") {
			keep = append(keep, line)
		}
	}
	return strings.Join(keep, "\n")
}

// gitArgvRaw is the git wrapper's whole log, environment lines included.
func (e *giEnv) gitArgvRaw() string {
	e.t.Helper()
	raw, err := os.ReadFile(e.gitCall)
	if errors.Is(err, os.ErrNotExist) {
		return ""
	}
	if err != nil {
		e.t.Fatal(err)
	}
	return string(raw)
}

// gitCalls is the number of git invocations so far.
func (e *giEnv) gitCalls() int {
	e.t.Helper()
	raw, err := os.ReadFile(e.gitCall)
	if errors.Is(err, os.ErrNotExist) {
		return 0
	}
	if err != nil {
		e.t.Fatal(err)
	}
	return strings.Count(string(raw), "--- call")
}

// dump is every row of every table as JSON text.
func (e *giEnv) dump() string {
	e.t.Helper()
	var b strings.Builder
	for _, table := range tableNames(e.t, e.s.db.DB) {
		b.WriteString(dumpTable(e.t, e.s.db.DB, table))
	}
	return b.String()
}

// giRecorder keeps the bodies and headers of the responses a test saw.
type giRecorder struct{ seen []string }

func (r *giRecorder) rec(resp response) response {
	r.seen = append(r.seen, string(resp.Body), fmt.Sprint(resp.Header))
	return resp
}

// ---- AC22: a GitLab pull request never reaches GitHub or GitLab for the features that are GitHub only ----

// lookups is the number of requests both stubs have seen.
func (e *giEnv) lookups() (gitlab, github int) { return e.gl.count(), len(e.s.gh.requests()) }

// AC22 (FR29, EC24, EC25, NFR3), as GitLab B changed it: comments, posting, the brief's issue, prior
// PRs and the intent's tickets of a GitLab merge request go to the merge request's own connection
// with its token and never to GitHub; the CI export still refuses a GitLab-only name (slice C) and
// prefers the GitHub repository of the same name. The behaviour of each feature is in the GitLab B
// wiring tests (gitlab_wiring_test.go).
func TestGitLabIsolation_AC22_GitLabPRFeaturesNeverReachGitHub(t *testing.T) {
	e := giBoot(t, giOpts{spec: func(sp *spec) {
		sp.runner = ciFakeRunner()
		sp.env = map[string]string{"GITHUB_TOKEN": ciToken}
	}})
	e.connect(giToken)
	repo := e.addRepo(giPath)
	pr := e.pullID(repo)
	if resp := e.s.get("/pulls/" + pr); resp.Status != http.StatusOK {
		t.Fatalf("GET /pulls/%s = %d %s, want 200", pr, resp.Status, resp.Body)
	}
	if gl, _ := e.lookups(); gl == 0 {
		t.Fatal("the GitLab stub saw no request while the repository was added and read; the stub is not wired")
	}
	glBefore, ghBefore := e.lookups()

	t.Run("comments are an empty list", func(t *testing.T) {
		resp := e.s.get("/pulls/" + pr + "/comments")
		if resp.Status != http.StatusOK || strings.TrimSpace(string(resp.Body)) != "[]" {
			t.Errorf("GET /pulls/:id/comments = %d %s, want 200 []", resp.Status, resp.Body)
		}
	})
	t.Run("posting a comment is refused by GitLab's answer", func(t *testing.T) {
		resp := e.s.post("/pulls/"+pr+"/comments", map[string]any{"path": "a.ts", "line": 3, "body": "x"})
		if resp.Status != http.StatusBadRequest || resp.errorCode(t) != "gitlab_comment_failed" {
			t.Errorf("POST /pulls/:id/comments = %d %s, want 400 gitlab_comment_failed", resp.Status, resp.Body)
		}
	})
	t.Run("the intent reads its tickets from GitLab", func(t *testing.T) {
		resp := e.s.post("/pulls/"+pr+"/intent/refresh", nil)
		if resp.Status != http.StatusOK {
			t.Fatalf("POST intent/refresh = %d %s, want 200", resp.Status, resp.Body)
		}
		var found bool
		for _, src := range obj(t, resp.JSON(t), "intent")["sources"].([]any) {
			m := src.(map[string]any)
			if m["kind"] != "ticket" {
				continue
			}
			found = true
			if m["ref"] != "12" || m["status"] != "used" {
				t.Errorf("ticket source = %v, want #12 used", m)
			}
		}
		if !found {
			t.Errorf("the intent has no ticket source for #12: %s", resp.Body)
		}
	})
	t.Run("the brief has its linked issue", func(t *testing.T) {
		resp := e.s.post("/pulls/"+pr+"/brief", nil)
		if resp.Status != http.StatusOK {
			t.Fatalf("POST brief = %d %s, want 200", resp.Status, resp.Body)
		}
		var missing []string
		for _, m := range obj(t, resp.JSON(t), "brief")["missing_inputs"].([]any) {
			mm := m.(map[string]any)
			missing = append(missing, fmt.Sprint(mm["input"], "/", mm["reason"]))
		}
		if slices.ContainsFunc(missing, func(m string) bool { return strings.HasPrefix(m, "linked_issue/") }) {
			t.Errorf("brief missing_inputs = %v, want the linked issue read from GitLab", missing)
		}
	})
	t.Run("blast radius has no prior PRs", func(t *testing.T) {
		resp := e.s.get("/pulls/" + pr + "/history")
		if resp.Status != http.StatusOK {
			t.Errorf("GET /pulls/:id/history = %d %s, want 200", resp.Status, resp.Body)
		}
		if resp := e.s.get("/pulls/" + pr + "/blast"); resp.Status != http.StatusOK {
			t.Errorf("GET /pulls/:id/blast = %d %s, want 200", resp.Status, resp.Body)
		}
	})

	gl, gh := e.lookups()
	if gh != ghBefore {
		t.Errorf("the GitLab features sent %d GitHub request(s), want none: %v", gh-ghBefore, e.s.gh.requests()[ghBefore:])
	}
	if gl == glBefore {
		t.Error("the GitLab features sent no request to GitLab; the wiring is missing")
	}
	for _, r := range e.gl.since(glBefore) {
		if r.Private != giToken {
			t.Errorf("GitLab request %s %s carried token %q, want the connection's", r.Method, r.Path, r.Private)
		}
	}
	glBefore, ghBefore = e.lookups()

	t.Run("CI export of a GitLab-only name is the GitLab export and sends no GitHub request", func(t *testing.T) {
		agent := cxAgent(t, e.s, "gi-agent", "openrouter", ciModel)
		// GitLab C: the repository is found on the GitLab host, whose connection is set, so the
		// export is supported (the former 422 unsupported_host is gone). The open_pr sequence is
		// covered by the GitLab CI export component tests.
		resp := e.s.post("/agents/"+agent+"/export-ci", ciExportBody(giPath, "files", nil))
		if resp.Status != http.StatusOK {
			t.Fatalf("export-ci files of the GitLab-only %s = %d %s, want 200", giPath, resp.Status, resp.Body)
		}
		var files struct {
			GitLabCI *struct {
				JobFile string `json:"job_file"`
			} `json:"gitlab_ci"`
		}
		if err := json.Unmarshal(resp.Body, &files); err != nil || files.GitLabCI == nil {
			t.Errorf("export-ci files of the GitLab-only %s = %s (%v), want a gitlab_ci block", giPath, resp.Body, err)
		}
		if _, gh := e.lookups(); gh != ghBefore {
			t.Errorf("the GitLab export sent %d GitHub request(s), want none", gh-ghBefore)
		}

		// A GitHub repository of the same name is the one exported.
		if resp := e.s.post("/repos", map[string]string{"url": "https://github.com/" + giPath}); resp.Status != http.StatusCreated {
			t.Fatalf("POST /repos github %s = %d %s, want 201", giPath, resp.Status, resp.Body)
		}
		glBefore, _ = e.lookups()
		d := newCiGH()
		d.branches[giPath] = map[string]string{"main": ciMainSHA}
		e.s.gh.setOverride(d.handle)
		out := ciExport(t, e.s, agent, ciExportBody(giPath, "open_pr", nil), http.StatusCreated)
		if out.Installation == nil || out.Installation.Repo != giPath || out.PRURL == nil || !strings.HasPrefix(*out.PRURL, "https://github.com/"+giPath+"/pull/") {
			t.Errorf("export of %s with both hosts = %+v, want the GitHub repository's installation and pull request", giPath, out)
		}
		if gl, _ := e.lookups(); gl != glBefore {
			t.Errorf("the GitHub export sent %d GitLab request(s), want none", gl-glBefore)
		}
	})
}

// AC22 (FR29), the other direction: a GitHub pull request sends nothing to GitLab.
func TestGitLabIsolation_AC22_GitHubPRFeaturesSendNoGitLabRequest(t *testing.T) {
	e := giBoot(t, giOpts{noIntel: true, spec: func(sp *spec) { sp.env = ghEnv() }})
	e.connect(giToken)
	repo := repoByName(t, e.s, "acme/payments-api")
	pr := pr482(t, e.s)
	glBefore, ghBefore := e.lookups()

	for _, c := range []struct{ method, path string }{
		{http.MethodGet, "/repos/" + repo + "/pulls"},
		{http.MethodGet, "/pulls/" + pr},
		{http.MethodGet, "/pulls/" + pr + "/comments"},
		{http.MethodPost, "/pulls/" + pr + "/intent/refresh"},
		{http.MethodPost, "/pulls/" + pr + "/brief"},
		{http.MethodGet, "/pulls/" + pr + "/history"},
		{http.MethodPost, "/repos/" + repo + "/poll"},
	} {
		// The GitHub stub answers 404 for acme/*, so a poll may fail; the status is not the point.
		e.s.do(c.method, c.path, nil)
	}
	gl, gh := e.lookups()
	if gh == ghBefore {
		t.Error("GitHub saw no request for the GitHub pull request; the check proves nothing")
	}
	if gl != glBefore {
		t.Errorf("GitHub reads sent %d request(s) to GitLab, want none: %v", gl-glBefore, e.gl.since(glBefore))
	}
}

// ---- AC23: the stored token never leaves the process ----

// AC23 (FR31, NFR1): the README excerpt sent to the model has every GitLab token replaced, and after
// forced errors in the connection test, add, sync, clone, refresh and a model call, no response, log
// line, git argument or stored row holds the connection's token.
func TestGitLabIsolation_AC23_TokenNeverLeaves(t *testing.T) {
	files := maps.Clone(tourFixture)
	files["README.md"] = "# Fixture\n\nA tiny module graph.\nPAT: " + giReadmePAT + "\nDeploy: " + giReadmeDT + "\nStored: " + giToken + "\n"
	var failTour, failIntent atomic.Bool
	leak := func(who string) callFn {
		return func(context.Context, llm.Attempt) (llm.AttemptResult, error) {
			return llm.AttemptResult{}, fmt.Errorf("%s upstream said: Add rate limiting; key %s", who, giToken)
		}
	}
	e := giBoot(t, giOpts{files: files, spec: func(sp *spec) { sp.llm = fastLLM() }, handlers: map[string]callFn{
		"OnboardingTour": func(ctx context.Context, at llm.Attempt) (llm.AttemptResult, error) {
			if failTour.Load() {
				return leak("onboarding")(ctx, at)
			}
			return answer(tourAnswer, 900, 300, 0.002), nil
		},
		"IntentClassification": func(ctx context.Context, at llm.Attempt) (llm.AttemptResult, error) {
			if failIntent.Load() {
				return leak("intent")(ctx, at)
			}
			return mockAnswer(ctx, at)
		},
	}})
	rec := &giRecorder{}
	conn := e.connect(giToken)
	repo := e.addRepo(giPath)
	e.s.waitJobs("repo-intel-index", 1)
	pr := e.pullID(repo)
	rec.rec(e.s.get("/pulls/" + pr))

	t.Run("the README excerpt is redacted", func(t *testing.T) {
		resp := rec.rec(e.s.post("/repos/"+repo+"/onboarding", nil))
		if resp.Status != http.StatusOK {
			t.Fatalf("POST onboarding = %d %s, want 200", resp.Status, resp.Body)
		}
		at := attemptsOf(e.sc, "OnboardingTour")
		if len(at) != 1 {
			t.Fatalf("onboarding requests = %d, want 1", len(at))
		}
		user := userOf(at[0])
		if !strings.Contains(user, "### README (excerpt)") || !strings.Contains(user, "<redacted>") {
			t.Errorf("the prompt has no redacted README excerpt:\n%s", user)
		}
		for _, secret := range []string{giReadmePAT, giReadmeDT, giToken} {
			if strings.Contains(user, secret) {
				t.Errorf("the prompt holds %q", secret)
			}
		}
	})

	t.Run("a failing connection test", func(t *testing.T) {
		e.gl.failing("/user", http.StatusUnauthorized, giEcho)
		defer e.gl.setHook(nil)
		resp := rec.rec(e.s.post("/settings/test-connection", map[string]any{"provider": "gitlab", "connection_id": conn}))
		body := resp.Obj(t)
		if resp.Status != http.StatusOK || body["ok"] != false || body["message"] != "GitLab rejected the token (401): it is wrong, expired or revoked" {
			t.Errorf("test-connection = %d %s, want 200 ok:false with the fixed 401 message", resp.Status, resp.Body)
		}
	})
	t.Run("a failing add", func(t *testing.T) {
		defer e.gl.setHook(nil)
		for _, tc := range []struct {
			path, code string
			status     int
		}{
			{"acme/forbidden", "gitlab_permission_denied", http.StatusForbidden},
			{"acme/gone", "gitlab_project_not_found", http.StatusNotFound},
		} {
			e.gl.failing("/projects/"+tc.path, tc.status, giEcho)
			resp := rec.rec(e.s.post("/repos", map[string]string{"url": e.gl.baseURL() + "/" + tc.path}))
			if resp.Status != http.StatusUnprocessableEntity || resp.errorCode(t) != tc.code {
				t.Errorf("POST /repos %s = %d %s, want 422 %s", tc.path, resp.Status, resp.Body, tc.code)
			}
		}
	})
	t.Run("a failing sync", func(t *testing.T) {
		e.gl.failing("/merge_requests", http.StatusForbidden, giEcho)
		defer e.gl.setHook(nil)
		resp := rec.rec(e.s.post("/repos/"+repo+"/poll", nil))
		if resp.Status != http.StatusBadGateway || resp.errorCode(t) != "external_service" {
			t.Errorf("POST poll = %d %s, want 502 external_service", resp.Status, resp.Body)
		}
		if resp := rec.rec(e.s.get("/repos/" + repo + "/pulls")); resp.Status != http.StatusOK || len(list(t, resp)) != 1 {
			t.Errorf("GET pulls after a failed sync = %d %s, want 200 with the stored merge request", resp.Status, resp.Body)
		}
		if resp := rec.rec(e.s.get("/pulls/" + pr)); resp.Status != http.StatusOK {
			t.Errorf("GET /pulls/:id after a failed refresh = %d %s, want 200 (stored)", resp.Status, resp.Body)
		}
	})
	t.Run("a failing clone and refresh", func(t *testing.T) {
		e.gl.setGitErr(giEchoGit)
		defer e.gl.setGitErr("")
		before := len(jobRows(t, e.s.db.DB, "clone"))
		rec.rec(e.s.post("/repos", map[string]string{"url": e.gl.baseURL() + "/" + giBrokenPath}))
		clone := e.s.waitJobs("clone", before+1)
		rec.rec(e.s.post("/repos/"+repo+"/refresh", nil))
		refresh := e.s.waitJobs("refresh", 1)
		for _, j := range []jobRow{clone[len(clone)-1], refresh[0]} {
			if j.Status != "failed" || j.Error == nil || !strings.Contains(*j.Error, "remote error") || !strings.Contains(*j.Error, "[REDACTED]") {
				t.Errorf("%s job = %+v, want failed with git's error and the token redacted", j.Kind, j)
			}
		}
	})
	t.Run("failing model calls", func(t *testing.T) {
		failTour.Store(true)
		failIntent.Store(true)
		resp := rec.rec(e.s.post("/repos/"+repo+"/onboarding", nil))
		if resp.Status != http.StatusBadGateway || resp.errorCode(t) != "generation_failed" || message(t, resp) != "The model call failed" {
			t.Errorf("POST onboarding = %d %s, want the generic 502", resp.Status, resp.Body)
		}
		resp = rec.rec(e.s.post("/pulls/"+pr+"/intent/refresh", nil))
		if resp.Status != http.StatusBadGateway || resp.errorCode(t) != "intent_unavailable" ||
			!strings.Contains(message(t, resp), "intent upstream said: Add rate limiting; key [REDACTED]") {
			t.Errorf("POST intent/refresh = %d %s, want 502 carrying the provider text with the token redacted", resp.Status, resp.Body)
		}
	})

	for _, path := range []string{"/settings", "/settings/gitlab-connections", "/settings/secrets-status", "/workspace/export", "/repos", "/about"} {
		if resp := rec.rec(e.s.get(path)); resp.Status != http.StatusOK {
			t.Errorf("GET %s = %d, want 200", path, resp.Status)
		}
	}

	forbidden := []string{giToken, giBasic, giToken[len("glpat-"):], giToken[:len("glpat-")+8]}
	where := map[string]string{
		"the log": e.s.logs.String(), "the stored rows": e.dump(), "the git arguments": e.gitArgv(),
		"the responses": strings.Join(rec.seen, "\n"),
	}
	// The sweep looks at real material: each place holds what the forced errors left in it.
	for name, marker := range map[string]string{"the log": "gitlab call failed", "the stored rows": "remote error",
		"the git arguments": "http.sslCAInfo=", "the responses": "GitLab request failed"} {
		if !strings.Contains(where[name], marker) {
			t.Errorf("%s holds no %q; the sweep would prove nothing", name, marker)
		}
	}
	if !strings.Contains(e.gitArgvRaw(), "GIT_CONFIG_VALUE_0=Authorization: Basic "+giBasic) {
		t.Error("git was never given the Basic header; the sweep would prove nothing")
	}
	for name, text := range where {
		assertNoSecret(t, name, text, forbidden...)
	}
	for _, line := range strings.Split(e.gitArgvRaw(), "\n") {
		// The per-command header is the one place the credential travels, as an environment value.
		if strings.HasPrefix(line, "GIT_CONFIG_VALUE_") && strings.Contains(line, giBasic) {
			continue
		}
		assertNoSecret(t, "a git call", line, giToken)
	}
}

// NFR1: a server may echo the Basic credential git sends it (a proxy page, a reflected header).
// Both the git side and the REST client scrub that spelling of the token.
func TestGitLabIsolation_AC23_RESTErrorsScrubTheBasicSpelling(t *testing.T) {
	e := giBoot(t, giOpts{noIntel: true})
	e.connect(giToken)
	repo := e.addRepo(giPath)
	e.gl.failing("/merge_requests", http.StatusForbidden, giEchoGit)
	e.s.post("/repos/"+repo+"/poll", nil)
	if !strings.Contains(e.s.logs.String(), "gitlab: HTTP 403") {
		t.Fatal("the failed sync left no log line; the check would prove nothing")
	}
	if strings.Contains(e.s.logs.String(), giBasic) {
		t.Errorf("the log holds the Basic credential of the token:\n%s", e.s.logs.String())
	}
}

// ---- AC24: GitHub-only installations are unchanged ----

// AC24 (NFR6, NFR7, FR16, FR46): with only GitHub repositories and no connection the secrets status
// has exactly four keys, a repository carries only the four added fields, the clone location, the
// git header and the GitHub requests are the ones of before GitLab, and nothing GitLab-shaped is
// written.
func TestGitLabIsolation_AC24_GitHubOnlyBehavesAsBefore(t *testing.T) {
	const token = "ghp_secret_x"
	remote := t.TempDir()
	makeRemote(t, remote, "o", "r")
	binary, callLog := gitWrapper(t, false)
	s := startStack(t, spec{remoteDir: remote, gitBinary: binary, env: map[string]string{"GITHUB_TOKEN": token}})

	t.Run("the secrets status has four keys", func(t *testing.T) {
		resp := s.get("/settings/secrets-status")
		want := map[string]any{"anthropic": false, "github": true, "openai": false, "openrouter": false}
		if diff := cmp.Diff(want, resp.Obj(t)); resp.Status != http.StatusOK || diff != "" {
			t.Errorf("GET /settings/secrets-status = %d %s, mismatch (-want +got):\n%s", resp.Status, resp.Body, diff)
		}
	})
	t.Run("no connection is listed", func(t *testing.T) {
		resp := s.get("/settings/gitlab-connections")
		if resp.Status != http.StatusOK || strings.TrimSpace(string(resp.Body)) != "[]" {
			t.Errorf("GET /settings/gitlab-connections = %d %s, want 200 []", resp.Status, resp.Body)
		}
	})

	resp := s.post("/repos", map[string]string{"url": "https://github.com/o/r"})
	if resp.Status != http.StatusCreated {
		t.Fatalf("POST /repos = %d %s, want 201", resp.Status, resp.Body)
	}
	repo := resp.Obj(t)
	s.waitJobs("clone", 1)

	t.Run("a repository carries only the added fields", func(t *testing.T) {
		keys := slices.Sorted(maps.Keys(repo))
		want := []string{"clone_path", "connection_id", "created_by", "default_branch", "full_name", "host", "id", "last_polled_at",
			"name", "owner", "provider", "web_url", "workspace_id"}
		if diff := cmp.Diff(want, keys); diff != "" {
			t.Errorf("repository keys mismatch (-want +got):\n%s", diff)
		}
		if repo["provider"] != "github" || repo["host"] != "github.com" || repo["web_url"] != "https://github.com/o/r" || repo["connection_id"] != nil {
			t.Errorf("repository = %v, want provider github, host github.com, its github.com address and no connection", repo)
		}
	})
	t.Run("the clone lives where it always did", func(t *testing.T) {
		want := filepath.Join(s.cfg.CloneDir, "o", "r")
		if got := scalar[*string](t, s.db.DB, `SELECT clone_path FROM repos WHERE id = ?`, repo["id"]); got == nil || *got != want {
			t.Errorf("clone_path = %v, want %s", got, want)
		}
		if _, err := os.Stat(filepath.Join(s.cfg.CloneDir, "@gitlab")); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("stat of the GitLab clone root = %v, want it absent", err)
		}
		if _, err := os.Stat(filepath.Join(s.cfg.DataDir, gitCABundleName)); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("stat of the git CA bundle = %v, want it absent without an extra CA", err)
		}
	})
	t.Run("git gets the GitHub header and nothing GitLab", func(t *testing.T) {
		raw, err := os.ReadFile(callLog)
		if err != nil {
			t.Fatal(err)
		}
		basic := base64.StdEncoding.EncodeToString([]byte("x-access-token:" + token))
		lines := strings.Split(string(raw), "\n")
		for _, want := range []string{"GIT_CONFIG_KEY_0=http.https://github.com/.extraHeader", "GIT_CONFIG_VALUE_0=Authorization: Basic " + basic} {
			if !slices.Contains(lines, want) {
				t.Errorf("git calls lack %q:\n%s", want, raw)
			}
		}
		for _, line := range lines {
			if strings.Contains(line, "@gitlab") || strings.Contains(line, "sslCAInfo") || strings.Contains(line, "oauth2") {
				t.Errorf("git call line %q is GitLab-shaped, want none", line)
			}
		}
	})
	t.Run("the GitHub requests are the usual ones", func(t *testing.T) {
		if n := len(s.gh.requests()); n != 0 {
			t.Fatalf("GitHub saw %d request(s) before any pull request read, want none", n)
		}
		listed := s.get("/repos/" + repo["id"].(string) + "/pulls")
		if listed.Status != http.StatusOK {
			t.Fatalf("GET pulls = %d %s, want 200", listed.Status, listed.Body)
		}
		pr := list(t, listed)[0]["id"].(string)
		if resp := s.get("/pulls/" + pr); resp.Status != http.StatusOK {
			t.Fatalf("GET /pulls/:id = %d %s, want 200", resp.Status, resp.Body)
		}
		var got []string
		for _, r := range s.gh.requests() {
			if r.Auth != "Bearer "+token && r.Auth != "token "+token {
				t.Errorf("GitHub request %s %s carried Authorization %q, want the token", r.Method, r.Path, r.Auth)
			}
			got = append(got, r.Method+" "+r.Path)
		}
		// The list backfills the stats of the stored pull request with one detail read; the detail
		// route then reads it again and fetches the linked issue.
		want := []string{
			"GET /repos/o/r/pulls",
			"GET /repos/o/r/pulls/7", "GET /repos/o/r/pulls/7/files", "GET /repos/o/r/pulls/7/commits",
			"GET /repos/o/r/pulls/7", "GET /repos/o/r/pulls/7/files", "GET /repos/o/r/pulls/7/commits",
			"GET /repos/o/r/issues/12",
		}
		if diff := cmp.Diff(want, got); diff != "" {
			t.Errorf("GitHub requests mismatch (-want +got):\n%s", diff)
		}
	})
}

// AC24 (NFR6): the new routes refuse a foreign Origin (403), a non-JSON body (415) and a foreign
// Host (403), like every other route.
func TestGitLabIsolation_AC24_NewRoutesKeepTheGuards(t *testing.T) {
	s := startStack(t, spec{allowedHosts: []string{"localhost", "127.0.0.1", "[::1]", "backend"}})
	const (
		collection = "/settings/gitlab-connections"
		item       = collection + "/00000000-0000-4000-8000-000000000001"
	)
	body := map[string]any{"base_url": "https://gitlab.example", "token": giToken}
	routes := []struct {
		name, method, path string
		body               any
		state              bool
	}{
		{"GET list", http.MethodGet, collection, nil, false},
		{"POST", http.MethodPost, collection, body, true},
		{"PUT", http.MethodPut, item, body, true},
		{"DELETE", http.MethodDelete, item, nil, true},
	}
	for _, rt := range routes {
		t.Run(rt.name+" with a foreign Host", func(t *testing.T) {
			resp := s.do(rt.method, rt.path, rt.body, "Host", "evil.example")
			want := map[string]any{"error": map[string]any{"code": "forbidden", "message": "Host not allowed"}}
			if diff := cmp.Diff(want, resp.JSON(t)); resp.Status != http.StatusForbidden || diff != "" {
				t.Errorf("%s %s with a foreign Host = %d %s, want 403 Host not allowed", rt.method, rt.path, resp.Status, resp.Body)
			}
		})
		if !rt.state {
			continue
		}
		t.Run(rt.name+" with a foreign Origin", func(t *testing.T) {
			resp := s.do(rt.method, rt.path, rt.body, "Origin", giEvil)
			want := map[string]any{"error": map[string]any{"code": "forbidden", "message": "Origin not allowed"}}
			if diff := cmp.Diff(want, resp.JSON(t)); resp.Status != http.StatusForbidden || diff != "" {
				t.Errorf("%s %s with a foreign Origin = %d %s, want 403 Origin not allowed", rt.method, rt.path, resp.Status, resp.Body)
			}
		})
		if rt.body == nil {
			continue
		}
		raw, err := json.Marshal(rt.body)
		if err != nil {
			t.Fatal(err)
		}
		t.Run(rt.name+" with a non-JSON body", func(t *testing.T) {
			for _, mediaType := range []string{"application/xml", "application/x-www-form-urlencoded", "multipart/form-data; boundary=x"} {
				// The refusal is the one of the existing routes: status, code and message.
				other := s.do(http.MethodPut, "/workspace", `{"name":"Kept"}`, "Content-Type", mediaType)
				resp := s.do(rt.method, rt.path, string(raw), "Content-Type", mediaType)
				if resp.Status != http.StatusUnsupportedMediaType || other.Status != http.StatusUnsupportedMediaType {
					t.Errorf("%s %s as %s = %d %s, want 415 (PUT /workspace answers %d)", rt.method, rt.path, mediaType, resp.Status, resp.Body, other.Status)
					continue
				}
				if diff := cmp.Diff(other.Obj(t), resp.Obj(t)); diff != "" {
					t.Errorf("%s %s as %s differs from the existing routes' refusal (-want +got):\n%s", rt.method, rt.path, mediaType, diff)
				}
			}
		})
	}
	if n := count(t, s.db.DB, `SELECT count(*) FROM gitlab_connections`); n != 0 {
		t.Errorf("gitlab_connections rows after the refused requests = %d, want 0", n)
	}
	if _, err := os.Stat(filepath.Join(s.cfg.DataDir, "secrets.json")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("secrets.json after the refused requests: stat error = %v, want it absent", err)
	}
}

// ---- AC37: a repository whose connection was deleted is served from storage ----

// hold makes the first request whose path ends in suffix wait until release is closed (or the
// request is cancelled); entered is closed when that request has arrived.
func (g *giServer) hold(suffix string) (entered <-chan struct{}, release func()) {
	in, rel := make(chan struct{}), make(chan struct{})
	var once, relOnce sync.Once
	g.setHook(func(w http.ResponseWriter, r *http.Request) bool {
		if !strings.HasSuffix(r.URL.Path, suffix) {
			return false
		}
		first := false
		once.Do(func() { first = true; close(in) })
		if first {
			select {
			case <-rel:
			case <-r.Context().Done():
			}
		}
		return false
	})
	return in, func() { relOnce.Do(func() { close(rel) }) }
}

// AC37 (FR48, EC30, EC31, NFR4): with its connection deleted, a GitLab repository still serves its
// stored rows, detail, review and findings, refuses the calls that need GitLab with
// repo_host_not_connected, fails its refresh job without a git command or a retry, and neither
// GitLab nor GitHub receives a single request.
func TestGitLabIsolation_AC37_DeletedConnectionServesStoredData(t *testing.T) {
	e := giBoot(t, giOpts{noIntel: true})
	conn := e.connect(giToken)
	repo := e.addRepo(giPath)
	pr := e.pullID(repo)
	detail := e.s.get("/pulls/" + pr).Obj(t)
	if detail["linked_issue"] == nil {
		t.Fatalf("the connected detail has no linked_issue, want the stub's issue #12: %v", detail)
	}
	f := newFx(t, e.s)
	agent := f.agent("gi-reviewer")
	review := f.review(pr, agent, "", "2026-10-20T10:00:00Z", 80)
	f.finding(review, "a.ts", 1, 1, "CRITICAL", "first")
	listBefore := e.s.get("/repos/" + repo + "/pulls")
	reviewsBefore := e.s.get("/pulls/" + pr + "/reviews")
	if listBefore.Status != http.StatusOK || reviewsBefore.Status != http.StatusOK {
		t.Fatalf("stored reads before the delete = %d / %d, want 200", listBefore.Status, reviewsBefore.Status)
	}

	del := e.s.do(http.MethodDelete, "/settings/gitlab-connections/"+conn, nil)
	if want := map[string]any{"deleted": conn, "disconnected_repos": float64(1)}; del.Status != http.StatusOK || !cmp.Equal(want, del.Obj(t)) {
		t.Fatalf("DELETE connection = %d %s, want 200 %v", del.Status, del.Body, want)
	}
	glBefore, ghBefore := e.lookups()
	gitBefore := e.gitCalls()

	t.Run("the repository is listed as not connected", func(t *testing.T) {
		for _, r := range list(t, e.s.get("/repos")) {
			if r["id"] == repo && (r["provider"] != "gitlab" || r["connection_id"] != nil || r["web_url"] != e.gl.baseURL()+"/"+giPath) {
				t.Errorf("repository = %v, want provider gitlab, no connection and the stored address", r)
			}
		}
	})
	t.Run("stored rows are served", func(t *testing.T) {
		resp := e.s.get("/repos/" + repo + "/pulls")
		if resp.Status != http.StatusOK {
			t.Fatalf("GET pulls = %d %s, want 200", resp.Status, resp.Body)
		}
		if diff := cmp.Diff(list(t, listBefore), list(t, resp)); diff != "" {
			t.Errorf("stored merge requests changed (-before +after):\n%s", diff)
		}
	})
	t.Run("the stored detail has no linked issue", func(t *testing.T) {
		resp := e.s.get("/pulls/" + pr)
		if resp.Status != http.StatusOK {
			t.Fatalf("GET /pulls/:id = %d %s, want 200", resp.Status, resp.Body)
		}
		want := maps.Clone(detail)
		delete(want, "linked_issue")
		got := resp.Obj(t)
		if v, has := got["linked_issue"]; has && v != nil {
			t.Errorf("linked_issue = %v, want none", v)
		}
		delete(got, "linked_issue")
		if diff := cmp.Diff(want, got); diff != "" {
			t.Errorf("stored detail changed (-before +after):\n%s", diff)
		}
	})
	t.Run("the review and findings read as before", func(t *testing.T) {
		resp := e.s.get("/pulls/" + pr + "/reviews")
		if resp.Status != http.StatusOK || !cmp.Equal(list(t, reviewsBefore), list(t, resp)) {
			t.Errorf("GET reviews after the delete = %d %s, want %s", resp.Status, resp.Body, reviewsBefore.Body)
		}
	})
	t.Run("a poll is repo_host_not_connected", func(t *testing.T) {
		resp := e.s.post("/repos/"+repo+"/poll", nil)
		details, _ := resp.Obj(t)["error"].(map[string]any)["details"].(map[string]any)
		if resp.Status != http.StatusUnprocessableEntity || resp.errorCode(t) != "repo_host_not_connected" ||
			details["host"] != e.gl.hostKey() || details["repo_id"] != repo {
			t.Errorf("POST poll = %d %s, want 422 repo_host_not_connected with host %s and repo_id %s", resp.Status, resp.Body, e.gl.hostKey(), repo)
		}
	})
	t.Run("adding the URL is repo_host_not_connected", func(t *testing.T) {
		resp := e.s.post("/repos", map[string]string{"url": e.gl.baseURL() + "/" + giPath})
		details, _ := resp.Obj(t)["error"].(map[string]any)["details"].(map[string]any)
		if resp.Status != http.StatusUnprocessableEntity || resp.errorCode(t) != "repo_host_not_connected" || details["host"] != e.gl.hostKey() {
			t.Errorf("POST /repos = %d %s, want 422 repo_host_not_connected with host %s", resp.Status, resp.Body, e.gl.hostKey())
		}
	})
	t.Run("a refresh job fails without git or a retry", func(t *testing.T) {
		if resp := e.s.post("/repos/"+repo+"/refresh", nil); resp.Status != http.StatusOK {
			t.Fatalf("POST refresh = %d %s, want 200 (the job is enqueued)", resp.Status, resp.Body)
		}
		job := e.s.waitJobs("refresh", 1)[0]
		wantMsg := "No GitLab connection for " + e.gl.hostKey() + ". Add one in Settings → GitLab Integration."
		if job.Status != "failed" || job.Error == nil || *job.Error != wantMsg || job.Attempts != 0 {
			t.Errorf("refresh job = %+v, want failed at once (no retry) with %q", job, wantMsg)
		}
	})

	if gl, gh := e.lookups(); gl != glBefore || gh != ghBefore {
		t.Errorf("after the delete GitLab received %d and GitHub %d request(s), want none; GitLab: %v", gl-glBefore, gh-ghBefore, e.gl.since(glBefore))
	}
	if got := e.gitCalls(); got != gitBefore {
		t.Errorf("git ran %d command(s) after the delete, want none:\n%s", got-gitBefore, e.gitArgv())
	}
}

// AC37 (EC30): a list sync that waits on GitLab when the connection is deleted completes and
// stores its rows; the next sync sends nothing.
func TestGitLabIsolation_AC37_ListSyncInFlightCompletes(t *testing.T) {
	e := giBoot(t, giOpts{noIntel: true})
	conn := e.connect(giToken)
	repo := e.addRepo(giPath)
	entered, release := e.gl.hold("/projects/7/merge_requests")
	defer release()

	type answer struct {
		status int
		body   []byte
		err    error
	}
	done := make(chan answer, 1)
	go func() {
		resp, err := e.s.client.Get(e.s.base + "/repos/" + repo + "/pulls")
		if err != nil {
			done <- answer{err: err}
			return
		}
		defer func() { _ = resp.Body.Close() }()
		body, err := io.ReadAll(resp.Body)
		done <- answer{status: resp.StatusCode, body: body, err: err}
	}()
	select {
	case <-entered:
	case <-time.After(30 * time.Second):
		t.Fatal("the list sync never reached GitLab")
	}
	if resp := e.s.do(http.MethodDelete, "/settings/gitlab-connections/"+conn, nil); resp.Status != http.StatusOK {
		t.Fatalf("DELETE connection = %d %s, want 200", resp.Status, resp.Body)
	}
	release()
	var ans answer
	select {
	case ans = <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("the list sync did not finish after the connection was deleted")
	}
	if ans.err != nil || ans.status != http.StatusOK {
		t.Fatalf("GET pulls = %d %s (%v), want 200", ans.status, ans.body, ans.err)
	}
	if n := count(t, e.s.db.DB, `SELECT count(*) FROM pull_requests WHERE repo_id = ?`, repo); n != 1 {
		t.Errorf("stored merge requests = %d, want the 1 the in-flight sync fetched", n)
	}

	glBefore, ghBefore := e.lookups()
	resp := e.s.get("/repos/" + repo + "/pulls")
	if resp.Status != http.StatusOK || len(list(t, resp)) != 1 {
		t.Errorf("next GET pulls = %d %s, want 200 with the stored merge request", resp.Status, resp.Body)
	}
	if gl, gh := e.lookups(); gl != glBefore || gh != ghBefore {
		t.Errorf("the next sync sent %d GitLab and %d GitHub request(s), want none", gl-glBefore, gh-ghBefore)
	}
}

// AC37 (FR48, EC30), the first request of the sync: the project read waits when the connection is
// deleted. FR48 resolves the connection by the host key at every GitLab call and EC30 lets only the
// request already in flight finish, so the project read completes, no merge request list is
// requested, nothing new is stored, and the response is 200 with the rows stored before.
func TestGitLabIsolation_AC37_ListSyncWaitingOnTheProjectCompletes(t *testing.T) {
	e := giBoot(t, giOpts{noIntel: true})
	conn := e.connect(giToken)
	repo := e.addRepo(giPath)
	if resp := e.s.get("/repos/" + repo + "/pulls"); resp.Status != http.StatusOK || len(list(t, resp)) != 1 {
		t.Fatalf("first GET pulls = %d %s, want 200 with the 1 merge request of the stub", resp.Status, resp.Body)
	}
	rowsBefore := count(t, e.s.db.DB, `SELECT count(*) FROM pull_requests WHERE repo_id = ?`, repo)
	entered, release := e.gl.hold("/api/v4/projects/7")
	defer release()
	glBefore, ghBefore := e.lookups()

	type answer struct {
		resp *response
		err  error
	}
	done := make(chan answer, 1)
	go func() {
		resp, err := e.s.client.Get(e.s.base + "/repos/" + repo + "/pulls")
		if err != nil {
			done <- answer{err: err}
			return
		}
		defer func() { _ = resp.Body.Close() }()
		body, err := io.ReadAll(resp.Body)
		done <- answer{resp: &response{Status: resp.StatusCode, Body: body}, err: err}
	}()
	select {
	case <-entered:
	case <-time.After(30 * time.Second):
		t.Fatal("the list sync never reached GitLab")
	}
	if resp := e.s.do(http.MethodDelete, "/settings/gitlab-connections/"+conn, nil); resp.Status != http.StatusOK {
		t.Fatalf("DELETE connection = %d %s, want 200", resp.Status, resp.Body)
	}
	release()
	var ans answer
	select {
	case ans = <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("the list sync did not finish after the connection was deleted")
	}
	if ans.err != nil || ans.resp.Status != http.StatusOK || len(list(t, *ans.resp)) != int(rowsBefore) {
		t.Fatalf("GET pulls = %+v (%v), want 200 with the %d stored row(s)", ans.resp, ans.err, rowsBefore)
	}
	if n := count(t, e.s.db.DB, `SELECT count(*) FROM pull_requests WHERE repo_id = ?`, repo); n != rowsBefore {
		t.Errorf("stored merge requests = %d, want the %d stored before (no new row)", n, rowsBefore)
	}
	var paths []string
	for _, r := range e.gl.since(glBefore) {
		paths = append(paths, r.Path)
	}
	if want := []string{"/api/v4/projects/7"}; !slices.Equal(paths, want) {
		t.Errorf("GitLab received %v after the sync started, want only the project read %v (no merge request list)", paths, want)
	}
	if _, gh := e.lookups(); gh != ghBefore {
		t.Errorf("GitHub received %d request(s), want none", gh-ghBefore)
	}
}

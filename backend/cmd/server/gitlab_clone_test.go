package main

import (
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"dev-digest-go/backend/internal/config"
	"dev-digest-go/backend/internal/jobs"
)

// Component tests of the GitLab clone and refresh jobs (plan S34: AC13, AC14) through the real
// server, and the GitLab stub that the repo tests (gitlab_repos_test.go) share. The stub is one
// TLS server on 127.0.0.1 that answers the REST and GraphQL calls and serves the bare repositories
// of its projects over smart HTTP with the real `git http-backend`. Its certificate is the
// DEVDIGEST_EXTRA_CA_FILE of the server under test, so the REST client and git both have to trust
// it through the extra CA. A host key is "127.0.0.1:<port>": the loopback address is allowed by
// the dial guard and the stub cannot listen on 443. Helpers are prefixed gr.

// grReq is one request the stub received.
type grReq struct {
	Method, Path, Query string // Path is the escaped path
	PrivateToken        string // PRIVATE-TOKEN of the REST calls
	Authorization       string // Authorization of the git calls
}

// grProject is a project of the stub.
type grProject struct {
	ID     int64
	Path   string // path_with_namespace as GitLab answers it now
	Branch string // default_branch; "" answers null
	WebURL string // web_url of the answer; "" = <base>/<Path>
	Answer string // path_with_namespace of the REST answer when it differs from Path
	repo   string // the bare repository below the git root, without ".git"
}

// grGitLab is a stub GitLab at <https://127.0.0.1:port><prefix>.
type grGitLab struct {
	t       *testing.T
	srv     *httptest.Server
	prefix  string // "" or "/gitlab"
	port    int
	host    string // the host key of its connection
	base    string // the connection's base URL
	caFile  string // PEM of its certificate
	gitRoot string
	backend *cgi.Handler

	mu       sync.Mutex
	reqs     []grReq
	projects []*grProject
	mrs      map[int64][]string
	override func(w http.ResponseWriter, r *http.Request) bool
}

// grNewGitLab starts the stub. prefix is the connection's path prefix.
func grNewGitLab(t *testing.T, prefix string) *grGitLab {
	t.Helper()
	execPath := strings.TrimSpace(grGit(t, "", "--exec-path"))
	backend := filepath.Join(execPath, "git-http-backend")
	if _, err := os.Stat(backend); err != nil {
		t.Skipf("git http-backend is not installed: %v", err)
	}
	g := &grGitLab{t: t, prefix: prefix, gitRoot: t.TempDir(), mrs: map[int64][]string{}}
	g.backend = &cgi.Handler{
		Path: backend, Root: "", Dir: g.gitRoot, InheritEnv: []string{"PATH"}, Stderr: io.Discard,
		Env: []string{"GIT_PROJECT_ROOT=" + g.gitRoot, "GIT_HTTP_EXPORT_ALL=1", "GIT_CONFIG_GLOBAL=" + os.DevNull, "GIT_CONFIG_SYSTEM=" + os.DevNull},
	}
	g.srv = httptest.NewUnstartedServer(http.HandlerFunc(g.serve))
	g.srv.StartTLS()
	t.Cleanup(g.srv.Close)
	g.port = g.srv.Listener.Addr().(*net.TCPAddr).Port
	g.host = "127.0.0.1:" + strconv.Itoa(g.port)
	g.base = "https://" + g.host + prefix
	g.caFile = filepath.Join(t.TempDir(), "gitlab-ca.pem")
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: g.srv.Certificate().Raw})
	if err := os.WriteFile(g.caFile, certPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	return g
}

// grGit runs git in dir with no user or system configuration.
func grGit(t testing.TB, dir string, args ...string) string {
	t.Helper()
	if dir == "" {
		dir = t.TempDir()
	}
	return gitRun(t, dir, args...)
}

// addProject registers a project and creates its bare repository (README.md and main.go on main).
func (g *grGitLab) addProject(id int64, fullPath, branch string) *grProject {
	g.t.Helper()
	p := &grProject{ID: id, Path: fullPath, Branch: branch, repo: fullPath}
	work := g.t.TempDir()
	grGit(g.t, work, "init", "-q", "-b", "main")
	for name, body := range map[string]string{"README.md": "# " + fullPath + "\n", "main.go": "package main\n\nfunc main() {}\n"} {
		if err := os.WriteFile(filepath.Join(work, name), []byte(body), 0o600); err != nil {
			g.t.Fatal(err)
		}
	}
	grGit(g.t, work, "add", ".")
	grGit(g.t, work, "commit", "-q", "-m", "initial")
	dir := filepath.Join(g.gitRoot, path.Dir(fullPath))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		g.t.Fatal(err)
	}
	grGit(g.t, dir, "clone", "-q", "--bare", work, path.Base(fullPath)+".git")
	g.mu.Lock()
	g.projects = append(g.projects, p)
	g.mu.Unlock()
	return p
}

// rename gives project id a new current path; its repository stays where it is.
func (g *grGitLab) rename(id int64, fullPath string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, p := range g.projects {
		if p.ID == id {
			p.Path = fullPath
		}
	}
}

// setWebURL makes the REST answer of project id carry web_url.
func (g *grGitLab) setWebURL(id int64, webURL string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, p := range g.projects {
		if p.ID == id {
			p.WebURL = webURL
		}
	}
}

// setMRs sets the merge request list of project id (JSON objects, see grMR).
func (g *grGitLab) setMRs(id int64, mrs ...string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.mrs[id] = mrs
}

func (g *grGitLab) setOverride(f func(w http.ResponseWriter, r *http.Request) bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.override = f
}

func (g *grGitLab) requests() []grReq {
	g.mu.Lock()
	defer g.mu.Unlock()
	return slices.Clone(g.reqs)
}

func (g *grGitLab) forget() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.reqs = nil
}

// restRequests are the calls below /api/v4 or /api/graphql, gitRequests the smart HTTP ones.
func (g *grGitLab) restRequests() []grReq {
	return g.filter(func(r grReq) bool { return !grIsGitPath(r.Path) })
}
func (g *grGitLab) gitRequests() []grReq {
	return g.filter(func(r grReq) bool { return grIsGitPath(r.Path) })
}

func grIsGitPath(p string) bool { return strings.Contains(p, ".git/") }

func (g *grGitLab) filter(keep func(grReq) bool) []grReq {
	var out []grReq
	for _, r := range g.requests() {
		if keep(r) {
			out = append(out, r)
		}
	}
	return out
}

// projectGETs counts the requests for one project by path or id (not its merge requests).
func (g *grGitLab) projectGETs() []grReq {
	return g.filter(func(r grReq) bool {
		rest, ok := strings.CutPrefix(r.Path, g.prefix+"/api/v4/projects/")
		return ok && r.Method == http.MethodGet && !strings.Contains(rest, "/")
	})
}

func (g *grGitLab) serve(w http.ResponseWriter, r *http.Request) {
	g.mu.Lock()
	g.reqs = append(g.reqs, grReq{
		Method: r.Method, Path: r.URL.EscapedPath(), Query: r.URL.RawQuery,
		PrivateToken: r.Header.Get("PRIVATE-TOKEN"), Authorization: r.Header.Get("Authorization"),
	})
	override := g.override
	g.mu.Unlock()
	if override != nil && override(w, r) {
		return
	}
	esc, ok := strings.CutPrefix(r.URL.EscapedPath(), g.prefix)
	switch {
	case !ok:
		grJSON(w, http.StatusNotFound, `{"message":"404 Not Found"}`)
	case strings.HasPrefix(esc, "/api/v4/projects/"):
		g.rest(w, strings.TrimPrefix(esc, "/api/v4/projects/"))
	case esc == "/api/graphql":
		grJSON(w, http.StatusOK, `{"data":{"projects":{"nodes":[]}}}`)
	case grIsGitPath(esc):
		g.git(w, r, esc)
	default:
		grJSON(w, http.StatusNotFound, `{"message":"404 Not Found"}`)
	}
}

// find returns the project with this id or (case-insensitive) current path.
func (g *grGitLab) find(key string) *grProject {
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, p := range g.projects {
		if strconv.FormatInt(p.ID, 10) == key || strings.EqualFold(p.Path, key) {
			c := *p
			return &c
		}
	}
	return nil
}

func (g *grGitLab) rest(w http.ResponseWriter, rest string) {
	seg, tail, _ := strings.Cut(rest, "/")
	key, err := url.PathUnescape(seg)
	p := g.find(key)
	if err != nil || p == nil {
		grJSON(w, http.StatusNotFound, `{"message":"404 Project Not Found"}`)
		return
	}
	switch tail {
	case "":
		grJSON(w, http.StatusOK, p.json(g.base))
	case "merge_requests":
		g.mu.Lock()
		list := g.mrs[p.ID]
		g.mu.Unlock()
		grJSON(w, http.StatusOK, "["+strings.Join(list, ",")+"]")
	default:
		grJSON(w, http.StatusNotFound, `{"message":"404 Not Found"}`)
	}
}

// git serves <prefix>/<path>.git/... from the project whose current path it is.
func (g *grGitLab) git(w http.ResponseWriter, r *http.Request, esc string) {
	i := strings.Index(esc, ".git/")
	full, err := url.PathUnescape(strings.TrimPrefix(esc[:i], "/"))
	p := g.find(full)
	if err != nil || p == nil || strings.EqualFold(strconv.FormatInt(p.ID, 10), full) {
		http.NotFound(w, r)
		return
	}
	r2 := r.Clone(r.Context())
	r2.URL.Path = "/" + p.repo + esc[i:]
	r2.URL.RawPath = ""
	g.backend.ServeHTTP(w, r2)
}

func (p *grProject) json(base string) string {
	ns, name := path.Split(p.Path)
	ns = strings.TrimSuffix(ns, "/")
	answer := p.Path
	if p.Answer != "" {
		answer = p.Answer
	}
	web := p.WebURL
	if web == "" {
		web = base + "/" + p.Path
	}
	branch := "null"
	if p.Branch != "" {
		branch = strconv.Quote(p.Branch)
	}
	return fmt.Sprintf(`{"id":%d,"path_with_namespace":%q,"path":%q,"namespace":{"full_path":%q},"default_branch":%s,"web_url":%q}`,
		p.ID, answer, name, ns, branch, web)
}

// grMR is the list entry of an open merge request of project id.
func grMR(projectID int64, iid int, title, sha string) string {
	return fmt.Sprintf(`{"iid":%d,"title":%q,"state":"opened","draft":false,"source_branch":"feat","target_branch":"main","sha":%q,`+
		`"source_project_id":%d,"target_project_id":%d,"created_at":"2026-10-20T10:00:00Z","updated_at":"2026-10-20T11:00:00Z",`+
		`"merged_at":null,"closed_at":null,"web_url":"https://elsewhere.invalid/mr/%d","author":{"username":"ada"}}`,
		iid, title, sha, projectID, projectID, iid)
}

func grJSON(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, body)
}

// hostDir is the directory name of the stub's host key in the clone root: ':' becomes '_'.
func (g *grGitLab) hostDir() string { return "127.0.0.1_" + strconv.Itoa(g.port) }

// grBasic is the Authorization header git sends for a token (FR19, D32).
func grBasic(token string) string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte("oauth2:"+token))
}

// grOpts are the server options of a GitLab test.
type grOpts struct {
	remote   string // directory of the GitHub bare repositories (spec.remoteDir)
	git      string // spec.gitBinary
	now      func() time.Time
	pollTick time.Duration
	cfg      func(*config.Config)
}

// grBoot starts the server with g's certificate as DEVDIGEST_EXTRA_CA_FILE, jobs without retries.
func grBoot(t *testing.T, g *grGitLab, o grOpts) *cpStack {
	t.Helper()
	return cpBoot(t, func(sp *spec) {
		sp.jobs = jobs.Options{Retries: -1}
		sp.remoteDir, sp.gitBinary, sp.pollTick = o.remote, o.git, o.pollTick
		if o.now != nil {
			sp.now = o.now
		}
		sp.config = func(c *config.Config) {
			c.ExtraCAFile = g.caFile
			if o.cfg != nil {
				o.cfg(c)
			}
		}
	})
}

// grConnect creates a connection to base with token ("" = none) and returns its id.
func grConnect(t *testing.T, s *cpStack, base, token string) string {
	t.Helper()
	body := map[string]any{"base_url": base}
	if token != "" {
		body["token"] = token
	}
	resp := s.post("/settings/gitlab-connections", body)
	if resp.Status != http.StatusCreated {
		t.Fatalf("POST /settings/gitlab-connections %s = %d %s, want 201", base, resp.Status, resp.Body)
	}
	id, _ := resp.Obj(t)["id"].(string)
	return id
}

// grAdd posts a repository URL and returns the response.
func grAdd(s *cpStack, rawURL string) response {
	s.t.Helper()
	return s.post("/repos", map[string]string{"url": rawURL})
}

// grArgvWrapper is a git wrapper that records the arguments (never the environment, which holds
// the token) of every call to a file, then runs the real git.
func grArgvWrapper(t *testing.T) (binary, logFile string) {
	t.Helper()
	dir := t.TempDir()
	binary, logFile = filepath.Join(dir, "git-argv"), filepath.Join(dir, "argv.log")
	script := "#!/bin/sh\n{ echo '--- call'; for a in \"$@\"; do printf '%s\\n' \"$a\"; done; } >> '" + logFile + "'\nexec git \"$@\"\n"
	if err := os.WriteFile(binary, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return binary, logFile
}

// grRepoPath is the clone_path of the repository id.
func grRepoPath(t testing.TB, s *cpStack, id string) string {
	t.Helper()
	return scalar[string](t, s.db.DB, `SELECT coalesce(clone_path, '') FROM repos WHERE id = ?`, id)
}

// grIndexState waits for the repo-intel index job of the repository to end and returns its state.
func grIndexState(t *testing.T, s *cpStack, id string) map[string]any {
	t.Helper()
	s.waitJobs("repo-intel-index", 1)
	resp := s.get("/repos/" + id + "/index-state")
	if resp.Status != http.StatusOK {
		t.Fatalf("GET /repos/%s/index-state = %d %s, want 200", id, resp.Status, resp.Body)
	}
	return resp.Obj(t)
}

// ---- AC13 ----

// AC13 (FR18-FR20, EC22, NFR2): the clone of a GitLab project lands at the host-and-id location,
// git trusts the extra CA, sends the Basic oauth2 header on every call below the connection's base
// URL, and neither argv, the remote URL, the clone's git config, the logs nor any answer holds the
// token; the index job finds the clone; a GitHub repository named gitlab/gitlab.com clones beside it.
func TestGitLabClone_AC13_TokenLocationAndIndex(t *testing.T) { // AC13
	const token = "glpat-grAC13cloneToken00001"
	g := grNewGitLab(t, "/gitlab")
	g.addProject(42, "acme/platform/payments", "main")
	remote := t.TempDir()
	makeRemote(t, remote, "gitlab", "gitlab.com")
	wrapper, argvLog := grArgvWrapper(t)
	s := grBoot(t, g, grOpts{remote: remote, git: wrapper})
	s.leakCheck(token)
	grConnect(t, s, g.base, token)

	resp := grAdd(s, g.base+"/acme/platform/payments")
	if resp.Status != http.StatusCreated {
		t.Fatalf("POST /repos = %d %s, want 201", resp.Status, resp.Body)
	}
	id, _ := resp.Obj(t)["id"].(string)
	clone := s.waitJobs("clone", 1)
	if clone[0].Status != "done" {
		t.Fatalf("clone job = %+v, want done", clone[0])
	}

	want := filepath.Join(s.cfg.CloneDir, "@gitlab", g.hostDir(), "42")
	if got := grRepoPath(t, s, id); got != want {
		t.Errorf("clone_path = %q, want %q (host key and project id)", got, want)
	}
	if _, err := os.Stat(filepath.Join(want, "main.go")); err != nil {
		t.Errorf("the clone has no main.go: %v", err)
	}
	if got, wantURL := grGit(t, want, "remote", "get-url", "origin"), g.base+"/acme/platform/payments.git"; got != wantURL {
		t.Errorf("origin of the clone = %q, want %q", got, wantURL)
	}

	reqs := g.gitRequests()
	if len(reqs) == 0 {
		t.Fatal("the stub saw no git request")
	}
	for _, r := range reqs {
		if !strings.HasPrefix(r.Path, "/gitlab/acme/platform/payments.git/") {
			t.Errorf("git requested %s, want a URL below %s/", r.Path, g.prefix)
		}
		if r.Authorization != grBasic(token) || r.PrivateToken != "" {
			t.Errorf("git request %s %s sent Authorization %q and PRIVATE-TOKEN %q, want only %q", r.Method, r.Path, r.Authorization, r.PrivateToken, grBasic(token))
		}
	}

	argv, err := os.ReadFile(argvLog)
	if err != nil {
		t.Fatal(err)
	}
	cfgFile, err := os.ReadFile(filepath.Join(want, ".git", "config"))
	if err != nil {
		t.Fatal(err)
	}
	for what, content := range map[string]string{"argv": string(argv), "clone .git/config": string(cfgFile)} {
		for _, secret := range []string{token, grBasic(token), strings.TrimPrefix(grBasic(token), "Basic ")} {
			if strings.Contains(content, secret) {
				t.Errorf("%s contains the token or its header", what)
			}
		}
	}
	if !strings.Contains(string(argv), g.base+"/acme/platform/payments.git") {
		t.Errorf("argv log has no call with the remote URL %s: %.300s", g.base+"/acme/platform/payments.git", argv)
	}

	// The index job finds the clone through the clone key.
	state := grIndexState(t, s, id)
	if status := state["status"]; status != "full" && status != "partial" {
		t.Errorf("index state of the GitLab clone = %v, want full or partial (not no_clone)", state)
	}

	// EC22: a GitHub repository owned by a user named gitlab lands in its own directory.
	gh := s.post("/repos", map[string]string{"url": "https://github.com/gitlab/gitlab.com"})
	if gh.Status != http.StatusCreated {
		t.Fatalf("POST /repos gitlab/gitlab.com = %d %s, want 201", gh.Status, gh.Body)
	}
	if jobsDone := s.waitJobs("clone", 2); jobsDone[1].Status != "done" {
		t.Fatalf("GitHub clone job = %+v, want done", jobsDone[1])
	}
	ghDir := filepath.Join(s.cfg.CloneDir, "gitlab", "gitlab.com")
	if _, err := os.Stat(filepath.Join(ghDir, "f1.txt")); err != nil {
		t.Errorf("the GitHub clone is not at %s: %v", ghDir, err)
	}
	if rel, err := filepath.Rel(ghDir, want); err != nil || !strings.HasPrefix(rel, "..") {
		t.Errorf("the GitLab clone %s lies inside the GitHub clone %s", want, ghDir)
	}
	if entries, err := os.ReadDir(filepath.Join(s.cfg.CloneDir, "gitlab")); err != nil || len(entries) != 1 {
		t.Errorf("entries of %s = %v, %v, want only gitlab.com", filepath.Join(s.cfg.CloneDir, "gitlab"), entries, err)
	}
}

// AC13, EC21: a connection without a token clones anonymously.
func TestGitLabClone_AC13_WithoutTokenIsAnonymous(t *testing.T) { // AC13, EC21
	g := grNewGitLab(t, "")
	g.addProject(42, "acme/platform/payments", "main")
	s := grBoot(t, g, grOpts{})
	grConnect(t, s, g.base, "")

	resp := grAdd(s, g.base+"/acme/platform/payments")
	if resp.Status != http.StatusCreated {
		t.Fatalf("POST /repos = %d %s, want 201", resp.Status, resp.Body)
	}
	if job := s.waitJobs("clone", 1)[0]; job.Status != "done" {
		t.Fatalf("clone job = %+v, want done", job)
	}
	reqs := g.gitRequests()
	if len(reqs) == 0 {
		t.Fatal("the stub saw no git request")
	}
	for _, r := range reqs {
		if r.Authorization != "" || r.PrivateToken != "" {
			t.Errorf("anonymous git request %s %s sent Authorization %q, PRIVATE-TOKEN %q, want none", r.Method, r.Path, r.Authorization, r.PrivateToken)
		}
	}
	for _, r := range g.restRequests() {
		if r.PrivateToken != "" {
			t.Errorf("anonymous REST request %s %s sent a PRIVATE-TOKEN", r.Method, r.Path)
		}
	}
}

// AC13, EC21, FR19: a git remote that answers a redirect is not followed and the job fails.
func TestGitLabClone_AC13_RedirectIsNotFollowed(t *testing.T) { // AC13, EC21
	const token = "glpat-grAC13redirectToken01"
	g := grNewGitLab(t, "")
	g.addProject(42, "acme/platform/payments", "main")
	var mu sync.Mutex
	var targetHits []string
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		targetHits = append(targetHits, r.Method+" "+r.URL.Path+" auth="+r.Header.Get("Authorization"))
		mu.Unlock()
		http.NotFound(w, r)
	}))
	t.Cleanup(target.Close)
	g.setOverride(func(w http.ResponseWriter, r *http.Request) bool {
		if !grIsGitPath(r.URL.EscapedPath()) {
			return false
		}
		http.Redirect(w, r, target.URL+r.URL.EscapedPath(), http.StatusFound)
		return true
	})
	s := grBoot(t, g, grOpts{})
	s.leakCheck(token)
	grConnect(t, s, g.base, token)

	resp := grAdd(s, g.base+"/acme/platform/payments")
	if resp.Status != http.StatusCreated {
		t.Fatalf("POST /repos = %d %s, want 201", resp.Status, resp.Body)
	}
	job := s.waitJobs("clone", 1)[0]
	if job.Status != "failed" || job.Error == nil {
		t.Fatalf("clone job after a redirect = %+v, want failed with an error", job)
	}
	if strings.Contains(*job.Error, token) {
		t.Error("the failed job's error text contains the token")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(targetHits) != 0 {
		t.Errorf("the redirect target received %v, want nothing", targetHits)
	}
	if _, err := os.Stat(filepath.Join(s.cfg.CloneDir, "@gitlab", g.hostDir(), "42")); err == nil {
		t.Error("a clone exists although the remote only redirected")
	}
}

// ---- AC14 ----

// grTree lists every path below root (relative), directories with a trailing slash. Symbolic
// links are listed, not followed.
func grTree(t testing.TB, root string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		if rel == "." {
			return nil
		}
		if d.IsDir() {
			rel += "/"
		}
		out = append(out, rel)
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return out
}

// AC14 (FR18, NFR10): stored values with "..", symbolic links on the way to the clone location and
// a project path that GitLab answers with "..", never create or remove anything outside the two
// accepted location forms. The sentinel files outside the clone root stay as they are.
func TestGitLabClone_AC14_NothingOutsideTheTwoLocationForms(t *testing.T) { // AC14
	g := grNewGitLab(t, "")
	g.addProject(42, "acme/platform/payments", "main")
	g.addProject(43, "acme/other", "main")
	g.mu.Lock()
	g.projects[1].Answer = "acme/../../escape" // GitLab answers a path that leaves the namespace
	g.mu.Unlock()
	s := grBoot(t, g, grOpts{})
	grConnect(t, s, g.base, "glpat-grAC14tokenToken000001")
	cloneDir := s.cfg.CloneDir

	outside := t.TempDir()
	sentinels := map[string]string{}
	for _, name := range []string{"a", "b", "c"} {
		dir := filepath.Join(outside, name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		sentinels[name] = filepath.Join(dir, "keep.txt")
		if err := os.WriteFile(sentinels[name], []byte("keep"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	assertOutsideUntouched := func(when string) {
		t.Helper()
		if diff := grTree(t, outside); !slices.Equal(diff, []string{"a/", "a/keep.txt", "b/", "b/keep.txt", "c/", "c/keep.txt"}) {
			t.Errorf("%s: the tree outside the clone root = %v, want only the three sentinels", when, diff)
		}
		for name, p := range sentinels {
			if body, err := os.ReadFile(p); err != nil || string(body) != "keep" {
				t.Errorf("%s: sentinel %s = %q, %v, want it intact", when, name, body, err)
			}
		}
	}
	assertNoEscape := func(when string) {
		t.Helper()
		for _, root := range []string{s.cfg.DataDir, filepath.Dir(s.cfg.DataDir)} {
			for _, p := range grTree(t, root) {
				if strings.Contains(p, "escape") {
					t.Errorf("%s: %s exists below %s", when, p, root)
				}
			}
		}
	}

	// A GitLab answer whose path leaves the namespace is refused at the add.
	before := s.rows(t, "repos")
	if resp := grAdd(s, g.base+"/acme/other"); resp.Status == http.StatusCreated || resp.Status == http.StatusOK {
		t.Errorf("POST /repos for a project answering the path acme/../../escape = %d %s, want a refusal", resp.Status, resp.Body)
	}
	if n := s.rows(t, "repos"); n != before {
		t.Errorf("repos after the refused add = %d, want %d", n, before)
	}

	// Stored values with ".." in the path never reach git: the clone job fails.
	n := 0
	nextClone := func() jobRow {
		n++
		rows := s.waitJobs("clone", n)
		return rows[len(rows)-1]
	}
	for i, row := range []struct{ owner, name string }{{"acme/../../escape", "x"}, {"acme", ".."}, {"../escape", "payments"}} {
		full := row.owner + "/" + row.name
		err := s.db.DB.Exec(`INSERT INTO repos (workspace_id, owner, name, full_name, provider, host, project_id, web_url)
			VALUES (?, ?, ?, ?, 'gitlab', ?, ?, ?)`, s.env.ws, row.owner, row.name, full, g.host, 900+i, g.base+"/"+full).Error
		if err != nil {
			t.Logf("the database refuses the stored path %q: %v", full, err)
			continue
		}
		repoID := scalar[string](t, s.db.DB, `SELECT id::text FROM repos WHERE project_id = ?`, 900+i)
		if resp := s.post("/repos/"+repoID+"/refresh", nil); resp.Status != http.StatusOK {
			t.Fatalf("POST /repos/%s/refresh = %d %s, want 200", repoID, resp.Status, resp.Body)
		}
		if last := nextClone(); last.Status != "failed" || last.Error == nil || !strings.Contains(*last.Error, "not a plain project path") {
			t.Errorf("clone job of the stored path %q = %+v, want failed with %q", full, last, "not a plain project path")
		}
	}
	assertNoEscape("stored paths with ..")
	assertOutsideUntouched("stored paths with ..")
	if reqs := g.gitRequests(); len(reqs) != 0 {
		t.Errorf("git contacted the stub %d time(s) for a refused path, want 0", len(reqs))
	}

	// A legitimate repository whose location is behind a symbolic link at each level.
	hostDir := filepath.Join(cloneDir, "@gitlab", g.hostDir())
	links := []struct{ name, link, target string }{
		{"top directory", filepath.Join(cloneDir, "@gitlab"), filepath.Join(outside, "a")},
		{"host directory", hostDir, filepath.Join(outside, "b")},
		{"project directory", filepath.Join(hostDir, "42"), filepath.Join(outside, "c")},
	}
	var id string
	for i, l := range links {
		if err := os.RemoveAll(filepath.Join(cloneDir, "@gitlab")); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Dir(l.link), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(l.target, l.link); err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			resp := grAdd(s, g.base+"/acme/platform/payments")
			if resp.Status != http.StatusCreated {
				t.Fatalf("POST /repos = %d %s, want 201", resp.Status, resp.Body)
			}
			id, _ = resp.Obj(t)["id"].(string)
		} else if resp := s.post("/repos/"+id+"/refresh", nil); resp.Status != http.StatusOK {
			t.Fatalf("POST /repos/%s/refresh = %d %s, want 200", id, resp.Status, resp.Body)
		}
		if last := nextClone(); last.Status != "failed" || last.Error == nil || !strings.Contains(*last.Error, "outside the clone root") {
			t.Errorf("clone job behind a symbolic link at the %s = %+v, want failed with %q", l.name, last, "outside the clone root")
		}
		assertOutsideUntouched("symbolic link at the " + l.name)
		if got := grRepoPath(t, s, id); got != "" {
			t.Errorf("clone_path behind a symbolic link at the %s = %q, want none", l.name, got)
		}
	}
	assertNoEscape("symbolic links")

	// Without a link the same job clones into the one accepted location, and nowhere else.
	if err := os.RemoveAll(filepath.Join(cloneDir, "@gitlab")); err != nil {
		t.Fatal(err)
	}
	if resp := s.post("/repos/"+id+"/refresh", nil); resp.Status != http.StatusOK {
		t.Fatalf("POST /repos/%s/refresh = %d %s, want 200", id, resp.Status, resp.Body)
	}
	if last := nextClone(); last.Status != "done" {
		t.Fatalf("clone job without a link = %+v, want done", last)
	}
	want := filepath.Join(hostDir, "42")
	if got := grRepoPath(t, s, id); got != want {
		t.Errorf("clone_path = %q, want %q", got, want)
	}
	for _, p := range grTree(t, cloneDir) {
		top, _, _ := strings.Cut(p, "/")
		if top != "@gitlab" {
			t.Errorf("the clone root holds %s beside @gitlab", p)
		}
	}
	if entries, err := os.ReadDir(filepath.Join(cloneDir, "@gitlab")); err != nil || len(entries) != 1 || entries[0].Name() != g.hostDir() {
		t.Errorf("entries of @gitlab = %v, %v, want only %s", entries, err, g.hostDir())
	}
	if entries, err := os.ReadDir(hostDir); err != nil || len(entries) != 1 || entries[0].Name() != "42" {
		t.Errorf("entries of the host directory = %v, %v, want only 42", entries, err)
	}
	assertOutsideUntouched("final")
	assertNoEscape("final")
}

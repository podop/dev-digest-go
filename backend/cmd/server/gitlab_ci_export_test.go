package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	ciapp "dev-digest-go/backend/internal/ci/app"
	cidomain "dev-digest-go/backend/internal/ci/domain"
	"dev-digest-go/backend/internal/config"
)

// GitLab C component tests of the gitlab_ci export (plan S22: AC1-AC6, AC10-AC14, AC44 publish half)
// through the real server: repository resolution, target and host rules, the check order, the files
// answer with the three root-config decisions from a clone, the open_pr publish sequence against a
// TLS GitLab stub (exact request order, commit actions, merge request description, stored
// installation), the idempotent re-run, the failure mapping, the rename follow, and the guarantee
// that a GitHub export sends GitLab nothing. The stub keeps a small git tree per commit, so create /
// update decisions are checked against what GitLab would accept. The goldens ci_gl_* are written by
// TestContracts_GitLabCIExport. Helpers are prefixed ge (the other GitLab component tests share the
// package).

const (
	geToken     = "glpat-GEexportTokenMarker01"
	geMarker    = "GE-GITLAB-MESSAGE-MARKER-9c2d"
	geProjectID = 42
	gePath      = "acme/web"
	geMainSHA   = "1111111111111111111111111111111111111111"
	geConnName  = "GE Lab"
	geStaleMR   = "https://elsewhere.invalid/acme/web/-/merge_requests/"
	geBuildID   = "abc123def456"
)

// ---- the stub GitLab ----

// geReq is one request the stub saw.
type geReq struct {
	Method, Path, Query string // Path is the escaped path
	Header              http.Header
	Body                []byte
}

// rel is the request as a short line: method, the path below the project with the escapes decoded,
// and for a file lookup the ref ("HEAD /repository/files/a/b.yml @main").
func (r geReq) rel(project int64) string {
	p := strings.TrimPrefix(r.Path, "/api/v4/projects/"+strconv.FormatInt(project, 10))
	if un, err := url.PathUnescape(p); err == nil {
		p = un
	}
	if p == "" {
		p = "/"
	}
	s := r.Method + " " + p
	if r.Method == http.MethodHead {
		q, _ := url.ParseQuery(r.Query)
		s += " @" + q.Get("ref")
	}
	return s
}

func (r geReq) query() url.Values {
	q, _ := url.ParseQuery(r.Query)
	return q
}

// geMR is a merge request of the stub.
type geMR struct {
	IID                   int
	State, Source, Target string
	Title, Description    string
	RemoveSourceBranch    bool
}

// geLab plays a self-hosted GitLab with one project: the endpoints the publish calls (project, file
// metadata by HEAD, branch, commits, merge requests). It keeps a file tree per commit and refuses a
// commit GitLab would refuse (create of an existing file, update of a missing one, start_branch of
// an existing branch, an action that is not `encoding: text`).
type geLab struct {
	t      testing.TB
	srv    *httptest.Server
	caFile string
	token  string

	mu       sync.Mutex
	reqs     []geReq
	id       int64
	path     string
	cfgPath  *string
	trees    map[string]map[string]string // commit sha -> file path -> content
	branches map[string]string            // branch -> commit sha
	mrs      []*geMR
	commitN  int
	conflict bool // the next MR create answers 409 after another MR was opened
	hook     func(w http.ResponseWriter, r *http.Request) bool
	release  chan struct{}
}

func geNewLab(t *testing.T, project int64, fullPath string) *geLab {
	t.Helper()
	l := &geLab{
		t: t, token: geToken, id: project, path: fullPath,
		trees:    map[string]map[string]string{geMainSHA: {}},
		branches: map[string]string{"main": geMainSHA},
		release:  make(chan struct{}),
	}
	l.srv = httptest.NewTLSServer(http.HandlerFunc(l.serve))
	t.Cleanup(l.srv.Close)
	t.Cleanup(func() { close(l.release) }) // runs before Close: a held handler would stall it
	l.caFile = filepath.Join(t.TempDir(), "gitlab-ca.pem")
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: l.srv.Certificate().Raw})
	if err := os.WriteFile(l.caFile, pemBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	return l
}

func (l *geLab) base() string    { return l.srv.URL }
func (l *geLab) hostKey() string { return strings.TrimPrefix(l.srv.URL, "https://") }

// sha is the id of the n-th commit the stub made (1-based).
func (l *geLab) sha(n int) string { return fmt.Sprintf("%040x", 0xc0de0000+n) }

func (l *geLab) setHook(h func(w http.ResponseWriter, r *http.Request) bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.hook = h
}

// rename is the project's new path_with_namespace.
func (l *geLab) rename(fullPath string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.path = fullPath
}

func (l *geLab) setCIConfigPath(p string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.cfgPath = &p
}

// seedMain puts a file on the default branch.
func (l *geLab) seedMain(file, content string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.trees[geMainSHA][file] = content
}

func (l *geLab) setMRState(iid int, state string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, m := range l.mrs {
		if m.IID == iid {
			m.State = state
		}
	}
}

// conflictNext makes the next MR create answer 409, as if somebody had opened one meanwhile.
func (l *geLab) conflictNext() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.conflict = true
}

func (l *geLab) requests() []geReq {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.reqs)
}

func (l *geLab) count() int { return len(l.requests()) }

// since returns the requests after the first n.
func (l *geLab) since(n int) []geReq {
	all := l.requests()
	if n > len(all) {
		n = len(all)
	}
	return all[n:]
}

// seq is the short lines of requests.
func (l *geLab) seq(reqs []geReq) []string {
	out := make([]string, len(reqs))
	for i, r := range reqs {
		out[i] = r.rel(l.id)
	}
	return out
}

func (l *geLab) mrList() []geMR {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]geMR, len(l.mrs))
	for i, m := range l.mrs {
		out[i] = *m
	}
	return out
}

// tree is the files of a ref (a branch name or a commit sha); nil when the ref does not exist.
func (l *geLab) tree(ref string) map[string]string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.treeLocked(ref)
}

func (l *geLab) treeLocked(ref string) map[string]string {
	if sha, ok := l.branches[ref]; ok {
		return l.trees[sha]
	}
	return l.trees[ref]
}

func geJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func geMsg(w http.ResponseWriter, status int, message string) {
	geJSON(w, status, map[string]any{"message": message})
}

func (l *geLab) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	l.mu.Lock()
	l.reqs = append(l.reqs, geReq{
		Method: r.Method, Path: r.URL.EscapedPath(), Query: r.URL.RawQuery, Header: r.Header.Clone(), Body: body,
	})
	hook := l.hook
	l.mu.Unlock()
	if hook != nil && hook(w, r) {
		return
	}
	if r.Header.Get("PRIVATE-TOKEN") != l.token {
		geMsg(w, http.StatusUnauthorized, "401 Unauthorized")
		return
	}
	prefix := "/api/v4/projects/" + strconv.FormatInt(l.id, 10)
	rest, ok := strings.CutPrefix(r.URL.EscapedPath(), prefix)
	if !ok {
		geMsg(w, http.StatusNotFound, "404 Not Found")
		return
	}
	switch {
	case rest == "" && r.Method == http.MethodGet:
		l.serveProject(w)
	case strings.HasPrefix(rest, "/repository/files/") && r.Method == http.MethodHead:
		l.serveFileMeta(w, r, strings.TrimPrefix(rest, "/repository/files/"))
	case strings.HasPrefix(rest, "/repository/branches/") && r.Method == http.MethodGet:
		l.serveBranch(w, strings.TrimPrefix(rest, "/repository/branches/"))
	case rest == "/repository/commits" && r.Method == http.MethodPost:
		l.serveCommit(w, body)
	case rest == "/merge_requests" && r.Method == http.MethodGet:
		l.serveMRList(w, r)
	case rest == "/merge_requests" && r.Method == http.MethodPost:
		l.serveMRCreate(w, body)
	default:
		geMsg(w, http.StatusNotFound, "404 Not Found")
	}
}

func (l *geLab) serveProject(w http.ResponseWriter) {
	l.mu.Lock()
	full, cfg := l.path, l.cfgPath
	l.mu.Unlock()
	geJSON(w, http.StatusOK, map[string]any{
		"id": l.id, "path": path.Base(full), "path_with_namespace": full, "default_branch": "main",
		"web_url": l.base() + "/" + full, "namespace": map[string]any{"full_path": path.Dir(full)},
		"ci_config_path": cfg,
	})
}

func (l *geLab) serveFileMeta(w http.ResponseWriter, r *http.Request, escaped string) {
	file, err := url.PathUnescape(escaped)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	l.mu.Lock()
	tree := l.treeLocked(r.URL.Query().Get("ref"))
	content, ok := tree[file]
	l.mu.Unlock()
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	w.Header().Set("X-Gitlab-Content-Sha256", sha256Of(content))
	w.Header().Set("X-Gitlab-Size", strconv.Itoa(len(content)))
	w.WriteHeader(http.StatusOK)
}

func (l *geLab) serveBranch(w http.ResponseWriter, escaped string) {
	name, err := url.PathUnescape(escaped)
	if err != nil {
		geMsg(w, http.StatusBadRequest, "bad branch")
		return
	}
	l.mu.Lock()
	sha, ok := l.branches[name]
	l.mu.Unlock()
	if !ok {
		geMsg(w, http.StatusNotFound, "404 Branch Not Found")
		return
	}
	geJSON(w, http.StatusOK, map[string]any{"name": name, "commit": map[string]any{"id": sha}})
}

func (l *geLab) serveCommit(w http.ResponseWriter, body []byte) {
	var in struct {
		Branch      string `json:"branch"`
		StartBranch string `json:"start_branch"`
		Message     string `json:"commit_message"`
		Actions     []struct {
			Action   string `json:"action"`
			FilePath string `json:"file_path"`
			Content  string `json:"content"`
			Encoding string `json:"encoding"`
		} `json:"actions"`
	}
	if err := json.Unmarshal(body, &in); err != nil || in.Branch == "" || len(in.Actions) == 0 {
		geMsg(w, http.StatusBadRequest, "400 bad commit")
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	parentSHA, exists := l.branches[in.Branch]
	switch {
	case exists && in.StartBranch != "":
		geMsg(w, http.StatusBadRequest, "A branch called '"+in.Branch+"' already exists. Switch to that branch in order to make changes")
		return
	case !exists && in.StartBranch == "":
		geMsg(w, http.StatusBadRequest, "You can only create or edit files when you are on a branch")
		return
	case !exists:
		var ok bool
		if parentSHA, ok = l.branches[in.StartBranch]; !ok {
			geMsg(w, http.StatusBadRequest, "Invalid start_branch")
			return
		}
	}
	next := geCopy(l.trees[parentSHA])
	for _, a := range in.Actions {
		_, present := next[a.FilePath]
		switch {
		case a.Encoding != "text":
			geMsg(w, http.StatusBadRequest, "encoding must be text, got "+a.Encoding)
			return
		case a.Action == "create" && present:
			geMsg(w, http.StatusBadRequest, "A file with this name already exists: "+a.FilePath)
			return
		case a.Action == "update" && !present:
			geMsg(w, http.StatusBadRequest, "A file with this name doesn't exist: "+a.FilePath)
			return
		case a.Action != "create" && a.Action != "update":
			geMsg(w, http.StatusBadRequest, "unknown action "+a.Action)
			return
		}
		next[a.FilePath] = a.Content
	}
	l.commitN++
	sha := l.sha(l.commitN)
	l.trees[sha] = next
	l.branches[in.Branch] = sha
	geJSON(w, http.StatusCreated, map[string]any{"id": sha, "message": in.Message})
}

func geCopy(m map[string]string) map[string]string {
	out := make(map[string]string, len(m)+8)
	for k, v := range m {
		out[k] = v
	}
	return out
}

func (l *geLab) mrJSON(m *geMR, decoy bool) map[string]any {
	web := l.base() + "/" + l.path + "/-/merge_requests/" + strconv.Itoa(m.IID)
	if decoy {
		web = geStaleMR + strconv.Itoa(m.IID)
	}
	return map[string]any{
		"iid": m.IID, "title": m.Title, "state": m.State, "draft": false, "description": m.Description,
		"source_branch": m.Source, "target_branch": m.Target, "sha": geMainSHA,
		"source_project_id": l.id, "target_project_id": l.id,
		"created_at": "2026-10-20T10:00:00Z", "updated_at": "2026-10-20T10:00:00Z", "merged_at": nil, "closed_at": nil,
		"web_url": web, "author": map[string]any{"username": "ann"},
	}
}

func (l *geLab) serveMRList(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	l.mu.Lock()
	defer l.mu.Unlock()
	out := []map[string]any{}
	for _, m := range l.mrs {
		if q.Get("state") == "opened" && m.State != "opened" {
			continue
		}
		if q.Get("source_branch") != "" && m.Source != q.Get("source_branch") {
			continue
		}
		if q.Get("target_branch") != "" && m.Target != q.Get("target_branch") {
			continue
		}
		out = append(out, l.mrJSON(m, true))
	}
	geJSON(w, http.StatusOK, out)
}

func (l *geLab) serveMRCreate(w http.ResponseWriter, body []byte) {
	var in struct {
		Source string `json:"source_branch"`
		Target string `json:"target_branch"`
		Title  string `json:"title"`
		Desc   string `json:"description"`
		Remove bool   `json:"remove_source_branch"`
	}
	if err := json.Unmarshal(body, &in); err != nil {
		geMsg(w, http.StatusBadRequest, "bad merge request")
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.conflict {
		l.conflict = false
		m := &geMR{IID: len(l.mrs) + 1, State: "opened", Source: in.Source, Target: in.Target, Title: "opened by somebody else"}
		l.mrs = append(l.mrs, m)
		geMsg(w, http.StatusConflict, "Another open merge request already exists for this source branch: !"+strconv.Itoa(m.IID))
		return
	}
	m := &geMR{IID: len(l.mrs) + 1, State: "opened", Source: in.Source, Target: in.Target, Title: in.Title, Description: in.Desc,
		RemoveSourceBranch: in.Remove}
	l.mrs = append(l.mrs, m)
	geJSON(w, http.StatusCreated, l.mrJSON(m, true))
}

// geOn answers the requests with that method whose decoded path ends with suffix.
func geOn(method, suffix string, answer http.HandlerFunc) func(http.ResponseWriter, *http.Request) bool {
	return func(w http.ResponseWriter, r *http.Request) bool {
		if r.Method != method || !strings.HasSuffix(r.URL.Path, suffix) {
			return false
		}
		answer(w, r)
		return true
	}
}

// geStatus answers with a status and a message that repeats the token and a marker, like a careless
// proxy would: neither may reach any answer or log line of the server.
func geStatus(status int) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		geMsg(w, status, fmt.Sprintf("%d %s: token %s was rejected", status, geMarker, geToken))
	}
}

// geReset closes the connection without an answer.
func geReset(w http.ResponseWriter, _ *http.Request) {
	if conn, _, err := w.(http.Hijacker).Hijack(); err == nil {
		_ = conn.Close()
	}
}

// geHang holds the request until the client gives up or the test ends.
func (l *geLab) geHang(w http.ResponseWriter, r *http.Request) {
	select {
	case <-r.Context().Done():
	case <-l.release:
	}
}

// ---- server and fixtures ----

// geRunner is the runner bundle of the tests; off answers "no runner" (the untagged build).
type geRunner struct{ off atomic.Bool }

func (g *geRunner) Runner() (ciapp.RunnerArtifact, bool) {
	if g.off.Load() {
		return ciapp.RunnerArtifact{}, false
	}
	return ciFakeRunner().Runner()
}

// geEnv is a server over a fresh database that trusts the labs' certificate.
type geEnv struct {
	t      *testing.T
	e      *sbEnv
	s      *cpStack
	gh     *ciGH
	runner *geRunner
}

// geBoot starts the server. Every lab shares httptest's certificate, so the first one's file is the
// extra CA of all of them (cfg.ExtraCAFile). GitHub is the double of the Phase 6 tests.
func geBoot(t *testing.T, labs ...*geLab) *geEnv {
	t.Helper()
	runner := &geRunner{}
	e := newSBEnv(t)
	s := cpBootOn(t, e, func(sp *spec) {
		sp.env = map[string]string{"GITHUB_TOKEN": ciToken}
		sp.runner = runner
		sp.config = func(c *config.Config) { c.ExtraCAFile = labs[0].caFile }
	})
	gh := newCiGH()
	s.gh.setOverride(gh.handle)
	s.leakCheck(geToken, geMarker)
	return &geEnv{t: t, e: e, s: s, gh: gh, runner: runner}
}

// connect creates the connection to the lab and returns its id.
func (g *geEnv) connect(l *geLab, token string) string {
	g.t.Helper()
	name := geConnName
	if l.id != geProjectID {
		name += " " + strconv.FormatInt(l.id, 10)
	}
	body := map[string]any{"name": name, "base_url": l.base()}
	if token != "" {
		body["token"] = token
	}
	resp := g.s.post("/settings/gitlab-connections", body)
	if resp.Status != http.StatusCreated {
		g.t.Fatalf("POST /settings/gitlab-connections = %d %s, want 201", resp.Status, resp.Body)
	}
	return idOf(g.t, resp)
}

// addRepo inserts the GitLab repository of the lab's project (web address of the lab) and returns its
// id. clone is the clone directory ("" = no clone yet).
func (g *geEnv) addRepo(l *geLab, full, clone string) string {
	g.t.Helper()
	return g.addRepoWithID(l, full, clone, l.id)
}

func (g *geEnv) agent(name string) string {
	g.t.Helper()
	return ciCreateAgent(g.t, g.s.stack, name, "openrouter")
}

// agentWithSkill is the "CI Reviewer" agent with the skill error-handling.
func (g *geEnv) agentWithSkill() (agent, skill string) {
	g.t.Helper()
	agent = g.agent("CI Reviewer")
	skill = ciCreateSkill(g.t, g.s.stack, "error-handling", "Check every error.", true)
	ciLinkSkills(g.t, g.s.stack, agent, skill)
	return agent, skill
}

// geBlock is the gitlab_ci block of an answer.
type geBlock struct {
	JobFile        string  `json:"job_file"`
	RootConfig     string  `json:"root_config"`
	ConfigPath     string  `json:"config_path"`
	IncludeSnippet string  `json:"include_snippet"`
	TriggersNote   *string `json:"triggers_note"`
}

type geInstallation struct {
	ciInstallation
	Provider string `json:"provider"`
	Host     string `json:"host"`
}

// geExported is an export answer.
type geExported struct {
	Installation      *geInstallation `json:"installation"`
	Files             []ciFile        `json:"files"`
	PRURL             *string         `json:"pr_url"`
	RequiredSecrets   []string        `json:"required_secrets"`
	RequiredVariables []string        `json:"required_variables"`
	GitLabCI          *geBlock        `json:"gitlab_ci"`
}

// geBody is an export body for a repository: repo (full name) and repo_id as the UI sends them.
func geBody(full, repoID, action string, extra map[string]any) map[string]any {
	body := map[string]any{"repo": full, "action": action}
	if repoID != "" {
		body["repo_id"] = repoID
	}
	for k, v := range extra {
		body[k] = v
	}
	return body
}

func (g *geEnv) export(agent string, body map[string]any, want int) (geExported, response) {
	g.t.Helper()
	resp := g.s.post("/agents/"+agent+"/export-ci", body)
	if resp.Status != want {
		g.t.Fatalf("POST /agents/%s/export-ci %v = %d %s, want %d", agent, body, resp.Status, resp.Body, want)
	}
	var out geExported
	if want < 300 {
		if err := json.Unmarshal(resp.Body, &out); err != nil {
			g.t.Fatalf("export body: %v: %s", err, resp.Body)
		}
	}
	return out, resp
}

// refuse sends an export that must be refused and returns the error.
func (g *geEnv) refuse(agent string, body map[string]any, status int, code string) ciError {
	g.t.Helper()
	resp := g.s.post("/agents/"+agent+"/export-ci", body)
	e := ciErr(g.t, resp)
	if resp.Status != status || e.Code != code {
		g.t.Fatalf("POST /agents/%s/export-ci %v = %d %s %q, want %d %s", agent, body, resp.Status, e.Code, e.Message, status, code)
	}
	return e
}

func (g *geEnv) installRows(agent string) int {
	return count(g.t, g.s.db.DB, `SELECT count(*) FROM ci_installations WHERE agent_id = ?`, agent)
}

// geClone makes a clone of a repository with the files, as the clone job leaves it.
func geClone(t *testing.T, files map[string]string) string {
	t.Helper()
	work := t.TempDir()
	gitRun(t, work, "init", "-q", "-b", "main")
	files["README.md"] = "# project\n"
	for rel, content := range files {
		full := filepath.Join(work, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	gitRun(t, work, "add", ".")
	gitRun(t, work, "commit", "-q", "-m", "fixture")
	dest := filepath.Join(t.TempDir(), "clone")
	gitRun(t, filepath.Dir(dest), "clone", "-q", work, dest)
	return dest
}

// ---- expected texts (spec 7.3, 7.4), written out independently of the production builders ----

func geWantJob(slug, postAs string, selfHosted bool) string {
	tags := ""
	if selfHosted {
		tags = "  tags: [devdigest]\n"
	}
	return "# DevDigest Review · " + slug + " — generated by DevDigest; included from the root CI configuration.\n" +
		"devdigest-review-" + slug + ":\n" +
		"  stage: test\n" +
		"  image:\n" +
		"    name: " + cidomain.JobImage + "\n" +
		"    entrypoint: [\"\"]\n" +
		tags +
		"  inherit:\n" +
		"    default: false\n" +
		"    variables: false\n" +
		"  rules:\n" +
		"    - if: $CI_PIPELINE_SOURCE == \"merge_request_event\" && $CI_MERGE_REQUEST_SOURCE_PROJECT_ID == $CI_MERGE_REQUEST_PROJECT_ID\n" +
		"  interruptible: true\n" +
		"  variables:\n" +
		"    GIT_DEPTH: \"0\"\n" +
		"    DEVDIGEST_PLATFORM: gitlab_ci\n" +
		"    DEVDIGEST_AGENT: " + slug + "\n" +
		"    DEVDIGEST_POST_AS: " + postAs + "\n" +
		"  script:\n" +
		"    - mkdir -p \"/tmp/devdigest-$CI_JOB_ID\"\n" +
		"    - base64 -d \"$CI_PROJECT_DIR/.devdigest/runner/devdigest-runner-linux-amd64.gz.b64\" > \"/tmp/devdigest-$CI_JOB_ID/devdigest-runner.gz\"\n" +
		"    - gzip -dc \"/tmp/devdigest-$CI_JOB_ID/devdigest-runner.gz\" > \"/tmp/devdigest-$CI_JOB_ID/devdigest-runner\"\n" +
		"    - cd \"/tmp/devdigest-$CI_JOB_ID\" && sha256sum -c \"$CI_PROJECT_DIR/.devdigest/runner/devdigest-runner-linux-amd64.sha256\"\n" +
		"    - chmod +x \"/tmp/devdigest-$CI_JOB_ID/devdigest-runner\"\n" +
		"    - cd \"$CI_PROJECT_DIR\" && \"/tmp/devdigest-$CI_JOB_ID/devdigest-runner\"\n" +
		"  artifacts:\n" +
		"    when: always\n" +
		"    name: devdigest-result-" + slug + "\n" +
		"    paths:\n" +
		"      - devdigest-result.json\n" +
		"    expire_in: 30 days\n"
}

// geInsecureJob is the generated job with a before_script added: rule include (EC11).
func geInsecureJob() string {
	return strings.Replace(geWantJob("ci-reviewer", "github_review", false), "  interruptible: true\n",
		"  interruptible: true\n  before_script:\n    - echo hi\n", 1)
}

func geWantRoot(slug string) string {
	return "# Added by DevDigest. Add your own jobs below or in other included files.\n" +
		"include:\n" +
		"  - local: .gitlab/devdigest/review-" + slug + ".yml\n"
}

// geWantPaths are the FR6 paths in order; withRoot adds the root include file.
func geWantPaths(slug string, withRoot bool, skills ...string) []string {
	paths := []string{".devdigest/agents/" + slug + ".yaml"}
	for _, sk := range skills {
		paths = append(paths, ".devdigest/skills/"+sk+".md")
	}
	paths = append(paths,
		".devdigest/memory.jsonl",
		".devdigest/runner/devdigest-runner-linux-amd64.gz.b64",
		".devdigest/runner/devdigest-runner-linux-amd64.sha256",
		".gitlab/devdigest/review-"+slug+".yml")
	if withRoot {
		paths = append(paths, ".gitlab-ci.yml")
	}
	return paths
}

// geDesc are the facts of a merge request description.
type geDesc struct {
	slug      string
	paths     []string
	secrets   []string
	variables []string
	root      string // create | keep_existing | custom_path
	config    string // the root configuration the include goes into
	note      string // the FR4 note, "" = none
}

func geWantDesc(d geDesc) string {
	var files []string
	for _, p := range d.paths {
		files = append(files, "- `"+p+"`")
	}
	var steps [][]string
	if len(d.secrets) > 0 || len(d.variables) > 0 {
		var s []string
		if len(d.secrets) > 0 {
			s = append(s,
				"Add these CI/CD variables (Settings > CI/CD > Variables), each **masked** and **not protected**",
				"   (merge request pipelines cannot read protected variables):")
			for _, name := range d.secrets {
				if name == "DEVDIGEST_GITLAB_TOKEN" {
					s = append(s,
						"   - `DEVDIGEST_GITLAB_TOKEN` — an access token with the `api` scope that DevDigest posts as",
						"     (a project access token with the Reporter role is recommended; on gitlab.com project access tokens need",
						"     Premium or Ultimate, otherwise use a personal access token of a dedicated user).")
					continue
				}
				s = append(s, "   - `"+name+"`")
			}
		}
		if len(d.variables) > 0 {
			s = append(s, "   And as a visible variable: `"+d.variables[0]+"` = the gateway base URL.")
		}
		s = append(s, "   This merge request runs the review job on itself, so its pipeline fails until the variables exist.")
		steps = append(steps, s)
	}
	if d.root != "create" {
		steps = append(steps, []string{
			"Add this to `" + d.config + "`:",
			"   ```yaml",
			"   include:",
			"     - local: .gitlab/devdigest/review-" + d.slug + ".yml",
			"   ```",
			"   If your workflow:rules exclude merge request pipelines, the review job cannot run.",
			"   Your stages must include test, or change the job's stage.",
		})
	}
	steps = append(steps,
		[]string{"Merge this merge request."},
		[]string{`To block merges on blockers, enable "Pipelines must succeed" (Settings > Merge requests > Merge checks).`})
	lines := []string{
		"Adds the DevDigest review agent `" + d.slug + "` to this project's GitLab CI.",
		"",
		"### Files",
		strings.Join(files, "\n"),
		"",
		"### Before merging",
	}
	for i, s := range steps {
		s[0] = strconv.Itoa(i+1) + ". " + s[0]
		lines = append(lines, s...)
	}
	if d.note != "" {
		lines = append(lines, d.note+".")
	}
	lines = append(lines,
		"",
		"### Runner",
		"`.devdigest/runner/devdigest-runner-linux-amd64.gz.b64` is the DevDigest CI runner (linux/amd64), built from DevDigest source `"+geBuildID+
			"`. SHA-256 of the decoded binary: `"+ciRunnerSHA()+"`. The job checks it before running.",
		"",
		"Branch: `devdigest/ci`. Generated by DevDigest.")
	return strings.Join(lines, "\n")
}

var geSecrets = []string{"OPENROUTER_API_KEY", "DEVDIGEST_GITLAB_TOKEN"}

// geCheckFiles compares the files of an answer with FR6/FR7/FR15 for an openrouter agent.
func (g *geEnv) geCheckFiles(got []ciFile, agent, slug string, withRoot bool, skills map[string]string, order []string, job string) {
	g.t.Helper()
	var gotPaths []string
	for _, f := range got {
		gotPaths = append(gotPaths, f.Path)
	}
	if diff := cmp.Diff(geWantPaths(slug, withRoot, order...), gotPaths); diff != "" {
		g.t.Fatalf("file paths mismatch (-want +got):\n%s", diff)
	}
	for _, f := range got {
		var want string
		switch {
		case strings.HasPrefix(f.Path, ".devdigest/skills/"):
			want = skills[strings.TrimSuffix(strings.TrimPrefix(f.Path, ".devdigest/skills/"), ".md")]
		case f.Path == ".devdigest/memory.jsonl":
			want = ""
		case strings.HasSuffix(f.Path, ".gz.b64"):
			want = ciFakeRunnerEncoded()
		case strings.HasSuffix(f.Path, ".sha256"):
			want = ciRunnerSHA() + "  devdigest-runner\n"
		case f.Path == ".gitlab-ci.yml":
			want = geWantRoot(slug)
		case strings.HasPrefix(f.Path, ".gitlab/"):
			want = job
		default:
			ciCheckManifest(g.t, g.s.stack, agent, f.Contents, order)
			if f.Editable {
				g.t.Errorf("manifest %s is editable, want false", f.Path)
			}
			continue
		}
		if f.Contents != want {
			g.t.Errorf("file %s contents = %q, want %q", f.Path, f.Contents, want)
		}
		if wantEditable := strings.HasPrefix(f.Path, ".gitlab/"); f.Editable != wantEditable {
			g.t.Errorf("file %s editable = %v, want %v (only the job file is editable)", f.Path, f.Editable, wantEditable)
		}
	}
}

func ciFakeRunnerEncoded() string {
	art, _ := ciFakeRunner().Runner()
	return art.Encoded
}

// geImageRE is the pinned image line of FR8: name[:tag]@sha256:<64 hex>.
var geImageRE = regexp.MustCompile(`(?m)^    name: [a-z0-9][a-z0-9._/-]*(:[A-Za-z0-9._-]{1,128})?@sha256:[0-9a-f]{64}$`)

func sha256Of(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// ---- AC1, EC1: repository resolution ----

func TestGitLabCIExport_AC1_RepositoryResolution(t *testing.T) { // AC1, FR1, EC1
	a, b := geNewLab(t, 11, gePath), geNewLab(t, 22, gePath)
	env := geBoot(t, a, b)
	env.connect(a, geToken)
	b.token = "glpat-GEsecondInstanceToken1"
	env.connect(b, b.token)
	env.s.leakCheck(b.token)
	github := env.e.repo(t, gePath)
	ra, rb := env.addRepo(a, gePath, ""), env.addRepo(b, gePath, "")
	agent, _ := env.agentWithSkill()

	// A GitHub repository with that full name wins (A EC25 unchanged).
	out, _ := env.export(agent, geBody(gePath, "", "files", nil), http.StatusOK)
	if out.GitLabCI != nil || len(out.Files) == 0 || !strings.HasPrefix(out.Files[len(out.Files)-1].Path, ".github/workflows/") {
		t.Errorf("export of acme/web with a GitHub twin = gitlab_ci %v, last file %v; want the GitHub export", out.GitLabCI, out.Files)
	}
	ghFiles := out.Files

	// Without the GitHub one the name is ambiguous: 422 with both ids.
	env.e.exec(t, `DELETE FROM repos WHERE id = ?`, github)
	resp := env.s.post("/agents/"+agent+"/export-ci", geBody(gePath, "", "files", nil))
	e := ciErr(t, resp)
	if resp.Status != http.StatusUnprocessableEntity || e.Code != "repo_ambiguous" ||
		e.Message != "acme/web exists on several hosts; choose the repository by id" {
		t.Fatalf("export with two GitLab twins = %d %s %q, want 422 repo_ambiguous", resp.Status, e.Code, e.Message)
	}
	details, _ := e.Details.(map[string]any)
	var ids []string
	for _, v := range details["repo_ids"].([]any) {
		ids = append(ids, v.(string))
	}
	slices.Sort(ids)
	if want := []string{ra, rb}; !slices.Equal(ids, slices.Sorted(slices.Values(want))) {
		t.Errorf("repo_ambiguous details.repo_ids = %v, want %v", ids, want)
	}

	// repo_id picks one (and wins over repo).
	gl, _ := env.export(agent, geBody(gePath, rb, "files", nil), http.StatusOK)
	if gl.GitLabCI == nil || gl.GitLabCI.JobFile != ".gitlab/devdigest/review-ci-reviewer.yml" {
		t.Errorf("export with repo_id of the second host: gitlab_ci = %+v, want the job file block", gl.GitLabCI)
	}
	if _, resp := env.export(agent, geBody("other/ignored", ra, "files", nil), http.StatusOK); !strings.Contains(string(resp.Body), `"gitlab_ci":{`) {
		t.Errorf("repo_id with another repo text = %s, want the GitLab export (repo is ignored)", resp.Body)
	}
	// FR6: manifest, skills, memory and runner files equal those of the GitHub export of the same agent.
	if len(ghFiles) != 6 || len(gl.Files) != 7 {
		t.Fatalf("files: github %d, gitlab %d, want 6 and 7 (root include listed)", len(ghFiles), len(gl.Files))
	}
	if diff := cmp.Diff(ghFiles[:5], gl.Files[:5]); diff != "" {
		t.Errorf("gitlab_ci files 1-5 differ from the gha export (-gha +gitlab):\n%s", diff)
	}

	// An unknown repository id is not found.
	e = env.refuse(agent, geBody(gePath, "00000000-0000-4000-8000-000000000999", "files", nil), http.StatusNotFound, "repo_not_found")
	if e.Message != "Repository is not imported in this workspace" {
		t.Errorf("repo_not_found message = %q", e.Message)
	}
	env.refuse(agent, geBody("acme/none", "", "files", nil), http.StatusNotFound, "repo_not_found")
	if n := a.count() + b.count(); n != 0 {
		t.Errorf("GitLab requests of the files exports = %d, want 0", n)
	}
}

// ---- AC2, EC2: target and host ----

func TestGitLabCIExport_AC2_TargetFollowsTheHost(t *testing.T) { // AC2, FR2, EC2
	lab := geNewLab(t, geProjectID, gePath)
	env := geBoot(t, lab)
	env.connect(lab, geToken)
	glRepo := env.addRepo(lab, gePath, "")
	ghRepo := env.e.repo(t, "acme/payments-api")
	agent, _ := env.agentWithSkill()

	const (
		needsGitHub = "GitHub Actions export needs a GitHub repository"
		needsGitLab = "GitLab CI export needs a GitLab repository"
	)
	for _, tc := range []struct {
		name   string
		repo   string
		full   string
		target any // nil = omitted
		status int
		code   string
		msg    string
		gitlab bool
	}{
		{name: "gitlab, target omitted", repo: glRepo, full: gePath, status: 200, gitlab: true},
		{name: "gitlab, gha", repo: glRepo, full: gePath, target: "gha", status: 422, code: "unsupported_target", msg: needsGitHub},
		{name: "gitlab, gitlab_ci", repo: glRepo, full: gePath, target: "gitlab_ci", status: 200, gitlab: true},
		{name: "gitlab, circle", repo: glRepo, full: gePath, target: "circle", status: 422, code: "unsupported_target"},
		{name: "github, gitlab_ci", repo: ghRepo, full: "acme/payments-api", target: "gitlab_ci", status: 422, code: "unsupported_target", msg: needsGitLab},
		{name: "github, target omitted", repo: ghRepo, full: "acme/payments-api", status: 200},
		{name: "github, gha", repo: ghRepo, full: "acme/payments-api", target: "gha", status: 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			extra := map[string]any{}
			if tc.target != nil {
				extra["target"] = tc.target
			}
			resp := env.s.post("/agents/"+agent+"/export-ci", geBody(tc.full, tc.repo, "files", extra))
			if resp.Status != tc.status {
				t.Fatalf("export = %d %s, want %d", resp.Status, resp.Body, tc.status)
			}
			if strings.Contains(string(resp.Body), "unsupported_host") {
				t.Errorf("answer %s names unsupported_host, which is never produced", resp.Body)
			}
			if tc.status == 422 {
				if e := ciErr(t, resp); e.Code != tc.code || (tc.msg != "" && e.Message != tc.msg) {
					t.Errorf("error = %s %q, want %s %q", e.Code, e.Message, tc.code, tc.msg)
				}
				return
			}
			var out geExported
			if err := json.Unmarshal(resp.Body, &out); err != nil {
				t.Fatal(err)
			}
			if (out.GitLabCI != nil) != tc.gitlab {
				t.Errorf("gitlab_ci = %+v, want present %v", out.GitLabCI, tc.gitlab)
			}
		})
	}
	if n := lab.count(); n != 0 {
		t.Errorf("GitLab requests = %d, want 0", n)
	}
}

// ---- AC3, EC3: a repository whose connection was deleted ----

func TestGitLabCIExport_AC3_NotConnected(t *testing.T) { // AC3 (export half), FR2, EC3
	lab := geNewLab(t, geProjectID, gePath)
	env := geBoot(t, lab)
	connID := env.connect(lab, geToken)
	repo := env.addRepo(lab, gePath, "")
	agent, _ := env.agentWithSkill()
	if resp := env.s.del("/settings/gitlab-connections/" + connID); resp.Status != http.StatusOK {
		t.Fatalf("DELETE connection = %d %s, want 200", resp.Status, resp.Body)
	}
	insecure := geInsecureJob()
	for _, tc := range []struct {
		name   string
		extra  map[string]any
		action string
	}{
		{"files", nil, "files"},
		{"open_pr", nil, "open_pr"},
		{"before every later check: an insecure job file and a bad slug", map[string]any{"workflow": insecure}, "open_pr"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := env.refuse(agent, geBody(gePath, repo, tc.action, tc.extra), http.StatusUnprocessableEntity, "repo_host_not_connected")
			want := map[string]any{"host": lab.hostKey(), "repo_id": repo}
			if diff := cmp.Diff(want, e.Details); diff != "" {
				t.Errorf("details mismatch (-want +got):\n%s", diff)
			}
		})
	}
	if n := lab.count(); n != 0 {
		t.Errorf("GitLab requests = %d, want 0", n)
	}
	if n := env.installRows(agent); n != 0 {
		t.Errorf("installation rows = %d, want 0", n)
	}
}

// ---- AC4: the check order ----

func TestGitLabCIExport_AC4_CheckOrder(t *testing.T) { // AC4, FR3
	lab, other := geNewLab(t, geProjectID, gePath), geNewLab(t, 99, "acme/unconnected")
	env := geBoot(t, lab, other)
	env.connect(lab, "") // a connection without a token: open_pr stops at the last check
	repo := env.addRepo(lab, gePath, "")
	unconnected := env.addRepo(other, "acme/unconnected", "")

	agent := env.agent("CI Reviewer")
	rival := env.agent("Rival")
	ghost := env.agent("Ghost Reviewer")
	env.e.exec(t, `UPDATE agents SET provider = 'custom:ghost' WHERE id = ?`, ghost)
	a, b := ciCreateSkill(t, env.s.stack, "first-skill", "one", true), ciCreateSkill(t, env.s.stack, "second-skill", "two", true)
	ciLinkSkills(t, env.s.stack, agent, a, b)
	env.e.exec(t, `UPDATE skills SET name = 'Error handling' WHERE id = ?`, a)
	env.e.exec(t, `UPDATE skills SET name = 'error-handling' WHERE id = ?`, b)
	// The rival agent owns the slug in this project.
	env.e.exec(t, `INSERT INTO ci_installations (agent_id, repo, target_type, slug, manifest_version, provider, host, project_id)
		VALUES (?, ?, 'gitlab_ci', 'ci-reviewer', 1, 'openrouter', ?, ?)`, rival, gePath, lab.hostKey(), geProjectID)
	// Every case below also has all the problems of the later ones; each is fixed after its turn.
	env.runner.off.Store(true)
	env.e.exec(t, `UPDATE agents SET model = '' WHERE id = ?`, agent)
	insecure := geInsecureJob()
	open := func(repoID, full string, extra map[string]any) map[string]any {
		if extra == nil {
			extra = map[string]any{}
		}
		extra["workflow"] = insecure
		return geBody(full, repoID, "open_pr", extra)
	}

	steps := []struct {
		name    string
		agent   string
		body    map[string]any
		status  int
		code    string
		details any
		fix     func()
	}{
		{"unknown agent", "00000000-0000-4000-8000-000000000999", open(repo, gePath, map[string]any{"triggers": []string{"closed"}}), 404, "agent_not_found", nil, nil},
		{"undefined custom provider", ghost, open(repo, gePath, map[string]any{"triggers": []string{"closed"}}), 422, "provider_not_configured", nil, nil},
		{"bad triggers", agent, open(repo, gePath, map[string]any{"triggers": []string{"closed"}}), 422, "invalid_triggers", nil, nil},
		{"unknown repository", agent, open("00000000-0000-4000-8000-000000000999", gePath, nil), 404, "repo_not_found", nil, nil},
		{"target does not fit the host", agent, open(repo, gePath, map[string]any{"target": "gha"}), 422, "unsupported_target", nil, nil},
		{"host without a connection", agent, open(unconnected, "acme/unconnected", nil), 422, "repo_host_not_connected",
			map[string]any{"host": other.hostKey(), "repo_id": unconnected}, nil},
		{"slug owned by another agent", agent, open(repo, gePath, nil), 409, "ci_slug_conflict", nil,
			func() { env.e.exec(t, `DELETE FROM ci_installations WHERE agent_id = ?`, rival) }},
		{"colliding skill file names", agent, open(repo, gePath, nil), 422, "skill_slug_conflict", nil,
			func() { env.e.exec(t, `UPDATE skills SET name = 'second-skill' WHERE id = ?`, a) }},
		{"insecure job file", agent, open(repo, gePath, nil), 422, "workflow_insecure", map[string]any{"rule": "include"}, nil},
		{"no runner", agent, geBody(gePath, repo, "open_pr", nil), 500, "runner_bundle_missing", nil,
			func() { env.runner.off.Store(false) }},
		{"invalid manifest", agent, geBody(gePath, repo, "open_pr", nil), 422, "manifest_invalid", nil,
			func() { env.e.exec(t, `UPDATE agents SET model = 'deepseek/deepseek-v4-flash' WHERE id = ?`, agent) }},
		{"no token on open_pr", agent, geBody(gePath, repo, "open_pr", nil), 500, "config_error", nil, nil},
	}
	for _, st := range steps {
		resp := env.s.post("/agents/"+st.agent+"/export-ci", st.body)
		e := ciErr(t, resp)
		if resp.Status != st.status || e.Code != st.code {
			t.Fatalf("%s: export = %d %s %q, want %d %s", st.name, resp.Status, e.Code, e.Message, st.status, st.code)
		}
		if st.details != nil {
			if diff := cmp.Diff(st.details, e.Details); diff != "" {
				t.Errorf("%s: details mismatch (-want +got):\n%s", st.name, diff)
			}
		}
		if st.code == "config_error" && e.Message != "GitLab connection "+geConnName+" has no token" {
			t.Errorf("%s: message = %q, want the connection named without a token", st.name, e.Message)
		}
		if n := lab.count() + other.count(); n != 0 {
			t.Fatalf("%s: GitLab requests = %d %v, want 0", st.name, n, lab.seq(lab.requests()))
		}
		if st.fix != nil {
			st.fix()
		}
	}
	if n := env.installRows(agent); n != 0 {
		t.Errorf("installation rows of the agent = %d, want 0", n)
	}
}

// ---- AC5: the triggers note ----

func TestGitLabCIExport_AC5_TriggersNote(t *testing.T) { // AC5, FR4
	lab := geNewLab(t, geProjectID, gePath)
	env := geBoot(t, lab)
	env.connect(lab, geToken)
	repo := env.addRepo(lab, gePath, "")
	agent, _ := env.agentWithSkill()

	const note = "GitLab runs merge request pipelines on every push; trigger choices do not apply"
	out, _ := env.export(agent, geBody(gePath, repo, "files", map[string]any{"triggers": []string{"opened"}}), http.StatusOK)
	if out.GitLabCI.TriggersNote == nil || *out.GitLabCI.TriggersNote != note {
		t.Errorf("triggers_note for [opened] = %v, want %q", out.GitLabCI.TriggersNote, note)
	}
	var job string
	for _, f := range out.Files {
		if strings.HasPrefix(f.Path, ".gitlab/devdigest/") {
			job = f.Contents
		}
	}
	if job != geWantJob("ci-reviewer", "github_review", false) || strings.Contains(job, "opened") || strings.Contains(job, "types") {
		t.Errorf("job file for [opened] = %q, want the unchanged job with no trigger list", job)
	}
	for name, triggers := range map[string][]string{
		"all three":   {"opened", "synchronize", "reopened"},
		"other order": {"reopened", "opened", "synchronize"},
		"omitted":     nil,
	} {
		extra := map[string]any{}
		if triggers != nil {
			extra["triggers"] = triggers
		}
		out, _ := env.export(agent, geBody(gePath, repo, "files", extra), http.StatusOK)
		if out.GitLabCI.TriggersNote != nil {
			t.Errorf("triggers_note for %s = %q, want null", name, *out.GitLabCI.TriggersNote)
		}
	}
	if n := lab.count(); n != 0 {
		t.Errorf("GitLab requests = %d, want 0", n)
	}
}

// ---- AC6, EC4-EC6 preview: root config from the clone ----

func TestGitLabCIExport_AC6_RootConfigFromTheClone(t *testing.T) { // AC6, FR5, NFR6
	lab := geNewLab(t, geProjectID, gePath)
	env := geBoot(t, lab)
	env.connect(lab, geToken)
	withRoot := geClone(t, map[string]string{".gitlab-ci.yml": "stages: [test]\n"})
	without := geClone(t, map[string]string{"main.go": "package main\n"})
	agent, _ := env.agentWithSkill()

	for i, tc := range []struct {
		name  string
		clone string
		want  string
	}{
		{"a clone with .gitlab-ci.yml", withRoot, "keep_existing"},
		{"a clone without it", without, "create"},
		{"no clone", "", "create"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			full := fmt.Sprintf("acme/web-%d", i)
			repo := env.addRepoWithID(lab, full, tc.clone, int64(1000+i))
			out, _ := env.export(agent, geBody(full, repo, "files", nil), http.StatusOK)
			if out.GitLabCI == nil || out.GitLabCI.RootConfig != tc.want || out.GitLabCI.ConfigPath != ".gitlab-ci.yml" ||
				out.GitLabCI.IncludeSnippet != "include:\n  - local: .gitlab/devdigest/review-ci-reviewer.yml\n" {
				t.Fatalf("gitlab_ci = %+v, want root_config %s, config_path .gitlab-ci.yml and the include snippet", out.GitLabCI, tc.want)
			}
			listed := out.Files[len(out.Files)-1].Path == ".gitlab-ci.yml"
			if listed != (tc.want == "create") {
				t.Errorf("root file listed = %v with root_config %s, want listed only for create", listed, tc.want)
			}
			if n := len(out.Files); (tc.want == "create") != (n == 7) {
				t.Errorf("files = %d, want 7 for create and 6 for keep_existing", n)
			}
		})
	}
	if n := lab.count(); n != 0 {
		t.Errorf("GitLab requests = %d, want 0 (files makes none)", n)
	}
	if n := count(t, env.s.db.DB, `SELECT count(*) FROM ci_installations`); n != 0 {
		t.Errorf("ci_installations rows = %d, want 0 (files writes none)", n)
	}
}

// addRepoWithID is addRepo for a project id other than the lab's own (a files export reads none).
func (g *geEnv) addRepoWithID(l *geLab, full, clone string, project int64) string {
	g.t.Helper()
	var clonePath any
	if clone != "" {
		clonePath = clone
	}
	return g.e.id(g.t, `INSERT INTO repos (workspace_id, owner, name, full_name, provider, host, project_id, web_url, default_branch, clone_path)
		VALUES (?, ?, ?, ?, 'gitlab', ?, ?, ?, 'main', ?) RETURNING id::text`,
		g.e.ws, path.Dir(full), path.Base(full), full, l.hostKey(), project, l.base()+"/"+full, clonePath)
}

// ---- AC7 (files over HTTP) and AC10: the open_pr sequence ----

func TestGitLabCIExport_AC10_OpenPRSequence(t *testing.T) { // AC10, AC44 (budget), FR11, FR12, FR13, NFR1
	lab := geNewLab(t, geProjectID, gePath)
	env := geBoot(t, lab)
	env.connect(lab, geToken)
	repo := env.addRepo(lab, gePath, "")
	agent, _ := env.agentWithSkill()
	skills := map[string]string{"error-handling": "Check every error."}

	preview, _ := env.export(agent, geBody(gePath, repo, "files", nil), http.StatusOK)
	job := geWantJob("ci-reviewer", "github_review", false)
	env.geCheckFiles(preview.Files, agent, "ci-reviewer", true, skills, []string{"error-handling"}, job)
	if !strings.HasSuffix(job, "\n") || !geImageRE.MatchString(job) {
		t.Errorf("job file does not pin the image by digest:\n%s", job)
	}

	out, _ := env.export(agent, geBody(gePath, repo, "open_pr", nil), http.StatusCreated)
	reqs := lab.requests()
	files := geWantPaths("ci-reviewer", true, "error-handling")
	wantSeq := []string{"GET /", "HEAD /repository/files/.gitlab-ci.yml @main", "GET /repository/branches/devdigest/ci"}
	for _, f := range files[:len(files)-1] { // the root file was looked up as the root decision
		wantSeq = append(wantSeq, "HEAD /repository/files/"+f+" @main")
	}
	wantSeq = append(wantSeq, "POST /repository/commits", "GET /merge_requests", "POST /merge_requests")
	if diff := cmp.Diff(wantSeq, lab.seq(reqs)); diff != "" {
		t.Fatalf("GitLab request sequence mismatch (-want +got):\n%s", diff)
	}
	// FR11 budget (NFR6): 1 project + 1 branch + 1 file lookup per exported file + 1 commit + 2 MR requests.
	if max := 1 + 1 + len(files) + 1 + 2; len(reqs) > max {
		t.Errorf("publish made %d requests, budget %d", len(reqs), max)
	}
	for _, r := range reqs {
		if got := r.Header.Get("PRIVATE-TOKEN"); got != geToken {
			t.Errorf("%s PRIVATE-TOKEN = %q, want the connection's token", r.rel(geProjectID), got)
		}
		if !strings.HasPrefix(r.Path, "/api/v4/projects/42") {
			t.Errorf("request %s %s is not below the connection's REST root", r.Method, r.Path)
		}
		if r.Header.Get("Authorization") != "" {
			t.Errorf("%s carries an Authorization header", r.rel(geProjectID))
		}
	}
	const jobEsc = "/api/v4/projects/42/repository/files/.gitlab%2Fdevdigest%2Freview-ci-reviewer.yml"
	if reqs[0].Path != "/api/v4/projects/42" || reqs[2].Path != "/api/v4/projects/42/repository/branches/devdigest%2Fci" ||
		!slices.ContainsFunc(reqs, func(r geReq) bool { return r.Method == http.MethodHead && r.Path == jobEsc }) {
		t.Errorf("escaped paths = %q, %q; want the project by id and the branch and file names as single escaped segments",
			reqs[0].Path, reqs[2].Path)
	}

	commit := reqs[len(reqs)-3]
	var cb struct {
		Branch      string `json:"branch"`
		StartBranch string `json:"start_branch"`
		Message     string `json:"commit_message"`
		Actions     []struct {
			Action   string `json:"action"`
			FilePath string `json:"file_path"`
			Content  string `json:"content"`
			Encoding string `json:"encoding"`
		} `json:"actions"`
	}
	if err := json.Unmarshal(commit.Body, &cb); err != nil {
		t.Fatal(err)
	}
	if cb.Branch != "devdigest/ci" || cb.StartBranch != "main" || cb.Message != "Add DevDigest CI review" {
		t.Errorf("commit = branch %q start_branch %q message %q, want devdigest/ci, main, Add DevDigest CI review", cb.Branch, cb.StartBranch, cb.Message)
	}
	if len(cb.Actions) != len(out.Files) {
		t.Fatalf("commit has %d actions, want one per file (%d)", len(cb.Actions), len(out.Files))
	}
	for i, a := range cb.Actions {
		if a.Action != "create" || a.FilePath != out.Files[i].Path || a.Content != out.Files[i].Contents || a.Encoding != "text" {
			t.Errorf("action %d = %s %s encoding %s, want a text create of %s with the file's contents", i, a.Action, a.FilePath, a.Encoding, out.Files[i].Path)
		}
	}
	env.geCheckFiles(out.Files, agent, "ci-reviewer", true, skills, []string{"error-handling"}, job)

	q := reqs[len(reqs)-2].query()
	if q.Get("state") != "opened" || q.Get("source_branch") != "devdigest/ci" || q.Get("target_branch") != "main" {
		t.Errorf("MR lookup query = %v, want opened devdigest/ci -> main", q)
	}
	var mb map[string]any
	if err := json.Unmarshal(reqs[len(reqs)-1].Body, &mb); err != nil {
		t.Fatal(err)
	}
	wantMR := map[string]any{
		"source_branch": "devdigest/ci", "target_branch": "main", "title": "Add DevDigest CI review", "remove_source_branch": true,
		"description": geWantDesc(geDesc{slug: "ci-reviewer", paths: files, secrets: geSecrets, root: "create", config: ".gitlab-ci.yml"}),
	}
	if diff := cmp.Diff(wantMR, mb); diff != "" {
		t.Errorf("MR create body mismatch (-want +got):\n%s", diff)
	}

	wantURL := lab.base() + "/acme/web/-/merge_requests/1"
	if out.PRURL == nil || *out.PRURL != wantURL {
		t.Errorf("pr_url = %v, want %s (the stored web address and the iid, never the answer's web_url)", out.PRURL, wantURL)
	}
	if strings.Contains(string(mustJSON(t, out)), "elsewhere.invalid") {
		t.Error("the answer holds the stub's web_url")
	}
	version := int(ciAgentField(t, env.s.stack, agent, "version").(float64))
	in := out.Installation
	if in == nil || in.AgentID != agent || in.TargetType != "gitlab_ci" || in.Repo != gePath || in.Provider != "gitlab" || in.Host != lab.hostKey() ||
		in.Slug == nil || *in.Slug != "ci-reviewer" || in.WorkflowPath == nil || *in.WorkflowPath != ".gitlab/devdigest/review-ci-reviewer.yml" ||
		in.ManifestVersion == nil || *in.ManifestVersion != version || in.LastRun != nil {
		t.Errorf("installation = %+v, want gitlab_ci on %s, slug ci-reviewer, workflow_path of the job file, manifest_version %d", in, lab.hostKey(), version)
	}
	if out.GitLabCI == nil || out.GitLabCI.RootConfig != "create" || out.GitLabCI.JobFile != ".gitlab/devdigest/review-ci-reviewer.yml" {
		t.Errorf("gitlab_ci = %+v, want root_config create", out.GitLabCI)
	}
	if diff := cmp.Diff(geSecrets, out.RequiredSecrets); diff != "" {
		t.Errorf("required_secrets mismatch (-want +got):\n%s", diff)
	}
	var row struct {
		Host, Repo, Slug, TargetType string
		ProjectID                    int64
	}
	if err := env.s.db.DB.Raw(`SELECT host, repo, slug, target_type, project_id FROM ci_installations WHERE agent_id = ?`, agent).Scan(&row).Error; err != nil {
		t.Fatal(err)
	}
	if want := (struct {
		Host, Repo, Slug, TargetType string
		ProjectID                    int64
	}{lab.hostKey(), gePath, "ci-reviewer", "gitlab_ci", geProjectID}); row != want {
		t.Errorf("stored installation = %+v, want %+v", row, want)
	}
	if n := env.installRows(agent); n != 1 {
		t.Errorf("installation rows = %d, want 1", n)
	}
	// The stub holds what was committed.
	tree := lab.tree("devdigest/ci")
	for _, f := range out.Files {
		if tree[f.Path] != f.Contents {
			t.Errorf("GitLab file %s = %q, want the exported contents", f.Path, tree[f.Path])
		}
	}
	if m := lab.mrList(); len(m) != 1 || m[0].Description != wantMR["description"] || !m[0].RemoveSourceBranch {
		t.Errorf("GitLab merge requests = %+v, want the one created with the description", m)
	}
}

func mustJSON(t testing.TB, v any) []byte {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// ---- AC11, EC4-EC6: the root decision against GitLab ----

func TestGitLabCIExport_AC11_RootDecisionAgainstGitLab(t *testing.T) { // AC11, FR11, EC4, EC5, EC6
	t.Run("keep_existing", func(t *testing.T) {
		lab := geNewLab(t, geProjectID, gePath)
		lab.seedMain(".gitlab-ci.yml", "stages: [test]\n")
		env := geBoot(t, lab)
		env.connect(lab, geToken)
		repo := env.addRepo(lab, gePath, "")
		agent, _ := env.agentWithSkill()
		skills := map[string]string{"error-handling": "Check every error."}

		out, _ := env.export(agent, geBody(gePath, repo, "open_pr", map[string]any{"triggers": []string{"opened"}}), http.StatusCreated)
		if out.GitLabCI.RootConfig != "keep_existing" || out.GitLabCI.ConfigPath != ".gitlab-ci.yml" {
			t.Errorf("gitlab_ci = %+v, want keep_existing at .gitlab-ci.yml", out.GitLabCI)
		}
		env.geCheckFiles(out.Files, agent, "ci-reviewer", false, skills, []string{"error-handling"}, geWantJob("ci-reviewer", "github_review", false))
		commit := lab.commits(t)
		if len(commit) != 1 || len(commit[0].Actions) != 6 {
			t.Fatalf("commits = %+v, want one with 6 actions", commit)
		}
		for _, a := range commit[0].Actions {
			if a.FilePath == ".gitlab-ci.yml" {
				t.Errorf("an action touches .gitlab-ci.yml: %+v", a)
			}
		}
		if got := lab.tree("main")[".gitlab-ci.yml"]; got != "stages: [test]\n" {
			t.Errorf(".gitlab-ci.yml on GitLab = %q, want it byte-identical", got)
		}
		want := geWantDesc(geDesc{slug: "ci-reviewer", paths: geWantPaths("ci-reviewer", false, "error-handling"), secrets: geSecrets,
			root: "keep_existing", config: ".gitlab-ci.yml", note: "GitLab runs merge request pipelines on every push; trigger choices do not apply"})
		if mrs := lab.mrList(); len(mrs) != 1 || mrs[0].Description != want {
			t.Errorf("MR description mismatch (-want +got):\n%s", cmp.Diff(want, firstDesc(lab)))
		}
	})

	t.Run("custom_path", func(t *testing.T) {
		lab := geNewLab(t, geProjectID, gePath)
		lab.setCIConfigPath("ci/main.yml")
		env := geBoot(t, lab)
		env.connect(lab, geToken)
		repo := env.addRepo(lab, gePath, "")
		agent, _ := env.agentWithSkill()
		skills := map[string]string{"error-handling": "Check every error."}

		out, _ := env.export(agent, geBody(gePath, repo, "open_pr", map[string]any{"post_as": "pr_comment", "runner": "self_hosted"}), http.StatusCreated)
		if out.GitLabCI.RootConfig != "custom_path" || out.GitLabCI.ConfigPath != "ci/main.yml" {
			t.Errorf("gitlab_ci = %+v, want custom_path at ci/main.yml", out.GitLabCI)
		}
		env.geCheckFiles(out.Files, agent, "ci-reviewer", false, skills, []string{"error-handling"}, geWantJob("ci-reviewer", "pr_comment", true))
		for _, r := range lab.requests() {
			if strings.Contains(r.Path, "ci/main.yml") || strings.Contains(r.Path, ".gitlab-ci.yml") {
				t.Errorf("request %s looks at a root config file, want none for a custom path", r.rel(geProjectID))
			}
		}
		for _, a := range lab.commits(t)[0].Actions {
			if a.FilePath == ".gitlab-ci.yml" || a.FilePath == "ci/main.yml" {
				t.Errorf("an action touches %s", a.FilePath)
			}
		}
		want := geWantDesc(geDesc{slug: "ci-reviewer", paths: geWantPaths("ci-reviewer", false, "error-handling"), secrets: geSecrets,
			root: "custom_path", config: "ci/main.yml"})
		if got := firstDesc(lab); got != want {
			t.Errorf("MR description mismatch (-want +got):\n%s", cmp.Diff(want, got))
		}
	})

	t.Run("a stale clone says create while GitLab has the file", func(t *testing.T) {
		lab := geNewLab(t, geProjectID, gePath)
		lab.seedMain(".gitlab-ci.yml", "stages: [test]\n")
		env := geBoot(t, lab)
		env.connect(lab, geToken)
		clone := geClone(t, map[string]string{"main.go": "package main\n"}) // no .gitlab-ci.yml
		repo := env.addRepo(lab, gePath, clone)
		agent, _ := env.agentWithSkill()

		preview, _ := env.export(agent, geBody(gePath, repo, "files", nil), http.StatusOK)
		if preview.GitLabCI.RootConfig != "create" || len(preview.Files) != 7 {
			t.Fatalf("preview = %s with %d files, want create with 7 (the clone is stale)", preview.GitLabCI.RootConfig, len(preview.Files))
		}
		out, _ := env.export(agent, geBody(gePath, repo, "open_pr", nil), http.StatusCreated)
		if out.GitLabCI.RootConfig != "keep_existing" || len(out.Files) != 6 {
			t.Errorf("open_pr answer = %s with %d files, want keep_existing with the 6 committed files", out.GitLabCI.RootConfig, len(out.Files))
		}
		if n := len(lab.commits(t)[0].Actions); n != 6 {
			t.Errorf("commit actions = %d, want 6", n)
		}
	})
}

func firstDesc(l *geLab) string {
	if m := l.mrList(); len(m) > 0 {
		return m[0].Description
	}
	return ""
}

// geCommit is a commit request body.
type geCommit struct {
	Branch      string `json:"branch"`
	StartBranch string `json:"start_branch"`
	Message     string `json:"commit_message"`
	Actions     []struct {
		Action   string `json:"action"`
		FilePath string `json:"file_path"`
		Content  string `json:"content"`
		Encoding string `json:"encoding"`
	} `json:"actions"`
}

// commits are the commit requests the stub received, in order.
func (l *geLab) commits(t testing.TB) []geCommit {
	t.Helper()
	var out []geCommit
	for _, r := range l.requests() {
		if r.Method == http.MethodPost && strings.HasSuffix(r.Path, "/repository/commits") {
			var c geCommit
			if err := json.Unmarshal(r.Body, &c); err != nil {
				t.Fatal(err)
			}
			out = append(out, c)
		}
	}
	return out
}

// ---- AC12, EC7, EC8: idempotent re-run ----

func TestGitLabCIExport_AC12_IdempotentReRun(t *testing.T) { // AC12, FR11, FR12, EC7, EC8
	lab := geNewLab(t, geProjectID, gePath)
	env := geBoot(t, lab)
	env.connect(lab, geToken)
	repo := env.addRepo(lab, gePath, "")
	agent, skill := env.agentWithSkill()
	open := func() geExported {
		out, _ := env.export(agent, geBody(gePath, repo, "open_pr", nil), http.StatusCreated)
		return out
	}

	first := open()
	n := lab.count()
	if len(lab.commits(t)) != 1 {
		t.Fatalf("first export committed %d times, want 1", len(lab.commits(t)))
	}

	// Identical content: no commit, no MR create, the open MR reused.
	again := open()
	later := lab.since(n)
	for _, r := range later {
		if r.Method == http.MethodPost {
			t.Errorf("identical re-export sent %s, want no write", r.rel(geProjectID))
		}
	}
	if *again.PRURL != *first.PRURL || again.Installation.ID != first.Installation.ID {
		t.Errorf("re-export = pr_url %s installation %s, want %s and %s", *again.PRURL, again.Installation.ID, *first.PRURL, first.Installation.ID)
	}
	if mrs := lab.mrList(); len(mrs) != 1 {
		t.Errorf("merge requests after the re-export = %d, want 1", len(mrs))
	}
	// The unchanged branch is the parent: the file lookups go to its head, not to the default branch.
	sawHead := false
	for _, r := range later {
		if r.Method == http.MethodHead && r.query().Get("ref") == lab.sha(1) {
			sawHead = true
		}
	}
	if !sawHead {
		t.Errorf("re-export lookups = %v, want file lookups at the branch head %s", lab.seq(later), lab.sha(1))
	}

	// A changed skill body: one commit, one update action, no start_branch.
	if resp := env.s.put("/skills/"+skill, map[string]any{"body": "Check every error, twice."}); resp.Status != http.StatusOK {
		t.Fatalf("PUT /skills = %d %s, want 200", resp.Status, resp.Body)
	}
	open()
	commits := lab.commits(t)
	if len(commits) != 2 {
		t.Fatalf("commits after the skill change = %d, want 2 in total", len(commits))
	}
	last := commits[1]
	if len(last.Actions) != 1 || last.Actions[0].Action != "update" || last.Actions[0].FilePath != ".devdigest/skills/error-handling.md" ||
		last.Actions[0].Content != "Check every error, twice." || last.StartBranch != "" || last.Branch != "devdigest/ci" {
		t.Errorf("second commit = %+v, want exactly one update of the skill file on devdigest/ci without start_branch", last)
	}

	// The MR was closed: a new one is created.
	lab.setMRState(1, "closed")
	n = lab.count()
	out := open()
	if want := lab.base() + "/acme/web/-/merge_requests/2"; *out.PRURL != want {
		t.Errorf("pr_url after closing the MR = %s, want %s", *out.PRURL, want)
	}
	if got := lab.seq(lab.since(n)); got[len(got)-1] != "POST /merge_requests" || len(lab.commits(t)) != 2 {
		t.Errorf("requests after closing the MR = %v (commits %d), want a new MR and no commit", got, len(lab.commits(t)))
	}
}

func TestGitLabCIExport_AC12_MRCreate409IsReused(t *testing.T) { // AC12, FR12, EC8
	lab := geNewLab(t, geProjectID, gePath)
	env := geBoot(t, lab)
	env.connect(lab, geToken)
	repo := env.addRepo(lab, gePath, "")
	agent, _ := env.agentWithSkill()
	lab.conflictNext()

	out, _ := env.export(agent, geBody(gePath, repo, "open_pr", nil), http.StatusCreated)
	got := lab.seq(lab.requests())
	if tail := got[len(got)-3:]; !slices.Equal(tail, []string{"GET /merge_requests", "POST /merge_requests", "GET /merge_requests"}) {
		t.Errorf("last requests = %v, want lookup, create (409), lookup again", tail)
	}
	if want := lab.base() + "/acme/web/-/merge_requests/1"; *out.PRURL != want {
		t.Errorf("pr_url = %s, want %s (the MR opened meanwhile)", *out.PRURL, want)
	}
	if n := len(lab.commits(t)); n != 1 {
		t.Errorf("commits = %d, want 1 (the 409 does not repeat the sequence)", n)
	}
	if n := env.installRows(agent); n != 1 {
		t.Errorf("installation rows = %d, want 1", n)
	}
}

// ---- AC13, EC9: failures ----

func TestGitLabCIExport_AC13_PublishFailures(t *testing.T) { // AC13, AC44, FR13, EC9
	lab := geNewLab(t, geProjectID, gePath)
	env := geBoot(t, lab)
	env.connect(lab, geToken)
	repo := env.addRepo(lab, gePath, "")
	agent, _ := env.agentWithSkill()
	const (
		denied      = "GitLab refused the write. The connection token needs the api scope and at least the Developer role in acme/web."
		unavailable = "GitLab is unavailable, try again later"
	)

	for _, tc := range []struct {
		name    string
		hook    func(w http.ResponseWriter, r *http.Request) bool
		status  int
		code    string
		msg     string
		attempt int // commit requests: 2 = the sequence was repeated once
		logged  any // the logged status, nil = none
		step    string
	}{
		{"commit 403", geOn(http.MethodPost, "/repository/commits", geStatus(403)), 422, "gitlab_permission_denied", denied, 1, float64(403), "commit"},
		{"commit 404", geOn(http.MethodPost, "/repository/commits", geStatus(404)), 422, "gitlab_permission_denied", denied, 1, float64(404), "commit"},
		{"commit 401", geOn(http.MethodPost, "/repository/commits", geStatus(401)), 422, "gitlab_permission_denied", denied, 1, float64(401), "commit"},
		{"commit 500, repeated once", geOn(http.MethodPost, "/repository/commits", geStatus(500)), 502, "gitlab_unavailable", unavailable, 2, float64(500), "commit"},
		{"commit 429, not repeated", geOn(http.MethodPost, "/repository/commits", geStatus(429)), 502, "gitlab_unavailable", unavailable, 1, float64(429), "commit"},
		{"commit connection reset, repeated once", geOn(http.MethodPost, "/repository/commits", geReset), 502, "gitlab_unavailable", unavailable, 2, nil, "commit"},
		{"project 403", geOn(http.MethodGet, "/api/v4/projects/42", geStatus(403)), 422, "gitlab_permission_denied", denied, 0, float64(403), "project"},
		{"MR create 500, repeated once", geOn(http.MethodPost, "/merge_requests", geStatus(500)), 502, "gitlab_unavailable", unavailable, 1, float64(500), "create_merge_request"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lab.setHook(tc.hook)
			logs := len(gcLogLines(t, env.s, "ci export: GitLab write failed"))
			before := lab.count()
			resp := env.s.post("/agents/"+agent+"/export-ci", geBody(gePath, repo, "open_pr", nil))
			e := ciErr(t, resp)
			if resp.Status != tc.status || e.Code != tc.code || e.Message != tc.msg {
				t.Fatalf("export = %d %s %q, want %d %s %q", resp.Status, e.Code, e.Message, tc.status, tc.code, tc.msg)
			}
			if n := env.installRows(agent); n != 0 {
				t.Errorf("installation rows = %d, want none after a failed publish", n)
			}
			mine := lab.since(before)
			var commits, projects int
			for _, r := range mine {
				switch {
				case r.Method == http.MethodPost && strings.HasSuffix(r.Path, "/repository/commits"):
					commits++
				case r.Method == http.MethodGet && r.Path == "/api/v4/projects/42":
					projects++
				}
			}
			if tc.step == "commit" && commits != tc.attempt {
				t.Errorf("commit requests = %d, want %d", commits, tc.attempt)
			}
			if wantProjects := tc.attempt; tc.step == "commit" && projects != wantProjects {
				t.Errorf("project reads = %d, want %d (the whole sequence is repeated once, writes never alone)", projects, wantProjects)
			}
			lines := gcLogLines(t, env.s, "ci export: GitLab write failed")
			if len(lines) != logs+1 {
				t.Fatalf("log lines %q = %d, want %d (one per failed export)", "ci export: GitLab write failed", len(lines), logs+1)
			}
			line := lines[len(lines)-1]
			if line["repo_id"] != repo || line["host"] != lab.hostKey() || line["step"] != tc.step || line["status"] != tc.logged {
				t.Errorf("log line = %v, want repo_id, host %s, step %s, status %v", line, lab.hostKey(), tc.step, tc.logged)
			}
		})
	}
	lab.setHook(nil)

	// After the failures the same export works: nothing was left half done.
	if out, _ := env.export(agent, geBody(gePath, repo, "open_pr", nil), http.StatusCreated); out.Installation == nil {
		t.Error("export after the failures has no installation")
	}
}

func TestGitLabCIExport_AC13_TimeoutIsRepeatedOnce(t *testing.T) { // AC13, FR13 (a timeout is transient)
	if testing.Short() {
		t.Skip("waits out two 30 s GitLab call timeouts")
	}
	lab := geNewLab(t, geProjectID, gePath)
	env := geBoot(t, lab)
	env.connect(lab, geToken)
	repo := env.addRepo(lab, gePath, "")
	agent, _ := env.agentWithSkill()
	lab.setHook(geOn(http.MethodPost, "/repository/commits", lab.geHang))

	resp := geSlowPost(t, env.s, "/agents/"+agent+"/export-ci", geBody(gePath, repo, "open_pr", nil))
	e := ciErr(t, resp)
	if resp.Status != http.StatusBadGateway || e.Code != "gitlab_unavailable" || e.Message != "GitLab is unavailable, try again later" {
		t.Fatalf("export with a hanging commit = %d %s %q, want 502 gitlab_unavailable", resp.Status, e.Code, e.Message)
	}
	if n := len(lab.commits(t)); n != 2 {
		t.Errorf("commit requests = %d, want 2 (one repeat of the sequence)", n)
	}
	lines := gcLogLines(t, env.s, "ci export: GitLab write failed")
	if len(lines) != 1 || lines[0]["step"] != "commit" || lines[0]["status"] != nil {
		t.Errorf("log lines = %v, want one for step commit without a status", lines)
	}
	if n := env.installRows(agent); n != 0 {
		t.Errorf("installation rows = %d, want 0", n)
	}
}

// geSlowPost is a POST with a client that outlasts the GitLab call timeout (30 s).
func geSlowPost(t *testing.T, s *cpStack, p string, body any) response {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, s.base+p, strings.NewReader(string(raw)))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Timeout: 3 * time.Minute}).Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", p, err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("POST %s: read body: %v", p, err)
	}
	s.mu.Lock()
	s.bodies = append(s.bodies, string(data))
	s.mu.Unlock()
	return response{Status: resp.StatusCode, Header: resp.Header, Body: data}
}

// A GitLab that answers with a redirect gets no token on the second hop and no second request (NFR1).
func TestGitLabCIExport_AC44_RedirectIsNotFollowed(t *testing.T) { // AC44, NFR1
	lab := geNewLab(t, geProjectID, gePath)
	var hops atomic.Int32
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hops.Add(1)
		geMsg(w, http.StatusOK, r.Header.Get("PRIVATE-TOKEN"))
	}))
	t.Cleanup(target.Close)
	env := geBoot(t, lab)
	env.connect(lab, geToken)
	repo := env.addRepo(lab, gePath, "")
	agent, _ := env.agentWithSkill()
	lab.setHook(geOn(http.MethodGet, "/api/v4/projects/42", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Location", target.URL+"/api/v4/projects/42?token="+geToken)
		w.WriteHeader(http.StatusFound)
	}))

	resp := env.s.post("/agents/"+agent+"/export-ci", geBody(gePath, repo, "open_pr", nil))
	if e := ciErr(t, resp); resp.Status != http.StatusBadGateway || e.Code != "gitlab_unavailable" {
		t.Fatalf("export behind a redirect = %d %s %q, want 502 gitlab_unavailable", resp.Status, e.Code, e.Message)
	}
	if n := hops.Load(); n != 0 {
		t.Errorf("the redirect target saw %d requests, want 0", n)
	}
	if n := lab.count(); n != 1 {
		t.Errorf("GitLab requests = %d, want 1 (a redirect is neither followed nor repeated)", n)
	}
}

// ---- AC14, EC10: rename and concurrency ----

func TestGitLabCIExport_AC14_RenamedProjectIsFollowed(t *testing.T) { // AC14, FR11, FR13, EC10
	lab := geNewLab(t, geProjectID, gePath)
	env := geBoot(t, lab)
	env.connect(lab, geToken)
	repo := env.addRepo(lab, gePath, "")
	agent, _ := env.agentWithSkill()

	first, _ := env.export(agent, geBody(gePath, repo, "open_pr", nil), http.StatusCreated)
	lab.rename("acme/web-renamed")
	out, _ := env.export(agent, geBody(gePath, repo, "open_pr", nil), http.StatusCreated)

	var stored struct {
		FullName, Owner, Name, WebURL string
		ProjectID                     int64
	}
	if err := env.s.db.DB.Raw(`SELECT full_name, owner, name, web_url, project_id FROM repos WHERE id = ?`, repo).Scan(&stored).Error; err != nil {
		t.Fatal(err)
	}
	want := struct {
		FullName, Owner, Name, WebURL string
		ProjectID                     int64
	}{"acme/web-renamed", "acme", "web-renamed", lab.base() + "/acme/web-renamed", geProjectID}
	if stored != want {
		t.Errorf("stored repository = %+v, want %+v (path followed, project id kept)", stored, want)
	}
	if out.Installation.Repo != "acme/web-renamed" || out.Installation.ID != first.Installation.ID {
		t.Errorf("installation = repo %s id %s, want acme/web-renamed and the same id %s", out.Installation.Repo, out.Installation.ID, first.Installation.ID)
	}
	if wantURL := lab.base() + "/acme/web-renamed/-/merge_requests/1"; *out.PRURL != wantURL {
		t.Errorf("pr_url = %s, want %s (the new path)", *out.PRURL, wantURL)
	}
	var row struct {
		Repo      string
		ProjectID int64
	}
	if err := env.s.db.DB.Raw(`SELECT repo, project_id FROM ci_installations WHERE agent_id = ?`, agent).Scan(&row).Error; err != nil {
		t.Fatal(err)
	}
	if row.Repo != "acme/web-renamed" || row.ProjectID != geProjectID || env.installRows(agent) != 1 {
		t.Errorf("installation row = %+v (rows %d), want the new repo path, project id %d, one row", row, env.installRows(agent), geProjectID)
	}
}

func TestGitLabCIExport_AC14_ConcurrentExportsGiveOneInstallation(t *testing.T) { // AC14, FR13
	lab := geNewLab(t, geProjectID, gePath)
	env := geBoot(t, lab)
	env.connect(lab, geToken)
	repo := env.addRepo(lab, gePath, "")
	agent, _ := env.agentWithSkill()

	// Both exports read the project before either goes on, so they overlap as much as they can.
	var arrived atomic.Int32
	gate := make(chan struct{})
	lab.setHook(geOn(http.MethodGet, "/api/v4/projects/42", func(w http.ResponseWriter, r *http.Request) {
		if arrived.Add(1) == 2 {
			close(gate)
		}
		select {
		case <-gate:
		case <-time.After(10 * time.Second):
		case <-r.Context().Done():
		}
		lab.serveProject(w)
	}))
	var wg sync.WaitGroup
	answers := make([]response, 2)
	for i := range answers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			answers[i] = <-ciAsyncPost(env.s.stack, "/agents/"+agent+"/export-ci", geBody(gePath, repo, "open_pr", nil))
		}()
	}
	wg.Wait()
	for _, a := range answers {
		env.s.mu.Lock()
		env.s.bodies = append(env.s.bodies, string(a.Body))
		env.s.mu.Unlock()
		// The loser's first commit is refused (the branch exists); its repeat finds the winner's files.
		if a.Status != http.StatusCreated {
			t.Errorf("concurrent export answered %d %s, want 201 for both", a.Status, a.Body)
		}
	}
	if n := env.installRows(agent); n != 1 {
		t.Errorf("installation rows = %d, want 1", n)
	}
	if mrs := lab.mrList(); len(mrs) != 1 {
		t.Errorf("merge requests = %d, want 1", len(mrs))
	}
}

// ---- no token, GitHub unchanged ----

func TestGitLabCIExport_ConnectionWithoutTokenServesFilesOnly(t *testing.T) { // FR3, FR5
	lab := geNewLab(t, geProjectID, gePath)
	env := geBoot(t, lab)
	env.connect(lab, "")
	repo := env.addRepo(lab, gePath, "")
	agent, _ := env.agentWithSkill()

	env.export(agent, geBody(gePath, repo, "files", nil), http.StatusOK)
	e := env.refuse(agent, geBody(gePath, repo, "open_pr", nil), http.StatusInternalServerError, "config_error")
	if e.Message != "GitLab connection "+geConnName+" has no token" {
		t.Errorf("message = %q, want the connection named", e.Message)
	}
	if n := lab.count(); n != 0 {
		t.Errorf("GitLab requests = %d, want 0", n)
	}
	if n := env.installRows(agent); n != 0 {
		t.Errorf("installation rows = %d, want 0", n)
	}
}

func TestGitLabCIExport_GitHubExportIsUnchangedAndSendsGitLabNothing(t *testing.T) { // FR2, NFR8
	lab := geNewLab(t, geProjectID, gePath)
	env := geBoot(t, lab)
	env.connect(lab, geToken)
	env.addRepo(lab, gePath, "")
	env.e.repo(t, ciRepoFull)
	env.e.exec(t, `UPDATE repos SET default_branch = 'main' WHERE full_name = ?`, ciRepoFull)
	agent, _ := env.agentWithSkill()

	files, _ := env.export(agent, ciExportBody(ciRepoFull, "files", nil), http.StatusOK)
	if files.GitLabCI != nil || len(files.Files) != 6 || files.Files[5].Path != ".github/workflows/devdigest-review-ci-reviewer.yml" {
		t.Errorf("GitHub files export = gitlab_ci %v, %d files; want the six GitHub files and no gitlab_ci", files.GitLabCI, len(files.Files))
	}
	out, _ := env.export(agent, ciExportBody(ciRepoFull, "open_pr", nil), http.StatusCreated)
	if out.GitLabCI != nil || out.Installation == nil || out.Installation.TargetType != "gha" || out.Installation.Host != "github.com" ||
		out.Installation.Provider != "github" || out.PRURL == nil || *out.PRURL != "https://github.com/acme/payments-api/pull/41" {
		t.Errorf("GitHub open_pr = %+v pr_url %v, want a gha installation on github.com", out.Installation, out.PRURL)
	}
	wantSeq := []string{
		"GET " + ciRepoAPI + "/git/ref/heads/devdigest/ci",
		"GET " + ciRepoAPI + "/git/ref/heads/main",
		"GET " + ciRepoAPI + "/git/commits/" + ciMainSHA,
		"POST " + ciRepoAPI + "/git/blobs",
		"POST " + ciRepoAPI + "/git/trees",
		"POST " + ciRepoAPI + "/git/commits",
		"POST " + ciRepoAPI + "/git/refs",
		"GET " + ciRepoAPI + "/pulls",
		"POST " + ciRepoAPI + "/pulls",
	}
	if diff := cmp.Diff(wantSeq, ciSeq(env.s.gh.requests())); diff != "" {
		t.Errorf("GitHub request sequence mismatch (-want +got):\n%s", diff)
	}
	if n := lab.count(); n != 0 {
		t.Errorf("GitLab requests during GitHub exports = %v, want none", lab.seq(lab.requests()))
	}
	var row struct {
		Host      string
		ProjectID *int64
	}
	if err := env.s.db.DB.Raw(`SELECT host, project_id FROM ci_installations WHERE agent_id = ?`, agent).Scan(&row).Error; err != nil {
		t.Fatal(err)
	}
	if row.Host != "github.com" || row.ProjectID != nil {
		t.Errorf("GitHub installation row = %+v, want host github.com and no project id", row)
	}
}

// ---- contract goldens ----

// geGolden checks resp against testdata/contracts/<name>.json after the stub's host key is replaced
// by goldenHost, so the answer does not hold the stub's port.
func geGolden(g *goldens, lab *geLab, name string, resp response, status int) {
	g.t.Helper()
	resp.Body = []byte(strings.ReplaceAll(string(resp.Body), lab.hostKey(), goldenHost))
	g.check(name, resp, status)
}

// TestContracts_GitLabCIExport records the answers the UI contract test validates for a gitlab_ci
// export: the files preview (create and keep_existing) and the published export. The installation and
// run lists with a GitLab row are the ingest tests' goldens (ci_gl_installations*, ci_gl_runs).
func TestContracts_GitLabCIExport(t *testing.T) {
	lab := geNewLab(t, geProjectID, gePath)
	env := geBoot(t, lab)
	gold := sbGoldens(t, env.s.stack)
	env.connect(lab, geToken)
	repo := env.addRepo(lab, gePath, "")
	agent, _ := env.agentWithSkill()

	_, resp := env.export(agent, geBody(gePath, repo, "files", nil), http.StatusOK)
	geGolden(gold, lab, "ci_gl_export_files", resp, http.StatusOK)

	env.e.exec(t, `UPDATE repos SET clone_path = ? WHERE id = ?`, geClone(t, map[string]string{".gitlab-ci.yml": "stages: [test]\n"}), repo)
	_, resp = env.export(agent, geBody(gePath, repo, "files", map[string]any{"triggers": []string{"opened"}}), http.StatusOK)
	geGolden(gold, lab, "ci_gl_export_files_keep_existing", resp, http.StatusOK)

	_, resp = env.export(agent, geBody(gePath, repo, "open_pr", nil), http.StatusCreated)
	geGolden(gold, lab, "ci_gl_export_open_pr", resp, http.StatusCreated)
}

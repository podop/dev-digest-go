package main

import (
	"crypto/tls"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/config"
)

// GitLab C component tests of the ingest and the lists (plan S23, spec AC3, AC30-AC35, AC44, AC45,
// EC27-EC32). GitLab is a TLS stub on loopback whose certificate is the extra CA of the server; the
// artifact storage hop is a second TLS stub reached through options.artifactClient. Installations,
// repositories and the run rows the lists need are inserted with SQL; the connections are created
// through the API. Helpers are prefixed gq (the other GitLab component tests share the package).

const (
	gqToken     = "glpat-GqIngestMarker0123456789"
	gqMessage   = "GQ-GITLAB-MESSAGE-MARKER-91c2"
	gqProject   = 7
	gqPath      = "acme/web"
	gqSlug      = "alpha-reviewer"
	gqJobName   = "devdigest-review-" + gqSlug
	gqCreatedAt = "2026-10-20T10:00:00Z"
	gqREST      = "/api/v4/projects/"
)

// ---- GitLab stub ----

// gqReq is one request the stub saw.
type gqReq struct {
	Method, Path, Query, Private, Auth, Cookie string
}

// gqPipe is a pipeline of the project. IID > 0 gives it the merge request ref; Source "" is a merge
// request event; Ref overrides the generated ref.
type gqPipe struct {
	ID     int64
	IID    int
	SHA    string
	Source string
	Ref    string
}

func (p gqPipe) ref() string {
	switch {
	case p.Ref != "":
		return p.Ref
	case p.IID > 0:
		return fmt.Sprintf("refs/merge-requests/%d/head", p.IID)
	}
	return "refs/heads/main"
}

func (p gqPipe) source() string {
	if p.Source == "" {
		return "merge_request_event"
	}
	return p.Source
}

// gqJob is a pipeline job. Archive is the size of the artifacts archive (0 = the job has none).
type gqJob struct {
	ID      int64
	Name    string
	Status  string
	Archive int64
	Expires string
}

// gqLab plays a self-hosted GitLab for the project gqProject: its pipelines, their jobs, the merge
// requests and the artifact files of jobs.
type gqLab struct {
	t    testing.TB
	srv  *httptest.Server
	host string // the connection's host key

	mu       sync.Mutex
	reqs     []gqReq
	pipes    []gqPipe // newest first, as the stub lists them
	jobs     map[int64][]gqJob
	mrSource map[int]int64 // merge request iid -> source_project_id (default: the project)
	files    map[int64]http.HandlerFunc
	hook     func(w http.ResponseWriter, r *http.Request) bool
	connID   string
}

var gqProjectRE = regexp.MustCompile(`^/projects/(\d+)(/.*)?$`)

func gqNewLab(t testing.TB) *gqLab {
	t.Helper()
	l := &gqLab{t: t, jobs: map[int64][]gqJob{}, mrSource: map[int]int64{}, files: map[int64]http.HandlerFunc{}}
	l.srv = httptest.NewUnstartedServer(http.HandlerFunc(l.serve))
	l.srv.TLS = &tls.Config{MinVersion: tls.VersionTLS12}
	l.srv.StartTLS()
	t.Cleanup(l.srv.Close)
	l.host = strings.TrimPrefix(l.srv.URL, "https://")
	return l
}

func gqJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// gqBody answers with a fixed artifact file.
func gqBody(b []byte) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(b)
	}
}

// gqRedirect answers 302 to location and sets a session cookie, which must never travel on.
func gqRedirect(location string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "_gitlab_session", Value: "GQ-SESSION"})
		w.Header().Set("Location", location)
		w.WriteHeader(http.StatusFound)
	}
}

func (l *gqLab) edit(f func()) {
	l.mu.Lock()
	defer l.mu.Unlock()
	f()
}

func (l *gqLab) setHook(f func(w http.ResponseWriter, r *http.Request) bool) {
	l.edit(func() { l.hook = f })
}

// add appends a pipeline (older than the ones added before) with one job.
func (l *gqLab) add(p gqPipe, j gqJob) {
	l.edit(func() {
		l.pipes = append(l.pipes, p)
		l.jobs[p.ID] = append(l.jobs[p.ID], j)
	})
}

// file sets the answer to the download of the result file of a job.
func (l *gqLab) file(job int64, h http.HandlerFunc) { l.edit(func() { l.files[job] = h }) }

func gqSHA(id int64) string { return fmt.Sprintf("%040x", id) }

// gqResult is a devdigest-result.json (version 2) that verifies against the pipeline and the job;
// over replaces keys (a nil value is JSON null).
func gqResult(p gqPipe, j gqJob, slug string, over map[string]any) []byte {
	m := map[string]any{
		"findings_count": 2, "critical": 1, "warning": 1, "suggestion": 0, "cost_usd": 0.0123, "duration_ms": 4321,
		"agent": slug, "version": "2", "pr_number": p.IID, "commit_sha": p.SHA, "repository": gqPath,
		"repository_id": gqProject, "run_id": j.ID, "run_attempt": 1, "model": ciModel,
		"manifest_version": 1, "skills": []string{}, "blockers": 1,
	}
	for k, v := range over {
		m[k] = v
	}
	raw, err := json.Marshal(m)
	if err != nil {
		panic(err)
	}
	return raw
}

// good adds a merge request pipeline with a finished job of slug whose artifact verifies.
func (l *gqLab) good(pipe int64, iid int, job int64, slug, status string, over map[string]any) (gqPipe, gqJob) {
	p := gqPipe{ID: pipe, IID: iid, SHA: gqSHA(pipe)}
	j := gqJob{ID: job, Name: "devdigest-review-" + slug, Status: status, Archive: 900}
	l.add(p, j)
	l.file(job, gqBody(gqResult(p, j, slug, over)))
	return p, j
}

func (l *gqLab) requests() []gqReq {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.reqs)
}

func (l *gqLab) count() int { return len(l.requests()) }

// since lists the requests after the first n.
func (l *gqLab) since(n int) []gqReq {
	all := l.requests()
	if n > len(all) {
		return nil
	}
	return all[n:]
}

// gqPaths lists "METHOD path" of requests, sorted.
func gqPaths(reqs []gqReq) []string {
	out := make([]string, len(reqs))
	for i, r := range reqs {
		out[i] = r.Method + " " + r.Path
	}
	slices.Sort(out)
	return out
}

// gqWith keeps the requests whose path contains sub.
func gqWith(reqs []gqReq, sub string) []gqReq {
	var out []gqReq
	for _, r := range reqs {
		if strings.Contains(r.Path, sub) {
			out = append(out, r)
		}
	}
	return out
}

func (l *gqLab) serve(w http.ResponseWriter, r *http.Request) {
	l.mu.Lock()
	l.reqs = append(l.reqs, gqReq{Method: r.Method, Path: r.URL.Path, Query: r.URL.RawQuery,
		Private: r.Header.Get("PRIVATE-TOKEN"), Auth: r.Header.Get("Authorization"), Cookie: r.Header.Get("Cookie")})
	hook := l.hook
	l.mu.Unlock()

	path := strings.TrimPrefix(r.URL.Path, "/api/v4")
	switch path { // what creating a connection asks
	case "/user":
		gqJSON(w, http.StatusOK, map[string]any{"username": "ada"})
		return
	case "/personal_access_tokens/self":
		gqJSON(w, http.StatusOK, map[string]any{"scopes": []string{"read_api", "read_repository"}, "expires_at": nil})
		return
	case "/metadata":
		gqJSON(w, http.StatusOK, map[string]any{"version": "17.5.1"})
		return
	}
	if hook != nil && hook(w, r) {
		return
	}
	m := gqProjectRE.FindStringSubmatch(path)
	if m == nil || m[1] != strconv.Itoa(gqProject) {
		gqJSON(w, http.StatusNotFound, map[string]any{"message": "404 Project Not Found"})
		return
	}
	sub := m[2]
	switch {
	case sub == "/pipelines":
		l.servePipelines(w, r)
	case regexp.MustCompile(`^/pipelines/\d+/jobs$`).MatchString(sub):
		id, _ := strconv.ParseInt(strings.Split(sub, "/")[2], 10, 64)
		l.serveJobs(w, r, id)
	case regexp.MustCompile(`^/merge_requests/\d+$`).MatchString(sub):
		iid, _ := strconv.Atoi(strings.TrimPrefix(sub, "/merge_requests/"))
		l.serveMR(w, iid)
	case regexp.MustCompile(`^/jobs/\d+/artifacts/.+$`).MatchString(sub):
		id, _ := strconv.ParseInt(strings.Split(sub, "/")[2], 10, 64)
		l.mu.Lock()
		h := l.files[id]
		l.mu.Unlock()
		if h == nil || strings.TrimPrefix(sub, fmt.Sprintf("/jobs/%d/artifacts/", id)) != "devdigest-result.json" {
			gqJSON(w, http.StatusNotFound, map[string]any{"message": "404 Not Found"})
			return
		}
		h(w, r)
	default:
		gqJSON(w, http.StatusNotFound, map[string]any{"message": "404 Not Found"})
	}
}

func (l *gqLab) servePipelines(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	perPage, _ := strconv.Atoi(q.Get("per_page"))
	page, _ := strconv.Atoi(q.Get("page"))
	if perPage < 1 {
		perPage = 20
	}
	if page < 1 {
		page = 1
	}
	l.mu.Lock()
	all := slices.Clone(l.pipes)
	l.mu.Unlock()
	from := min((page-1)*perPage, len(all))
	to := min(from+perPage, len(all))
	out := []any{}
	for _, p := range all[from:to] { // the stub does not filter by source: the ingest must
		out = append(out, map[string]any{"id": p.ID, "project_id": gqProject, "ref": p.ref(), "sha": p.SHA,
			"source": p.source(), "status": "success", "created_at": gqCreatedAt})
	}
	gqJSON(w, http.StatusOK, out)
}

func (l *gqLab) serveJobs(w http.ResponseWriter, r *http.Request, pipeline int64) {
	scopes := r.URL.Query()["scope[]"]
	l.mu.Lock()
	jobs := slices.Clone(l.jobs[pipeline])
	l.mu.Unlock()
	out := []any{}
	for _, j := range jobs {
		if len(scopes) > 0 && !slices.Contains(scopes, j.Status) {
			continue
		}
		item := map[string]any{"id": j.ID, "name": j.Name, "status": j.Status, "created_at": gqCreatedAt,
			"artifacts_file": nil, "artifacts_expire_at": nil}
		if j.Archive > 0 {
			item["artifacts_file"] = map[string]any{"filename": "artifacts.zip", "size": j.Archive}
		}
		if j.Expires != "" {
			item["artifacts_expire_at"] = j.Expires
		}
		out = append(out, item)
	}
	gqJSON(w, http.StatusOK, out)
}

func (l *gqLab) serveMR(w http.ResponseWriter, iid int) {
	l.mu.Lock()
	src, ok := l.mrSource[iid]
	l.mu.Unlock()
	if !ok {
		src = gqProject
	}
	gqJSON(w, http.StatusOK, map[string]any{"iid": iid, "title": "Add feature", "state": "opened", "draft": false,
		"source_branch": "feat", "target_branch": "main", "sha": gqSHA(int64(iid)), "source_project_id": src,
		"target_project_id": gqProject, "created_at": gqCreatedAt, "updated_at": gqCreatedAt,
		"web_url": l.srv.URL + "/" + gqPath + "/-/merge_requests/" + strconv.Itoa(iid), "author": map[string]any{"username": "octo"}})
}

// ---- artifact storage stub ----

// gqStorage plays the object storage a GitLab artifact redirect points at (https, no credentials).
type gqStorage struct {
	srv *httptest.Server

	mu     sync.Mutex
	seen   []gqReq
	bodies map[string][]byte
}

func gqNewStorage(t testing.TB) *gqStorage {
	t.Helper()
	st := &gqStorage{bodies: map[string][]byte{}}
	st.srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		st.mu.Lock()
		st.seen = append(st.seen, gqReq{Method: r.Method, Path: r.URL.Path, Query: r.URL.RawQuery,
			Private: r.Header.Get("PRIVATE-TOKEN"), Auth: r.Header.Get("Authorization"), Cookie: r.Header.Get("Cookie")})
		body, ok := st.bodies[r.URL.Path]
		st.mu.Unlock()
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(body)
	}))
	t.Cleanup(st.srv.Close)
	return st
}

func (st *gqStorage) put(path string, body []byte) {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.bodies[path] = body
}

func (st *gqStorage) requests() []gqReq {
	st.mu.Lock()
	defer st.mu.Unlock()
	return slices.Clone(st.seen)
}

// ---- stack ----

type gqOpts struct {
	labs    int          // GitLab instances (default 1), connected as "Lab 1" ...
	noToken map[int]bool // lab index -> its connection has no token
	github  bool         // a GitHub token and the GitHub double
	storage bool         // the artifact storage stub as options.artifactClient
	runner  bool         // the fake runner bundle (exports)
	now     func() time.Time
}

type gqEnv struct {
	t       *testing.T
	s       *stack
	labs    []*gqLab
	gh      *ciGH
	storage *gqStorage
	ws      string
}

// gqBoot starts the stubs and a server that trusts them and has a connection to each lab.
func gqBoot(t *testing.T, o gqOpts) *gqEnv {
	t.Helper()
	e := &gqEnv{t: t}
	for range max(o.labs, 1) {
		e.labs = append(e.labs, gqNewLab(t))
	}
	caFile := filepath.Join(t.TempDir(), "gq-ca.pem")
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: e.labs[0].srv.Certificate().Raw})
	if err := os.WriteFile(caFile, pemBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SSL_CERT_FILE", caFile) // the git CA bundle of the server needs a system bundle (backend/INSIGHTS)

	sp := spec{now: o.now}
	sp.config = func(c *config.Config) { c.ExtraCAFile = caFile }
	if o.github {
		sp.env = map[string]string{"GITHUB_TOKEN": ciToken}
	}
	if o.runner {
		sp.runner = ciFakeRunner()
	}
	if o.storage {
		e.storage = gqNewStorage(t)
		sp.artifactClient = e.storage.srv.Client()
	}
	e.s = startStack(t, sp)
	if o.github {
		e.gh = newCiGH()
		e.s.gh.setOverride(e.gh.handle)
	}
	e.ws = scalar[string](t, e.s.db.DB, `SELECT id::text FROM workspaces LIMIT 1`)
	for i, l := range e.labs {
		body := map[string]any{"name": fmt.Sprintf("Lab %d", i+1), "base_url": l.srv.URL}
		if !o.noToken[i] {
			body["token"] = gqToken
		}
		resp := e.s.post("/settings/gitlab-connections", body)
		if resp.Status != http.StatusCreated {
			t.Fatalf("POST /settings/gitlab-connections (lab %d) = %d %s, want 201", i+1, resp.Status, resp.Body)
		}
		l.connID = idOf(t, resp)
	}
	return e
}

// repo inserts the GitLab repository path of host/project (a web_url under webURL).
func (e *gqEnv) repo(host string, project int64, path, webURL string) {
	e.t.Helper()
	err := e.s.db.DB.Exec(`INSERT INTO repos (workspace_id, owner, name, full_name, provider, host, project_id, web_url)
		VALUES (?, ?, ?, ?, 'gitlab', ?, ?, ?)`, e.ws, filepath.Dir(path), filepath.Base(path), path, host, project, webURL).Error
	if err != nil {
		e.t.Fatalf("insert repo %s %s: %v", host, path, err)
	}
}

// install inserts a gitlab_ci installation (provider openai: not the agent's) and its repository.
func (e *gqEnv) install(agent string, l *gqLab, project int64, path, slug string, at time.Time) string {
	e.t.Helper()
	if n := count(e.t, e.s.db.DB, `SELECT count(*) FROM repos WHERE host = ? AND project_id = ?`, l.host, project); n == 0 {
		e.repo(l.host, project, path, l.srv.URL+"/"+path)
	}
	return e.installOn(agent, l.host, project, path, slug, at)
}

func (e *gqEnv) installOn(agent, host string, project int64, path, slug string, at time.Time) string {
	e.t.Helper()
	return scalar[string](e.t, e.s.db.DB, `INSERT INTO ci_installations
		(agent_id, repo, target_type, installed_at, slug, manifest_version, provider, host, project_id)
		VALUES (?, ?, 'gitlab_ci', ?, ?, 1, 'openai', ?, ?) RETURNING id::text`, agent, path, at, slug, host, project)
}

var gqBase = time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)

type gqSkip struct {
	GithubRunID int64  `json:"github_run_id"`
	Reason      string `json:"reason"`
}

type gqInstResult struct {
	InstallationID string   `json:"installation_id"`
	Repo           string   `json:"repo"`
	Host           string   `json:"host"`
	State          string   `json:"state"`
	ErrorCode      string   `json:"error_code"`
	Ingested       int      `json:"ingested"`
	Skipped        []gqSkip `json:"skipped"`
}

type gqRefreshResult struct {
	Ingested      int            `json:"ingested"`
	Degraded      bool           `json:"degraded"`
	Installations []gqInstResult `json:"installations"`
}

// refresh runs POST /ci/refresh and expects 200.
func (e *gqEnv) refresh() (gqRefreshResult, response) {
	e.t.Helper()
	resp := e.s.post("/ci/refresh", nil)
	if resp.Status != http.StatusOK {
		e.t.Fatalf("POST /ci/refresh = %d %s, want 200", resp.Status, resp.Body)
	}
	var out gqRefreshResult
	if err := json.Unmarshal(resp.Body, &out); err != nil {
		e.t.Fatalf("refresh body: %v: %s", err, resp.Body)
	}
	return out, resp
}

type gqRunRow struct {
	Host        string
	Source      string
	GithubRunID int64
	RunAttempt  int
	GithubURL   string
	PRNumber    *int
	HeadSHA     string
	Status      string
	Error       *string
	Provider    string
}

// runs lists the stored CI runs with the provider of their agent run.
func (e *gqEnv) runs() []gqRunRow {
	e.t.Helper()
	var rows []gqRunRow
	err := e.s.db.DB.Raw(`SELECT r.host, r.source, r.github_run_id, r.run_attempt, r.github_url, r.pr_number, r.head_sha,
			r.status, a.error, a.provider
		FROM ci_runs r JOIN agent_runs a ON a.id = r.agent_run_id ORDER BY r.github_run_id, r.host`).Scan(&rows).Error
	if err != nil {
		e.t.Fatal(err)
	}
	return rows
}

// ledger lists the dedupe keys of the CI entries of the cost ledger.
func (e *gqEnv) ledger() []string {
	e.t.Helper()
	var keys []string
	if err := e.s.db.DB.Raw(`SELECT dedupe_key FROM cost_ledger WHERE kind = 'ci_run' ORDER BY dedupe_key`).Scan(&keys).Error; err != nil {
		e.t.Fatal(err)
	}
	return keys
}

// gqNoLeak fails the test when a token or a stub message text is in text.
func gqNoLeak(t *testing.T, what, text string, secrets ...string) {
	t.Helper()
	for _, s := range secrets {
		if strings.Contains(text, s) {
			t.Errorf("%s holds %q, want tokens and GitLab texts kept out", what, s)
		}
	}
}

// gqGolden checks a response against testdata/contracts/<name>.json with the lab's address
// replaced by a fixed one.
func gqGolden(g *goldens, name string, resp response, labs ...*gqLab) {
	g.t.Helper()
	body := string(resp.Body)
	for i, l := range labs {
		body = strings.ReplaceAll(body, l.host, fmt.Sprintf("gitlab-%d.lab.test", i+1))
	}
	resp.Body = []byte(body)
	g.check(name, resp, http.StatusOK)
}

// ---- AC31 + AC30 + AC44: merge request pipelines are ingested ----

func TestGitLabCIIngest_AC31_StoresMergeRequestPipelineJobs(t *testing.T) { // AC30, AC31, AC44, EC27
	e := gqBoot(t, gqOpts{}) // no GitHub token: only a gitlab_ci installation
	lab := e.labs[0]
	agent := ciCreateAgent(t, e.s, "Alpha Reviewer", "openrouter")
	inst := e.install(agent, lab, gqProject, gqPath, gqSlug, gqBase)
	g := ciGoldens(t, e.s)

	// Newest first: a job of another agent, a branch pipeline, a canceled job, a failed job and a
	// successful one, the last two with a verified artifact.
	lab.add(gqPipe{ID: 105, IID: 8, SHA: gqSHA(105)}, gqJob{ID: 1005, Name: "devdigest-review-other-agent", Status: "success"})
	lab.add(gqPipe{ID: 104, SHA: gqSHA(104), Source: "push"}, gqJob{ID: 1004, Name: gqJobName, Status: "success", Archive: 900})
	lab.add(gqPipe{ID: 103, IID: 7, SHA: gqSHA(103)}, gqJob{ID: 1003, Name: gqJobName, Status: "canceled"})
	lab.good(102, 6, 1002, gqSlug, "failed", map[string]any{"findings_count": 0, "pr_number": nil})
	lab.good(101, 5, 1001, gqSlug, "success", nil)

	before := lab.count()
	out, resp := e.refresh()
	if out.Ingested != 2 || out.Degraded || len(out.Installations) != 1 {
		t.Fatalf("refresh = %s, want 2 ingested, not degraded, one installation", resp.Body)
	}
	if in := out.Installations[0]; in.InstallationID != inst || in.State != "ok" || in.Ingested != 2 || in.Host != lab.host || len(in.Skipped) != 0 {
		t.Errorf("installation result = %+v, want ok, 2 ingested, host %s, nothing skipped", in, lab.host)
	}
	gqGolden(g, "ci_gl_refresh", resp, lab)

	wantRows := []gqRunRow{
		{Host: lab.host, Source: "gitlab_ci", GithubRunID: 1001, RunAttempt: 1, GithubURL: lab.srv.URL + "/acme/web/-/jobs/1001",
			PRNumber: ciPtr(5), HeadSHA: gqSHA(101), Status: "succeeded", Provider: "openai"},
		{Host: lab.host, Source: "gitlab_ci", GithubRunID: 1002, RunAttempt: 1, GithubURL: lab.srv.URL + "/acme/web/-/jobs/1002",
			PRNumber: ciPtr(6), HeadSHA: gqSHA(102), Status: "no_findings", Provider: "openai"},
	}
	if diff := cmp.Diff(wantRows, e.runs()); diff != "" {
		t.Errorf("stored runs mismatch (-want +got):\n%s", diff)
	}
	wantKeys := []string{"ci:" + lab.host + ":1001:1", "ci:" + lab.host + ":1002:1"}
	if diff := cmp.Diff(wantKeys, e.ledger()); diff != "" {
		t.Errorf("cost ledger keys mismatch (-want +got):\n%s", diff)
	}

	// AC44: one list, the jobs of the four merge request pipelines, the merge request and the result
	// file of the two candidates; the branch pipeline is never asked for its jobs.
	reqs := lab.since(before)
	wantPaths := []string{
		"GET " + gqREST + "7/jobs/1001/artifacts/devdigest-result.json",
		"GET " + gqREST + "7/jobs/1002/artifacts/devdigest-result.json",
		"GET " + gqREST + "7/merge_requests/5",
		"GET " + gqREST + "7/merge_requests/6",
		"GET " + gqREST + "7/pipelines",
		"GET " + gqREST + "7/pipelines/101/jobs",
		"GET " + gqREST + "7/pipelines/102/jobs",
		"GET " + gqREST + "7/pipelines/103/jobs",
		"GET " + gqREST + "7/pipelines/105/jobs",
	}
	if diff := cmp.Diff(wantPaths, gqPaths(reqs)); diff != "" {
		t.Errorf("GitLab requests of the refresh mismatch (-want +got):\n%s", diff)
	}
	for _, r := range reqs {
		if r.Private != gqToken || r.Auth != "" || r.Cookie != "" {
			t.Errorf("request %s %s carried PRIVATE-TOKEN %q Authorization %q Cookie %q, want only the token", r.Method, r.Path, r.Private, r.Auth, r.Cookie)
		}
	}
	list := gqWith(reqs, "/pipelines")[0]
	for _, want := range []string{"source=merge_request_event", "per_page=30", "page=1"} {
		if !strings.Contains(list.Query, want) {
			t.Errorf("pipeline list query = %q, want %s", list.Query, want)
		}
	}
	gqNoLeak(t, "the refresh answer", string(resp.Body), gqToken)
	gqNoLeak(t, "the logs", e.s.logs.String(), gqToken)

	// A second refresh stores nothing and asks neither for merge requests nor for artifacts.
	before = lab.count()
	if again, _ := e.refresh(); again.Ingested != 0 {
		t.Errorf("second refresh ingested %d, want 0", again.Ingested)
	}
	if got := gqWith(lab.since(before), "/merge_requests"); len(got) != 0 {
		t.Errorf("second refresh asked for %d merge request(s), want none for stored jobs", len(got))
	}
	if got := gqWith(lab.since(before), "/artifacts/"); len(got) != 0 {
		t.Errorf("second refresh downloaded %d artifact(s), want none for stored jobs", len(got))
	}

	// EC27: the canceled job is stored once it has succeeded.
	p103 := gqPipe{ID: 103, IID: 7, SHA: gqSHA(103)}
	j103 := gqJob{ID: 1003, Name: gqJobName, Status: "success", Archive: 900}
	lab.edit(func() {
		lab.jobs[103] = []gqJob{j103}
		lab.files[1003] = gqBody(gqResult(p103, j103, gqSlug, nil))
	})
	if third, _ := e.refresh(); third.Ingested != 1 {
		t.Errorf("refresh after the job succeeded ingested %d, want 1", third.Ingested)
	}
	if n := count(t, e.s.db.DB, `SELECT count(*) FROM ci_runs`); n != 3 {
		t.Errorf("ci_runs rows = %d, want 3 (each job once)", n)
	}
}

// ---- AC31: error codes of the refresh, AC30: connections that cannot serve ----

func TestGitLabCIIngest_AC31_RefreshErrorCodes(t *testing.T) { // AC30, AC31, AC44
	e := gqBoot(t, gqOpts{labs: 2, noToken: map[int]bool{1: true}})
	lab, noToken := e.labs[0], e.labs[1]
	agent := ciCreateAgent(t, e.s, "Alpha Reviewer", "openrouter")
	other := ciCreateAgent(t, e.s, "Beta Reviewer", "openrouter")
	third := ciCreateAgent(t, e.s, "Gamma Reviewer", "openrouter")
	iA := e.install(agent, lab, gqProject, gqPath, gqSlug, gqBase)
	iB := e.install(other, noToken, gqProject, gqPath, "beta-reviewer", gqBase.Add(-time.Minute))
	const gone = "gone.example.net"
	e.repo(gone, 9, "acme/web", "https://"+gone+"/acme/web")
	iC := e.installOn(third, gone, 9, "acme/web", "gamma-reviewer", gqBase.Add(-2*time.Minute))
	lab.good(101, 5, 1001, gqSlug, "success", nil)
	noTokenBefore := noToken.count()

	echo := fmt.Sprintf("%s: token %s was rejected", gqMessage, gqToken)
	fail := func(status int) {
		lab.setHook(func(w http.ResponseWriter, r *http.Request) bool {
			if !strings.HasSuffix(r.URL.Path, "/pipelines") {
				return false
			}
			w.Header().Set("Retry-After", "3600")
			gqJSON(w, status, map[string]any{"message": echo})
			return true
		})
	}
	for _, tc := range []struct {
		name   string
		status int
		state  string
		code   string
	}{
		{"429 is rate limited", http.StatusTooManyRequests, "error", "gitlab_rate_limited"},
		{"403 is a permission problem", http.StatusForbidden, "error", "gitlab_permission_denied"},
		{"401 is a permission problem", http.StatusUnauthorized, "error", "gitlab_permission_denied"},
		{"500 is unavailable", http.StatusInternalServerError, "error", "gitlab_unavailable"},
		{"404 is not installed", http.StatusNotFound, "not_installed", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fail(tc.status)
			logBefore := len(e.s.logs.entries(t))
			out, resp := e.refresh()
			if len(out.Installations) != 3 {
				t.Fatalf("refresh = %s, want three installations", resp.Body)
			}
			a := out.Installations[0]
			if a.InstallationID != iA || a.State != tc.state || a.ErrorCode != tc.code || a.Ingested != 0 || a.Host != lab.host {
				t.Errorf("installation A = %+v, want state %s code %q host %s", a, tc.state, tc.code, lab.host)
			}
			if !out.Degraded || out.Ingested != 0 { // installations B and C always fail
				t.Errorf("refresh degraded = %v ingested = %d, want degraded and 0 ingested", out.Degraded, out.Ingested)
			}
			if b := out.Installations[1]; b.InstallationID != iB || b.State != "error" || b.ErrorCode != "gitlab_token_missing" || b.Host != noToken.host {
				t.Errorf("installation B = %+v, want error gitlab_token_missing host %s", b, noToken.host)
			}
			if c := out.Installations[2]; c.InstallationID != iC || c.State != "error" || c.ErrorCode != "repo_host_not_connected" || c.Host != gone {
				t.Errorf("installation C = %+v, want error repo_host_not_connected host %s", c, gone)
			}
			if tc.code != "" {
				var failed []map[string]any
				for _, l := range e.s.logs.entries(t)[logBefore:] {
					if l["msg"] == "ci refresh: installation failed" && l["code"] == tc.code {
						failed = append(failed, l)
					}
				}
				if len(failed) != 1 || failed[0]["status"] != float64(tc.status) {
					t.Errorf("failure log lines for %s = %v, want one with status %d", tc.code, failed, tc.status)
				}
			}
			gqNoLeak(t, "the refresh answer", string(resp.Body), gqToken, gqMessage)
			gqNoLeak(t, "the logs", e.s.logs.String(), gqToken, gqMessage)
		})
	}
	if got := noToken.since(noTokenBefore); len(got) != 0 {
		t.Errorf("the connection without a token received %d request(s), want none: %v", len(got), got)
	}
	if n := count(t, e.s.db.DB, `SELECT count(*) FROM ci_runs`); n != 0 {
		t.Errorf("ci_runs rows = %d, want none after failed refreshes", n)
	}
}

func TestGitLabCIIngest_EC32_GitHubInstallationNeedsTheGitHubToken(t *testing.T) { // AC30, EC32
	e := gqBoot(t, gqOpts{}) // no GitHub token
	lab := e.labs[0]
	agent := ciCreateAgent(t, e.s, "Alpha Reviewer", "openrouter")
	e.install(agent, lab, gqProject, gqPath, gqSlug, gqBase)
	lab.good(101, 5, 1001, gqSlug, "success", nil)

	if out, resp := e.refresh(); out.Ingested != 1 || out.Degraded {
		t.Fatalf("refresh with only a gitlab_ci installation = %s, want 1 ingested", resp.Body)
	}
	ciInstall(t, e.s, agent, "acme/ci-one", "alpha-reviewer", 1, gqBase.Add(-time.Hour))
	before := lab.count()
	resp := e.s.post("/ci/refresh", nil)
	if resp.Status != http.StatusInternalServerError || ciErr(t, resp).Code != "config_error" {
		t.Errorf("refresh with a gha and a gitlab_ci installation and no GitHub token = %d %s, want 500 config_error", resp.Status, resp.Body)
	}
	if got := lab.since(before); len(got) != 0 {
		t.Errorf("the refused refresh sent %d GitLab request(s), want none", len(got))
	}
	if got := e.s.gh.requests(); len(got) != 0 {
		t.Errorf("GitHub requests = %v, want none", ciSeq(got))
	}
}

// ---- AC32: fork merge requests are never inspected ----

func TestGitLabCIIngest_AC32_ForkMergeRequestIsNeverInspected(t *testing.T) { // AC32, EC31
	e := gqBoot(t, gqOpts{})
	lab := e.labs[0]
	agent := ciCreateAgent(t, e.s, "Alpha Reviewer", "openrouter")
	inst := e.install(agent, lab, gqProject, gqPath, gqSlug, gqBase)
	lab.good(502, 10, 5002, gqSlug, "success", nil)
	lab.good(501, 9, 5001, gqSlug, "success", nil)
	lab.edit(func() { lab.mrSource[9] = 99 }) // the source branch lives in another project

	out, resp := e.refresh()
	if out.Ingested != 1 || out.Degraded || len(out.Installations) != 1 || out.Installations[0].InstallationID != inst {
		t.Fatalf("refresh = %s, want the merge request of the project ingested", resp.Body)
	}
	if got := out.Installations[0].Skipped; len(got) != 0 {
		t.Errorf("skipped = %v, want none: a fork run is not in the list", got)
	}
	if got := gqWith(lab.requests(), "/jobs/5001/artifacts/"); len(got) != 0 {
		t.Errorf("the fork's artifact was requested %d time(s), want never", len(got))
	}
	if got := gqWith(lab.requests(), "/jobs/5002/artifacts/"); len(got) != 1 {
		t.Errorf("the project's own artifact was requested %d time(s), want once", len(got))
	}
	rows := e.runs()
	if len(rows) != 1 || rows[0].GithubRunID != 5002 {
		t.Errorf("stored runs = %+v, want only job 5002", rows)
	}
	warns := logLines(t, e.s.logs, "ci refresh: run not inspected")
	if len(warns) != 1 || warns[0]["level"] != "WARN" || warns[0]["reason"] != "source_project_differs" || warns[0]["github_run_id"] != float64(5001) {
		t.Errorf("run not inspected log lines = %v, want one WARN for job 5001 with reason source_project_differs", warns)
	}
}

// ---- AC33: what happens to the artifacts ----

func TestGitLabCIIngest_AC33_ArtifactOutcomes(t *testing.T) { // AC33, EC29, AC44
	e := gqBoot(t, gqOpts{storage: true})
	lab := e.labs[0]
	agent := ciCreateAgent(t, e.s, "Alpha Reviewer", "openrouter")
	inst := e.install(agent, lab, gqProject, gqPath, gqSlug, gqBase)
	g := ciGoldens(t, e.s)

	job := func(pipe int64, iid int, id int64, status string, archive int64, expires string) gqPipe {
		p := gqPipe{ID: pipe, IID: iid, SHA: gqSHA(pipe)}
		lab.add(p, gqJob{ID: id, Name: gqJobName, Status: status, Archive: archive, Expires: expires})
		return p
	}
	var plainHits int
	var plainMu sync.Mutex
	plain := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		plainMu.Lock()
		plainHits++
		plainMu.Unlock()
	}))
	t.Cleanup(plain.Close)

	job(210, 1, 2101, "failed", 0, "") // no artifact: a failed run
	job(209, 2, 2102, "success", 900, "2020-01-01T00:00:00Z")
	job(208, 3, 2103, "success", 2<<20, "")
	job(207, 4, 2104, "success", 900, "")
	lab.file(2104, gqBody([]byte(`{"pad":"`+strings.Repeat("x", 70*1024)+`"}`)))
	job(206, 5, 2105, "success", 900, "") // no result file in the archive: 404
	p2106 := job(205, 6, 2106, "success", 900, "")
	j2106 := gqJob{ID: 2106, Name: gqJobName, Status: "success"}
	e.storage.put("/artifact/ok", gqResult(p2106, j2106, gqSlug, nil))
	lab.file(2106, gqRedirect(e.storage.srv.URL+"/artifact/ok?sig=SIGNED-ONE"))
	job(204, 7, 2107, "success", 900, "")
	lab.file(2107, gqRedirect(plain.URL+"/artifact/plain?sig=SIGNED-PLAIN")) // a redirect to http

	out, resp := e.refresh()
	if len(out.Installations) != 1 {
		t.Fatalf("refresh = %s, want one installation", resp.Body)
	}
	in := out.Installations[0]
	wantSkips := []gqSkip{
		{GithubRunID: 2102, Reason: "artifact_expired"},
		{GithubRunID: 2103, Reason: "artifact_too_large"},
		{GithubRunID: 2104, Reason: "artifact_too_large"},
		{GithubRunID: 2105, Reason: "artifact_invalid"},
	}
	if in.InstallationID != inst || in.State != "error" || in.ErrorCode != "gitlab_unavailable" || in.Ingested != 2 || out.Ingested != 2 || !out.Degraded {
		t.Errorf("installation = %+v (ingested %d), want error gitlab_unavailable with 2 ingested: the failed run and the storage hop", in, out.Ingested)
	}
	if diff := cmp.Diff(wantSkips, in.Skipped); diff != "" {
		t.Errorf("skipped mismatch (-want +got):\n%s", diff)
	}
	gqGolden(g, "ci_gl_refresh_skips", resp, lab)

	rows := e.runs()
	if len(rows) != 2 || rows[0].GithubRunID != 2101 || rows[1].GithubRunID != 2106 {
		t.Fatalf("stored runs = %+v, want the failed job 2101 and job 2106", rows)
	}
	if r := rows[0]; r.Status != "failed" || r.Error == nil || *r.Error != "The CI run produced no result artifact" || r.PRNumber == nil || *r.PRNumber != 1 || r.HeadSHA != gqSHA(210) {
		t.Errorf("failed run without artifact = %+v, want status failed with the runner-crash text, MR !1 and the pipeline sha", r)
	}
	if r := rows[1]; r.Status != "succeeded" || r.HeadSHA != gqSHA(205) {
		t.Errorf("run behind the storage hop = %+v, want succeeded with the pipeline sha", r)
	}
	if diff := cmp.Diff([]string{"ci:" + lab.host + ":2106:1"}, e.ledger()); diff != "" {
		t.Errorf("ledger keys mismatch (-want +got), a failed run without artifact has no entry:\n%s", diff)
	}

	// Which result files were asked for: not the expired archive nor the 2 MB one.
	var downloaded []string
	for _, r := range gqWith(lab.requests(), "/artifacts/") {
		if r.Private != gqToken {
			t.Errorf("artifact request %s carried PRIVATE-TOKEN %q, want the token on the first leg", r.Path, r.Private)
		}
		downloaded = append(downloaded, strings.Split(r.Path, "/")[6])
	}
	if diff := cmp.Diff([]string{"2104", "2105", "2106", "2107"}, downloaded); diff != "" {
		t.Errorf("downloaded jobs mismatch (-want +got):\n%s", diff)
	}
	// The hop to the storage: once, with no credential and no cookie; the plain http target never.
	hops := e.storage.requests()
	if len(hops) != 1 || hops[0].Path != "/artifact/ok" {
		t.Fatalf("storage requests = %+v, want one for /artifact/ok", hops)
	}
	if h := hops[0]; h.Private != "" || h.Auth != "" || h.Cookie != "" {
		t.Errorf("storage request carried PRIVATE-TOKEN %q Authorization %q Cookie %q, want none", h.Private, h.Auth, h.Cookie)
	}
	plainMu.Lock()
	if plainHits != 0 {
		t.Errorf("the http redirect target was contacted %d time(s), want never", plainHits)
	}
	plainMu.Unlock()
	for what, text := range map[string]string{"the refresh answer": string(resp.Body), "the logs": e.s.logs.String()} {
		gqNoLeak(t, what, text, gqToken, "SIGNED-", "sig=", e.storage.srv.URL, "/artifact/ok", "/artifact/plain")
	}
}

// ---- AC34: artifacts that do not verify ----

func TestGitLabCIIngest_AC34_ArtifactsThatDoNotVerify(t *testing.T) { // AC34, EC28
	e := gqBoot(t, gqOpts{})
	lab := e.labs[0]
	agent := ciCreateAgent(t, e.s, "Alpha Reviewer", "openrouter")
	e.install(agent, lab, gqProject, gqPath, gqSlug, gqBase)

	lab.good(306, 6, 3006, gqSlug, "success", map[string]any{"repository": "moved/elsewhere"}) // same ids, another path: stored
	lab.good(305, 5, 3005, gqSlug, "success", nil)
	lab.file(3005, gqBody([]byte("{not json")))
	lab.good(304, 4, 3004, gqSlug, "success", map[string]any{"findings_count": -1})
	lab.good(303, 3, 3003, gqSlug, "success", map[string]any{"commit_sha": gqSHA(9999)})
	lab.good(302, 2, 3002, gqSlug, "success", map[string]any{"run_id": 12345})
	lab.good(301, 1, 3001, gqSlug, "success", map[string]any{"repository_id": 99})

	out, resp := e.refresh()
	wantSkips := []gqSkip{
		{GithubRunID: 3005, Reason: "artifact_invalid"},
		{GithubRunID: 3004, Reason: "artifact_invalid"},
		{GithubRunID: 3003, Reason: "artifact_mismatch"},
		{GithubRunID: 3002, Reason: "artifact_mismatch"},
		{GithubRunID: 3001, Reason: "artifact_mismatch"},
	}
	if len(out.Installations) != 1 || out.Ingested != 1 || out.Degraded {
		t.Fatalf("refresh = %s, want 1 ingested", resp.Body)
	}
	if diff := cmp.Diff(wantSkips, out.Installations[0].Skipped); diff != "" {
		t.Errorf("skipped mismatch (-want +got):\n%s", diff)
	}
	rows := e.runs()
	if len(rows) != 1 || rows[0].GithubRunID != 3006 || rows[0].Source != "gitlab_ci" {
		t.Errorf("stored runs = %+v, want only job 3006 (ids match, the path is not compared)", rows)
	}
}

// ---- EC30 + AC45: one id on two hosts and on GitHub ----

func TestGitLabCIIngest_EC30_SameJobIDOnTwoHostsAndGitHubIsThreeRuns(t *testing.T) { // EC30, AC31, AC45
	e := gqBoot(t, gqOpts{labs: 2, github: true})
	a, b := e.labs[0], e.labs[1]
	agent := ciCreateAgent(t, e.s, "Alpha Reviewer", "openrouter")
	e.install(agent, a, gqProject, gqPath, gqSlug, gqBase)
	e.install(agent, b, gqProject, gqPath, gqSlug, gqBase.Add(-time.Minute))
	a.good(101, 5, 777, gqSlug, "success", nil)
	b.good(101, 5, 777, gqSlug, "success", nil)
	ciInstall(t, e.s, agent, "acme/ci-one", gqSlug, 1, gqBase.Add(-2*time.Minute))
	e.gh.addGoodRun(t, "acme/ci-one", gqSlug, ciRun(gqSlug, "acme/ci-one", 777, gqBase.Add(-time.Hour)), 9777, nil)

	out, resp := e.refresh()
	if out.Ingested != 3 || out.Degraded || len(out.Installations) != 3 {
		t.Fatalf("refresh = %s, want 3 ingested over 3 installations", resp.Body)
	}
	rows := e.runs()
	var got []string
	for _, r := range rows {
		got = append(got, fmt.Sprintf("%s/%s/%d/%d", r.Host, r.Source, r.GithubRunID, r.RunAttempt))
	}
	want := []string{"github.com/gha/777/1", a.host + "/gitlab_ci/777/1", b.host + "/gitlab_ci/777/1"}
	slices.Sort(want)
	slices.Sort(got)
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("stored runs mismatch (-want +got):\n%s", diff)
	}
	wantKeys := []string{"ci:777:1", "ci:" + a.host + ":777:1", "ci:" + b.host + ":777:1"}
	slices.Sort(wantKeys)
	if diff := cmp.Diff(wantKeys, e.ledger()); diff != "" {
		t.Errorf("cost ledger keys mismatch (-want +got):\n%s", diff)
	}
	if n := count(t, e.s.db.DB, `SELECT count(*) FROM cost_ledger WHERE kind = 'ci_run' AND origin = 'ci_ingest'`); n != 3 {
		t.Errorf("ci_ingest ledger rows = %d, want 3", n)
	}
	if again, _ := e.refresh(); again.Ingested != 0 {
		t.Errorf("second refresh ingested %d, want 0: each key is stored once", again.Ingested)
	}
	if n := count(t, e.s.db.DB, `SELECT count(*) FROM ci_runs`); n != 3 {
		t.Errorf("ci_runs rows = %d after the second refresh, want 3", n)
	}
}

func TestGitLabCIIngest_AC45_GitHubIngestIsUnchanged(t *testing.T) { // AC45, NFR8
	e := gqBoot(t, gqOpts{github: true})
	lab := e.labs[0] // a connection exists, no GitLab installation does
	agent := ciCreateAgent(t, e.s, "Alpha Reviewer", "openrouter")
	ciInstall(t, e.s, agent, "acme/ci-one", gqSlug, 1, gqBase)
	e.gh.addGoodRun(t, "acme/ci-one", gqSlug, ciRun(gqSlug, "acme/ci-one", 101, gqBase.Add(-time.Hour)), 9101, nil)
	before := lab.count()

	out, resp := e.refresh()
	if out.Ingested != 1 || out.Degraded || len(out.Installations) != 1 || out.Installations[0].State != "ok" {
		t.Fatalf("refresh = %s, want 1 ingested", resp.Body)
	}
	if strings.Contains(string(resp.Body), `"host"`) {
		t.Errorf("GitHub refresh answer %s has a host key, want today's shape (host only for GitLab hosts)", resp.Body)
	}
	rows := e.runs()
	if len(rows) != 1 || rows[0].Host != "github.com" || rows[0].Source != "gha" || rows[0].GithubRunID != 101 || rows[0].Provider != "openrouter" {
		t.Errorf("stored runs = %+v, want one gha run on github.com", rows)
	}
	if diff := cmp.Diff([]string{"ci:101:1"}, e.ledger()); diff != "" {
		t.Errorf("cost ledger keys mismatch (-want +got), the GitHub key stays ci:<run id>:<attempt>:\n%s", diff)
	}
	if got := lab.since(before); len(got) != 0 {
		t.Errorf("a GitHub-only refresh sent %d GitLab request(s), want none", len(got))
	}
	runs := e.s.get("/ci/runs").Obj(t)["runs"].([]any)
	if len(runs) != 1 {
		t.Fatalf("GET /ci/runs = %v, want one run", runs)
	}
	if r := runs[0].(map[string]any); r["provider"] != "github" || r["host"] != "github.com" || r["pr_url"] != nil || r["source"] != "gha" {
		t.Errorf("GitHub run = %v, want provider github, host github.com, pr_url null", r)
	}
}

// ---- AC44: the request budget ----

func TestGitLabCIIngest_AC44_ThirtyPipelinesStayWithinTheRequestBound(t *testing.T) { // AC44, NFR6
	e := gqBoot(t, gqOpts{})
	lab := e.labs[0]
	agent := ciCreateAgent(t, e.s, "Alpha Reviewer", "openrouter")
	e.install(agent, lab, gqProject, gqPath, gqSlug, gqBase)
	for id := int64(434); id >= 400; id-- { // 35 pipelines: only the newest 30 are listed
		lab.good(id, int(id-300), id*10, gqSlug, "success", nil)
	}
	before := lab.count()
	out, resp := e.refresh()
	if out.Ingested != 30 || out.Degraded {
		t.Fatalf("refresh = %s, want the newest 30 pipelines ingested", resp.Body)
	}
	reqs := lab.since(before)
	counts := map[string]int{}
	for _, r := range reqs {
		if !strings.HasPrefix(r.Path, gqREST+"7/") {
			t.Errorf("request %s is outside the connection's REST root of the project", r.Path)
		}
		switch {
		case strings.HasSuffix(r.Path, "/pipelines"):
			counts["pipelines"]++
		case strings.HasSuffix(r.Path, "/jobs"):
			counts["jobs"]++
		case strings.Contains(r.Path, "/merge_requests/"):
			counts["merge_requests"]++
		case strings.Contains(r.Path, "/artifacts/"):
			counts["artifacts"]++
		}
	}
	if diff := cmp.Diff(map[string]int{"pipelines": 1, "jobs": 30, "merge_requests": 30, "artifacts": 30}, counts); diff != "" {
		t.Errorf("request counts (-want +got):\n%s", diff)
	}
	if len(reqs) > 1+30+30+30 {
		t.Errorf("a refresh of 30 pipelines sent %d requests, want at most %d", len(reqs), 1+30+30+30)
	}
	gqNoLeak(t, "the refresh answer", string(resp.Body), gqToken)
	gqNoLeak(t, "the logs", e.s.logs.String(), gqToken)
}

func TestGitLabCIIngest_AC44_BudgetEndsBeforeTheNextInstallation(t *testing.T) { // AC44, NFR6
	clock := &ciClock{t: time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)}
	e := gqBoot(t, gqOpts{now: clock.Now})
	lab := e.labs[0]
	agent := ciCreateAgent(t, e.s, "Slow Reviewer", "openrouter")
	first := e.install(agent, lab, gqProject, gqPath, gqSlug, gqBase)
	second := e.install(agent, lab, 8, "acme/api", gqSlug, gqBase.Add(-time.Minute))
	lab.good(101, 5, 1001, gqSlug, "success", nil)
	lab.setHook(func(_ http.ResponseWriter, r *http.Request) bool {
		if strings.Contains(r.URL.Path, "/artifacts/") {
			clock.Advance(46 * time.Second) // the last call of the first installation takes 46 s
		}
		return false
	})
	before := lab.count()

	out, resp := e.refresh()
	if len(out.Installations) != 2 || out.Installations[0].InstallationID != first || out.Installations[0].State != "ok" ||
		out.Installations[1].InstallationID != second || out.Installations[1].State != "not_checked" || out.Ingested != 1 || !out.Degraded {
		t.Fatalf("refresh = %s, want the first installation ok and the second not_checked, degraded", resp.Body)
	}
	if strings.Contains(string(resp.Body), "error_code") {
		t.Errorf("refresh body %s has an error_code key, want none for not_checked", resp.Body)
	}
	for _, r := range lab.since(before) {
		if !strings.HasPrefix(r.Path, gqREST+"7/") {
			t.Errorf("request %s belongs to the installation reached after the deadline", r.Path)
		}
	}
	if n := len(lab.since(before)); n != 4 {
		t.Errorf("GitLab requests = %d, want 4 (pipelines, jobs, merge request, artifact of the first installation)", n)
	}
}

// ---- AC35: the lists ----

// insertRun inserts a CI run with its agent run; host and source name where it ran.
func (e *gqEnv) insertRun(agent, inst, host, source string, pr int, id int64, url string, at time.Time) {
	e.t.Helper()
	agentRun := scalar[string](e.t, e.s.db.DB, `INSERT INTO agent_runs
		(workspace_id, agent_id, ran_at, provider, model, duration_ms, status, source, findings_count, blockers, cost_usd)
		SELECT workspace_id, id, ?::timestamptz, 'openai', ?::text, 1000, 'done', 'ci', 2, 0, 0.01 FROM agents WHERE id = ?
		RETURNING id::text`, at, ciModel, agent)
	err := e.s.db.DB.Exec(`INSERT INTO ci_runs
		(ci_installation_id, pr_number, ran_at, status, findings_count, cost_usd, github_url, source, agent_run_id, github_run_id, run_attempt, head_sha, manifest_version, host)
		VALUES (?::uuid, ?, ?::timestamptz, 'succeeded', 2, 0.01, ?, ?, ?::uuid, ?, 1, ?, 1, ?)`,
		inst, pr, at, url, source, agentRun, id, gqSHA(id), host).Error
	if err != nil {
		e.t.Fatalf("insert run %d: %v", id, err)
	}
}

func TestGitLabCIIngest_AC35_ListsCarryProviderHostAndMergeRequestURL(t *testing.T) { // AC35, EC34
	e := gqBoot(t, gqOpts{})
	lab := e.labs[0]
	g := ciGoldens(t, e.s)
	agent := ciCreateAgent(t, e.s, "Alpha Reviewer", "openrouter")
	const down = "gitlab.example.com" // a host without a connection

	// The GitLab project was renamed: the repository row has the current path, the installation the old one.
	e.repo(lab.host, gqProject, "acme/web-renamed", lab.srv.URL+"/acme/web-renamed/")
	e.repo(down, 9, "acme/web", "https://"+down+"/acme/web")
	iGH := ciInstall(t, e.s, agent, "acme/web", gqSlug, 1, gqBase.Add(-time.Hour))
	iGL := e.installOn(agent, lab.host, gqProject, "acme/web", gqSlug, gqBase)
	iDown := e.installOn(agent, down, 9, "acme/web", gqSlug, gqBase.Add(-2*time.Hour))
	e.insertRun(agent, iGH, "github.com", "gha", 7, 1001, "https://github.com/acme/web/actions/runs/1001", gqBase.Add(-30*time.Minute))
	e.insertRun(agent, iGL, lab.host, "gitlab_ci", 3, 2001, lab.srv.URL+"/acme/web-renamed/-/jobs/2001", gqBase.Add(-20*time.Minute))
	e.insertRun(agent, iDown, down, "gitlab_ci", 4, 3001, "https://"+down+"/acme/web/-/jobs/3001", gqBase.Add(-10*time.Minute))

	insts := e.s.get("/agents/" + agent + "/ci-installations")
	gqGolden(g, "ci_gl_installations", insts, lab)
	var parsed struct {
		Installations []struct {
			ID           string  `json:"id"`
			Repo         string  `json:"repo"`
			TargetType   string  `json:"target_type"`
			Provider     string  `json:"provider"`
			Host         string  `json:"host"`
			WorkflowPath *string `json:"workflow_path"`
			LastRun      *struct {
				PRNumber int `json:"pr_number"`
			} `json:"last_run"`
		} `json:"installations"`
	}
	if err := json.Unmarshal(insts.Body, &parsed); err != nil {
		t.Fatal(err)
	}
	type instView struct{ Repo, TargetType, Provider, Host, WorkflowPath string }
	got := map[string]instView{}
	for _, in := range parsed.Installations {
		wp := ""
		if in.WorkflowPath != nil {
			wp = *in.WorkflowPath
		}
		got[in.ID] = instView{in.Repo, in.TargetType, in.Provider, in.Host, wp}
	}
	glPath := ".gitlab/devdigest/review-" + gqSlug + ".yml"
	want := map[string]instView{
		iGH:   {"acme/web", "gha", "github", "github.com", ".github/workflows/devdigest-review-" + gqSlug + ".yml"},
		iGL:   {"acme/web-renamed", "gitlab_ci", "gitlab", lab.host, glPath},
		iDown: {"acme/web", "gitlab_ci", "gitlab", down, glPath},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("agent installations (-want +got):\n%s", diff)
	}

	runs := e.s.get("/ci/runs")
	gqGolden(g, "ci_gl_runs", runs, lab)
	type runView struct {
		Provider, Host, URL string
		PRURL               *string
	}
	gotRuns := map[string]runView{}
	for _, r := range runs.Obj(t)["runs"].([]any) {
		m := r.(map[string]any)
		var prURL *string
		if s, ok := m["pr_url"].(string); ok {
			prURL = &s
		}
		gotRuns[fmt.Sprint(m["github_url"])] = runView{fmt.Sprint(m["provider"]), fmt.Sprint(m["host"]), fmt.Sprint(m["github_url"]), prURL}
	}
	mrURL := lab.srv.URL + "/acme/web-renamed/-/merge_requests/3"
	wantRuns := map[string]runView{
		"https://github.com/acme/web/actions/runs/1001": {"github", "github.com", "https://github.com/acme/web/actions/runs/1001", nil},
		lab.srv.URL + "/acme/web-renamed/-/jobs/2001":   {"gitlab", lab.host, lab.srv.URL + "/acme/web-renamed/-/jobs/2001", &mrURL},
		"https://" + down + "/acme/web/-/jobs/3001":     {"gitlab", down, "https://" + down + "/acme/web/-/jobs/3001", nil},
	}
	if diff := cmp.Diff(wantRuns, gotRuns); diff != "" {
		t.Errorf("GET /ci/runs views (-want +got):\n%s", diff)
	}

	byRepo := e.s.get("/ci/installations")
	gqGolden(g, "ci_gl_installations_by_repo", byRepo, lab)
	type entry struct{ Repo, Provider, Host, TargetType string }
	var gotRepos []entry
	for _, r := range byRepo.Obj(t)["repos"].([]any) {
		m := r.(map[string]any)
		gotRepos = append(gotRepos, entry{fmt.Sprint(m["repo"]), fmt.Sprint(m["provider"]), fmt.Sprint(m["host"]), fmt.Sprint(m["target_type"])})
	}
	wantRepos := []entry{
		{"acme/web", "github", "github.com", "gha"},
		{"acme/web", "gitlab", down, "gitlab_ci"},
		{"acme/web-renamed", "gitlab", lab.host, "gitlab_ci"},
	}
	if diff := cmp.Diff(wantRepos, gotRepos); diff != "" {
		t.Errorf("GET /ci/installations repos (-want +got):\n%s", diff)
	}
	gqNoLeak(t, "the lists", string(insts.Body)+string(runs.Body)+string(byRepo.Body), gqToken)

	// A deleted connection makes the repository not connected: the merge request link goes away.
	if resp := e.s.do(http.MethodDelete, "/settings/gitlab-connections/"+lab.connID, nil); resp.Status >= 300 {
		t.Fatalf("DELETE the connection = %d %s, want success", resp.Status, resp.Body)
	}
	for _, r := range e.s.get("/ci/runs").Obj(t)["runs"].([]any) {
		m := r.(map[string]any)
		if m["pr_url"] != nil {
			t.Errorf("run %v has pr_url %v after its connection was deleted, want null", m["github_url"], m["pr_url"])
		}
		if m["provider"] == "gitlab" && m["github_url"] == lab.srv.URL+"/acme/web-renamed/-/jobs/2001" && m["host"] != lab.host {
			t.Errorf("run host = %v, want the stored host %s still shown", m["host"], lab.host)
		}
	}
}

// ---- AC3: a repository whose connection was deleted ----

func TestGitLabCIIngest_AC3_DeletedConnectionIsReportedAndTheOthersAreIngested(t *testing.T) { // AC3, EC3
	e := gqBoot(t, gqOpts{labs: 2, runner: true})
	gone, live := e.labs[0], e.labs[1]
	agent := ciCreateAgent(t, e.s, "Alpha Reviewer", "openrouter")
	other := ciCreateAgent(t, e.s, "Beta Reviewer", "openrouter")
	iGone := e.install(agent, gone, gqProject, gqPath, gqSlug, gqBase)
	iLive := e.install(other, live, gqProject, gqPath, "beta-reviewer", gqBase.Add(-time.Minute))
	live.good(101, 5, 1001, "beta-reviewer", "success", nil)
	gone.good(101, 5, 1001, gqSlug, "success", nil)
	repoID := scalar[string](t, e.s.db.DB, `SELECT id::text FROM repos WHERE host = ? AND project_id = ?`, gone.host, gqProject)

	if resp := e.s.do(http.MethodDelete, "/settings/gitlab-connections/"+gone.connID, nil); resp.Status >= 300 {
		t.Fatalf("DELETE the connection = %d %s, want success", resp.Status, resp.Body)
	}
	goneBefore, liveBefore := gone.count(), live.count()

	for _, action := range []string{"files", "open_pr"} {
		resp := e.s.post("/agents/"+agent+"/export-ci", map[string]any{"repo": gqPath, "repo_id": repoID, "target": "gitlab_ci", "action": action})
		er := ciErr(t, resp)
		details, _ := er.Details.(map[string]any)
		if resp.Status != http.StatusUnprocessableEntity || er.Code != "repo_host_not_connected" || details["host"] != gone.host || details["repo_id"] != repoID {
			t.Errorf("export %s of a repository without connection = %d %s, want 422 repo_host_not_connected with details.host %s and details.repo_id %s",
				action, resp.Status, resp.Body, gone.host, repoID)
		}
	}
	out, resp := e.refresh()
	if len(out.Installations) != 2 || out.Ingested != 1 || !out.Degraded {
		t.Fatalf("refresh = %s, want two installations, one run ingested, degraded", resp.Body)
	}
	if in := out.Installations[0]; in.InstallationID != iGone || in.State != "error" || in.ErrorCode != "repo_host_not_connected" || in.Host != gone.host {
		t.Errorf("installation of the deleted connection = %+v, want error repo_host_not_connected", in)
	}
	if in := out.Installations[1]; in.InstallationID != iLive || in.State != "ok" || in.Ingested != 1 {
		t.Errorf("installation of the connected host = %+v, want ok with 1 ingested", in)
	}
	if got := gone.since(goneBefore); len(got) != 0 {
		t.Errorf("the deleted connection's host received %d request(s) from the export and the refresh, want none: %v", len(got), got)
	}
	if got := live.since(liveBefore); len(got) == 0 {
		t.Errorf("the connected host received no request, want its installation walked")
	}
}

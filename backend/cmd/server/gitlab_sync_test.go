package main

import (
	"crypto/tls"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"gorm.io/gorm"

	"dev-digest-go/backend/internal/config"
	"dev-digest-go/backend/internal/platform/postgres/pgtest"
)

// Component tests of the GitLab merge request sync (GitLab A, plan S35, spec AC15-AC17, AC20, AC40,
// AC41) through the real server: a real Postgres, the connection API, the pulls routes and a TLS
// stub GitLab on 127.0.0.1. The stub's certificate is the extra CA (DEVDIGEST_EXTRA_CA_FILE, plan
// [W3/S32]); loopback is allowed by the dial guard. The background poll tests are in
// gitlab_poll_test.go and use the same harness. Every package-level name here starts with gs.

const (
	gsToken  = "glpat-gs0123456789abcdefghij"
	gsToken2 = "glpat-gsrotated0123456789ab"
)

// gsEpoch is the fixed time MR timestamps are derived from.
var gsEpoch = time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC)

// ---- stub GitLab ----

// gsReq is one request a stub GitLab received.
type gsReq struct {
	Method, Path, Query, Token string
	Body                       []byte
}

// String is "METHOD path?query", the form tests compare request sequences in.
func (r gsReq) String() string {
	if r.Query == "" {
		return r.Method + " " + r.Path
	}
	return r.Method + " " + r.Path + "?" + r.Query
}

// gsMR is one merge request as the stub lists it.
type gsMR struct {
	iid              int
	title, author    string // author "" = the answer has no author object
	state, sha       string
	draft            bool
	source, target   int64 // project ids; 0 = null
	created, updated time.Time
	merged, closed   *time.Time
	description      *string
}

// gsProject is a project of the stub; webURL is what the stub claims (the server must not use it).
type gsProject struct {
	id            int64
	path          string // path_with_namespace
	defaultBranch string
}

type gsDiffEntry struct {
	oldPath, newPath, diff       string
	deleted, collapsed, tooLarge bool
}

type gsCommitEntry struct {
	id, message, author string
	date                time.Time
}

type gsIssue struct {
	iid                       int
	title, description, state string
}

type gsStat struct{ add, del, files int }

// gsGauge counts the requests in flight over every stub of one test.
type gsGauge struct{ cur, max atomic.Int32 }

func (g *gsGauge) enter() {
	n := g.cur.Add(1)
	for {
		m := g.max.Load()
		if n <= m || g.max.CompareAndSwap(m, n) {
			return
		}
	}
}

func (g *gsGauge) leave() { g.cur.Add(-1) }

// gsLab is a stub GitLab instance: TLS on 127.0.0.1, one host key (127.0.0.1:<port>).
type gsLab struct {
	t    testing.TB
	srv  *httptest.Server
	URL  string // https://127.0.0.1:<port>, the connection's base URL
	host string // the host key of the connections and repositories on it

	gauge *gsGauge

	mu       sync.Mutex
	reqs     []gsReq
	projects map[int64]gsProject
	mrs      map[int64][]gsMR
	diffs    map[string][]gsDiffEntry
	commits  map[string][]gsCommitEntry
	issues   map[int64][]gsIssue
	stats    map[int64]map[int]gsStat
	gqlError bool
	hook     func(w http.ResponseWriter, r *http.Request) bool
}

func gsNewLab(t testing.TB, gauge *gsGauge) *gsLab {
	t.Helper()
	l := &gsLab{
		t: t, gauge: gauge,
		projects: map[int64]gsProject{}, mrs: map[int64][]gsMR{}, diffs: map[string][]gsDiffEntry{},
		commits: map[string][]gsCommitEntry{}, issues: map[int64][]gsIssue{}, stats: map[int64]map[int]gsStat{},
	}
	l.srv = httptest.NewUnstartedServer(http.HandlerFunc(l.serveHTTP))
	l.srv.EnableHTTP2 = false
	l.srv.TLS = &tls.Config{MinVersion: tls.VersionTLS12}
	l.srv.StartTLS()
	t.Cleanup(l.srv.Close)
	l.URL = l.srv.URL
	l.host = strings.TrimPrefix(l.srv.URL, "https://")
	return l
}

// caPEM is the stub's certificate, which the server is told to trust.
func (l *gsLab) caPEM() []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: l.srv.Certificate().Raw})
}

func (l *gsLab) with(fn func()) {
	l.mu.Lock()
	defer l.mu.Unlock()
	fn()
}

func (l *gsLab) setProject(p gsProject)          { l.with(func() { l.projects[p.id] = p }) }
func (l *gsLab) setMRs(project int64, m ...gsMR) { l.with(func() { l.mrs[project] = m }) }
func (l *gsLab) setGraphQLError(on bool)         { l.with(func() { l.gqlError = on }) }
func (l *gsLab) setStats(project int64, s map[int]gsStat) {
	l.with(func() { l.stats[project] = s })
}
func (l *gsLab) setIssues(project int64, i ...gsIssue) { l.with(func() { l.issues[project] = i }) }
func (l *gsLab) setDiffs(project int64, iid int, d ...gsDiffEntry) {
	l.with(func() { l.diffs[fmt.Sprintf("%d/%d", project, iid)] = d })
}
func (l *gsLab) setCommits(project int64, iid int, c ...gsCommitEntry) {
	l.with(func() { l.commits[fmt.Sprintf("%d/%d", project, iid)] = c })
}

// setHook installs a function that may answer a request itself (it reports whether it did). It runs
// after the request was recorded and outside the stub's lock.
func (l *gsLab) setHook(fn func(w http.ResponseWriter, r *http.Request) bool) {
	l.with(func() { l.hook = fn })
}

func (l *gsLab) requests() []gsReq {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.reqs)
}

// requestsMatching are the requests whose "METHOD path" starts with prefix, in arrival order.
func (l *gsLab) requestsMatching(prefix string) []gsReq {
	var out []gsReq
	for _, r := range l.requests() {
		if strings.HasPrefix(r.Method+" "+r.Path, prefix) {
			out = append(out, r)
		}
	}
	return out
}

func gsStrings(reqs []gsReq) []string {
	out := make([]string, len(reqs))
	for i, r := range reqs {
		out[i] = r.String()
	}
	return out
}

func gsJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func gsError(w http.ResponseWriter, status int, message string, headers ...string) {
	for i := 0; i+1 < len(headers); i += 2 {
		w.Header().Set(headers[i], headers[i+1])
	}
	gsJSON(w, status, map[string]string{"message": message})
}

func (l *gsLab) serveHTTP(w http.ResponseWriter, r *http.Request) {
	if l.gauge != nil {
		l.gauge.enter()
		defer l.gauge.leave()
		time.Sleep(3 * time.Millisecond) // widens the window in which two overlapping requests would show
	}
	body, _ := io.ReadAll(r.Body)
	l.mu.Lock()
	l.reqs = append(l.reqs, gsReq{
		Method: r.Method, Path: r.URL.EscapedPath(), Query: r.URL.RawQuery, Token: r.Header.Get("PRIVATE-TOKEN"), Body: body,
	})
	hook := l.hook
	l.mu.Unlock()
	if hook != nil && hook(w, r) {
		return
	}
	p := r.URL.EscapedPath()
	switch {
	case p == "/api/graphql" && r.Method == http.MethodPost:
		l.graphQL(w, body)
	case strings.HasPrefix(p, "/api/v4/projects/") && r.Method == http.MethodGet:
		l.projectRoute(w, r, strings.Split(strings.TrimPrefix(p, "/api/v4/projects/"), "/"))
	default:
		gsError(w, http.StatusNotFound, "404 Not Found")
	}
}

func (l *gsLab) projectRoute(w http.ResponseWriter, r *http.Request, segs []string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	var proj gsProject
	if id, err := strconv.ParseInt(segs[0], 10, 64); err == nil {
		p, ok := l.projects[id]
		if !ok {
			gsError(w, http.StatusNotFound, "404 Project Not Found")
			return
		}
		proj = p
	} else {
		path, err := url.PathUnescape(segs[0])
		if err != nil {
			gsError(w, http.StatusBadRequest, "bad path")
			return
		}
		for _, p := range l.projects {
			if p.path == path {
				proj = p
			}
		}
		if proj.id == 0 {
			gsError(w, http.StatusNotFound, "404 Project Not Found")
			return
		}
	}
	rest := segs[1:]
	switch {
	case len(rest) == 0:
		i := strings.LastIndexByte(proj.path, '/')
		branch := any(proj.defaultBranch)
		if proj.defaultBranch == "" {
			branch = nil
		}
		gsJSON(w, http.StatusOK, map[string]any{
			"id": proj.id, "path": proj.path[i+1:], "path_with_namespace": proj.path, "default_branch": branch,
			"web_url":   "https://evil.example/" + proj.path, // the server must build its own (FR41)
			"namespace": map[string]any{"full_path": proj.path[:i]},
		})
	case rest[0] == "merge_requests" && len(rest) == 1:
		gsJSON(w, http.StatusOK, gsPage(l.mrs[proj.id], r, gsMRJSON))
	case rest[0] == "merge_requests" && len(rest) >= 2:
		iid, _ := strconv.Atoi(rest[1])
		key := fmt.Sprintf("%d/%d", proj.id, iid)
		switch {
		case len(rest) == 2:
			for _, m := range l.mrs[proj.id] {
				if m.iid == iid {
					gsJSON(w, http.StatusOK, gsMRJSON(m))
					return
				}
			}
			gsError(w, http.StatusNotFound, "404 Not Found")
		case rest[2] == "diffs":
			gsJSON(w, http.StatusOK, gsPage(l.diffs[key], r, gsDiffJSON))
		case rest[2] == "commits":
			gsJSON(w, http.StatusOK, gsPage(l.commits[key], r, gsCommitJSON))
		default:
			gsError(w, http.StatusNotFound, "404 Not Found")
		}
	case rest[0] == "issues" && len(rest) == 2:
		iid, _ := strconv.Atoi(rest[1])
		for _, is := range l.issues[proj.id] {
			if is.iid == iid {
				gsJSON(w, http.StatusOK, map[string]any{"iid": is.iid, "title": is.title, "description": is.description, "state": is.state})
				return
			}
		}
		gsError(w, http.StatusNotFound, "404 Not Found")
	default:
		gsError(w, http.StatusNotFound, "404 Not Found")
	}
}

// gsPage returns page `page` of per_page items.
func gsPage[T any](items []T, r *http.Request, toJSON func(T) map[string]any) []map[string]any {
	q := r.URL.Query()
	page, _ := strconv.Atoi(q.Get("page"))
	per, _ := strconv.Atoi(q.Get("per_page"))
	if page < 1 {
		page = 1
	}
	if per < 1 {
		per = 20
	}
	lo := min((page-1)*per, len(items))
	hi := min(lo+per, len(items))
	out := make([]map[string]any, 0, hi-lo)
	for _, it := range items[lo:hi] {
		out = append(out, toJSON(it))
	}
	return out
}

func gsTime(t *time.Time) any {
	if t == nil {
		return nil
	}
	return t.UTC().Format(time.RFC3339)
}

func gsMRJSON(m gsMR) map[string]any {
	out := map[string]any{
		"iid": m.iid, "title": m.title, "state": m.state, "draft": m.draft,
		"source_branch": "feat", "target_branch": "main", "sha": m.sha,
		"created_at": m.created.UTC().Format(time.RFC3339), "updated_at": m.updated.UTC().Format(time.RFC3339),
		"merged_at": gsTime(m.merged), "closed_at": gsTime(m.closed), "description": m.description,
		"web_url": "https://evil.example/mr", "source_project_id": nil, "target_project_id": nil,
	}
	if m.source != 0 {
		out["source_project_id"] = m.source
	}
	if m.target != 0 {
		out["target_project_id"] = m.target
	}
	if m.author != "" {
		out["author"] = map[string]any{"username": m.author}
	}
	return out
}

func gsDiffJSON(d gsDiffEntry) map[string]any {
	return map[string]any{
		"old_path": d.oldPath, "new_path": d.newPath, "diff": d.diff,
		"deleted_file": d.deleted, "collapsed": d.collapsed, "too_large": d.tooLarge,
	}
}

func gsCommitJSON(c gsCommitEntry) map[string]any {
	out := map[string]any{"id": c.id, "message": c.message, "committed_date": c.date.UTC().Format(time.RFC3339)}
	if c.author != "" {
		out["author_name"] = c.author
	}
	return out
}

// graphQL answers the diff-stats query for the iids in its variables.
func (l *gsLab) graphQL(w http.ResponseWriter, body []byte) {
	var req struct {
		Variables struct {
			IDs  []string `json:"ids"`
			IIDs []string `json:"iids"`
		} `json:"variables"`
	}
	if err := json.Unmarshal(body, &req); err != nil || len(req.Variables.IDs) != 1 {
		gsError(w, http.StatusBadRequest, "bad query")
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.gqlError {
		gsJSON(w, http.StatusOK, map[string]any{"errors": []map[string]string{{"message": "GraphQL is disabled"}}})
		return
	}
	gid := req.Variables.IDs[0]
	project, _ := strconv.ParseInt(strings.TrimPrefix(gid, "gid://gitlab/Project/"), 10, 64)
	nodes := []map[string]any{}
	for _, s := range req.Variables.IIDs {
		iid, _ := strconv.Atoi(s)
		if st, ok := l.stats[project][iid]; ok {
			nodes = append(nodes, map[string]any{
				"iid":              s,
				"diffStatsSummary": map[string]any{"additions": st.add, "deletions": st.del, "fileCount": st.files},
			})
		}
	}
	gsJSON(w, http.StatusOK, map[string]any{"data": map[string]any{"projects": map[string]any{"nodes": []map[string]any{
		{"id": gid, "mergeRequests": map[string]any{"nodes": nodes}},
	}}}})
}

// ---- harness ----

// gsOpts configures gsStart.
type gsOpts struct {
	labs        int
	poll        bool // PR_POLL_ENABLED, the poller's clock is the harness clock
	githubToken bool
	git         bool // spec.gitBinary is a wrapper that records every git call (gsHarness.gitLog)
	spec        func(*spec)
}

// gsHarness is a running server with stub GitLab instances, one workspace and no repository.
type gsHarness struct {
	t      *testing.T
	s      *stack
	db     *gorm.DB
	labs   []*gsLab
	gauge  *gsGauge
	clock  *pollClock
	base   time.Time // the clock when the harness started
	ws     string
	gitLog string
	nRepo  int
	nPR    int
}

func gsStart(t *testing.T, o gsOpts) *gsHarness {
	t.Helper()
	db := pgtest.Migrated(t)
	h := &gsHarness{t: t, db: db.DB, gauge: &gsGauge{}, clock: newPollClock(), ws: lbID(idWorkspace, 1)}
	h.base = h.clock.Now()
	for range o.labs {
		h.labs = append(h.labs, gsNewLab(t, h.gauge))
	}

	// The stubs' certificates are the extra CA of the server; git needs a system bundle to add them to,
	// and the host may have none, so the same file is the system bundle of this test.
	var bundle []byte
	for _, l := range h.labs {
		bundle = append(bundle, l.caPEM()...)
	}
	caFile := filepath.Join(t.TempDir(), "gs-ca.pem")
	if err := os.WriteFile(caFile, bundle, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SSL_CERT_FILE", caFile)

	f := &lbFixture{t: t, db: db.DB}
	f.exec(`INSERT INTO workspaces (id, name) VALUES (?, 'default')`, h.ws)
	f.exec(`INSERT INTO users (id, email, name) VALUES (?, 'you@local', 'You')`, lbID(idUser, 1))

	env := map[string]string{}
	if o.githubToken {
		env["GITHUB_TOKEN"] = pollToken
	}
	sp := spec{
		db: db, noSeed: true, env: env,
		config: func(c *config.Config) {
			c.ExtraCAFile = caFile
			c.PRPollEnabled = o.poll
		},
	}
	if o.poll {
		sp.now, sp.pollTick = h.clock.Now, pollTick
	}
	if o.git {
		sp.gitBinary, h.gitLog = gitWrapper(t, false)
	}
	if o.spec != nil {
		o.spec(&sp)
	}
	h.s = startStack(t, sp)
	return h
}

// connect creates a connection for the stub through the API and returns its id; token "" creates one
// without a token.
func (h *gsHarness) connect(l *gsLab, name, token string) string {
	h.t.Helper()
	body := map[string]any{"name": name, "base_url": l.URL}
	if token != "" {
		body["token"] = token
	}
	resp := h.s.post("/settings/gitlab-connections", body)
	if resp.Status != http.StatusCreated && resp.Status != http.StatusOK {
		h.t.Fatalf("POST /settings/gitlab-connections = %d %s, want 201", resp.Status, resp.Body)
	}
	id, _ := resp.Obj(h.t)["id"].(string)
	if id == "" {
		h.t.Fatalf("POST /settings/gitlab-connections answered no id: %s", resp.Body)
	}
	return id
}

// putConnection sends PUT /settings/gitlab-connections/:id with the given body.
func (h *gsHarness) putConnection(id string, body map[string]any) {
	h.t.Helper()
	resp := h.s.do(http.MethodPut, "/settings/gitlab-connections/"+id, body)
	if resp.Status != http.StatusOK {
		h.t.Fatalf("PUT /settings/gitlab-connections/%s %v = %d %s, want 200", id, body, resp.Status, resp.Body)
	}
}

func (h *gsHarness) setToken(id, token string) {
	h.t.Helper()
	h.putConnection(id, map[string]any{"token": token})
}
func (h *gsHarness) removeToken(id string) {
	h.t.Helper()
	h.putConnection(id, map[string]any{"token": nil})
}

// setInterval writes the workspace's Settings polling_interval_min through the API.
func (h *gsHarness) setInterval(minutes int) {
	h.t.Helper()
	resp := h.s.do(http.MethodPut, "/settings", map[string]any{"polling_interval_min": minutes})
	if resp.Status != http.StatusOK {
		h.t.Fatalf("PUT /settings polling_interval_min=%d = %d %s, want 200", minutes, resp.Status, resp.Body)
	}
}

// repo inserts a GitLab repository (as a stored add would have) of host key `host` whose web address
// is base; fullPath is "group/sub/name". It returns the repository id.
func (h *gsHarness) repo(tx *gorm.DB, host, base string, project int64, fullPath string) string {
	h.t.Helper()
	h.nRepo++
	id := lbID(idRepo, h.nRepo)
	i := strings.LastIndexByte(fullPath, '/')
	err := tx.Exec(`INSERT INTO repos (id, workspace_id, owner, name, full_name, provider, host, project_id, web_url)
		VALUES (?, ?, ?, ?, ?, 'gitlab', ?, ?, ?)`,
		id, h.ws, fullPath[:i], fullPath[i+1:], fullPath, host, project, base+"/"+fullPath).Error
	if err != nil {
		h.t.Fatalf("insert GitLab repository %s: %v", fullPath, err)
	}
	return id
}

// addRepo is repo for a stub, in its own transaction.
func (h *gsHarness) addRepo(l *gsLab, project int64, fullPath string) string {
	h.t.Helper()
	return h.repo(h.db, l.host, l.URL, project, fullPath)
}

// storedPR inserts a pull request row, as an earlier sync would have left it.
func (h *gsHarness) storedPR(tx *gorm.DB, repoID string, number int, title, status string) {
	h.t.Helper()
	err := tx.Exec(`INSERT INTO pull_requests (workspace_id, repo_id, number, title, author, branch, base, head_sha,
			additions, deletions, files_count, status, opened_at, updated_at, draft, head_is_fork)
		VALUES (?, ?, ?, ?, 'stored', 'feat', 'main', ?, 3, 1, 2, ?, ?, ?, false, false)`,
		h.ws, repoID, number, title, fmt.Sprintf("stored%d", number), status, gsEpoch, gsEpoch).Error
	if err != nil {
		h.t.Fatalf("insert stored PR #%d: %v", number, err)
	}
}

// gsRepoRow is the identity columns of a repository.
type gsRepoRow struct {
	FullName      string `gorm:"column:full_name"`
	Owner         string `gorm:"column:owner"`
	Name          string `gorm:"column:name"`
	WebURL        string `gorm:"column:web_url"`
	DefaultBranch string `gorm:"column:default_branch"`
	ProjectID     int64  `gorm:"column:project_id"`
}

func (h *gsHarness) repoRow(id string) gsRepoRow {
	h.t.Helper()
	var r gsRepoRow
	if err := h.db.Raw(`SELECT full_name, owner, name, web_url, default_branch, project_id FROM repos WHERE id = ?`, id).Scan(&r).Error; err != nil {
		h.t.Fatalf("read repository %s: %v", id, err)
	}
	return r
}

// gsPR is a stored pull request.
type gsPR struct {
	Number     int        `gorm:"column:number"`
	Title      string     `gorm:"column:title"`
	Author     string     `gorm:"column:author"`
	Branch     string     `gorm:"column:branch"`
	Base       string     `gorm:"column:base"`
	HeadSHA    string     `gorm:"column:head_sha"`
	Status     string     `gorm:"column:status"`
	Draft      bool       `gorm:"column:draft"`
	HeadIsFork *bool      `gorm:"column:head_is_fork"`
	Additions  int        `gorm:"column:additions"`
	Deletions  int        `gorm:"column:deletions"`
	FilesCount int        `gorm:"column:files_count"`
	OpenedAt   *time.Time `gorm:"column:opened_at"`
	UpdatedAt  *time.Time `gorm:"column:updated_at"`
	MergedAt   *time.Time `gorm:"column:merged_at"`
	ClosedAt   *time.Time `gorm:"column:closed_at"`
}

func (h *gsHarness) prs(repoID string) []gsPR {
	h.t.Helper()
	var out []gsPR
	err := h.db.Raw(`SELECT number, title, author, branch, base, head_sha, status, draft, head_is_fork, additions, deletions,
			files_count, opened_at, updated_at, merged_at, closed_at
		FROM pull_requests WHERE repo_id = ? ORDER BY number`, repoID).Scan(&out).Error
	if err != nil {
		h.t.Fatalf("read pull requests of %s: %v", repoID, err)
	}
	return out
}

func (h *gsHarness) prByNumber(repoID string, number int) (gsPR, bool) {
	h.t.Helper()
	for _, p := range h.prs(repoID) {
		if p.Number == number {
			return p, true
		}
	}
	return gsPR{}, false
}

// errorOf returns code and message of an error envelope.
func gsErrorOf(t testing.TB, r response) (code, message string) {
	t.Helper()
	e, _ := r.Obj(t)["error"].(map[string]any)
	code, _ = e["code"].(string)
	message, _ = e["message"].(string)
	return code, message
}

// gsSameTime reports whether two optional times are the same instant.
func gsSameTime(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.Equal(*b)
}

func gsPtr[T any](v T) *T { return &v }

// gsHold answers the first request that passes the filter only after release is closed; started is
// closed when it arrives. Other requests fall through to the stub.
func gsHold(l *gsLab, match func(r *http.Request) bool) (started <-chan struct{}, release func()) {
	startedCh := make(chan struct{})
	releaseCh := make(chan struct{})
	var once, relOnce sync.Once
	l.setHook(func(w http.ResponseWriter, r *http.Request) bool {
		if !match(r) {
			return false
		}
		held := false
		once.Do(func() { held = true })
		if !held {
			return false
		}
		close(startedCh)
		select {
		case <-releaseCh:
		case <-r.Context().Done():
		}
		return false
	})
	rel := func() { relOnce.Do(func() { close(releaseCh) }) }
	l.t.Cleanup(rel)
	return startedCh, rel
}

// ---- AC15 ----

// AC15 (FR21, FR22, EC17, NFR4): the list sync reads the project and then one list page with the
// documented query, maps every state, skips the MR without a head sha and a second sync updates what changed.
func TestGsSync_AC15_ListMapsEveryStateWithOneProjectAndOneListRequest(t *testing.T) {
	h := gsStart(t, gsOpts{labs: 1})
	lab := h.labs[0]
	h.connect(lab, "Cloud", gsToken)
	lab.setProject(gsProject{id: 42, path: "acme/platform/payments", defaultBranch: "main"})
	at := func(day int) time.Time { return gsEpoch.Add(time.Duration(day) * 24 * time.Hour) }
	merged, closed := at(9), at(8)
	lab.setMRs(42,
		gsMR{iid: 1, title: "Opened", author: "ada", state: "opened", sha: "sha1", source: 42, target: 42, created: at(1), updated: at(10)},
		gsMR{iid: 2, title: "Draft", author: "bob", state: "opened", sha: "sha2", draft: true, source: 42, target: 42, created: at(1), updated: at(9)},
		gsMR{iid: 3, title: "From a fork", author: "eve", state: "opened", sha: "sha3", source: 77, target: 42, created: at(1), updated: at(8)},
		gsMR{iid: 4, title: "No sha", author: "ada", state: "opened", sha: "", source: 42, target: 42, created: at(1), updated: at(7)},
		gsMR{iid: 5, title: "Merged", author: "ada", state: "merged", sha: "sha5", source: 42, target: 42, created: at(1), updated: at(6), merged: &merged},
		gsMR{iid: 6, title: "Closed", author: "ada", state: "closed", sha: "sha6", source: 42, target: 42, created: at(1), updated: at(5), closed: &closed},
		gsMR{iid: 7, title: "Locked", author: "ada", state: "locked", sha: "sha7", source: 42, target: 42, created: at(1), updated: at(4)},
		gsMR{iid: 8, title: "Nobody", author: "", state: "opened", sha: "sha8", source: 42, target: 42, created: at(1), updated: at(3)},
	)
	repo := h.addRepo(lab, 42, "acme/platform/payments")

	resp := h.s.get("/repos/" + repo + "/pulls")
	if resp.Status != http.StatusOK {
		t.Fatalf("GET /repos/%s/pulls = %d %s, want 200", repo, resp.Status, resp.Body)
	}
	var listed []int
	for _, it := range resp.JSON(t).([]any) {
		listed = append(listed, int(it.(map[string]any)["number"].(float64)))
	}
	slices.Sort(listed)
	if diff := cmp.Diff([]int{1, 2, 3, 5, 6, 7, 8}, listed); diff != "" {
		t.Errorf("numbers in GET /repos/:id/pulls (the sha-less MR 4 must be absent) mismatch (-want +got):\n%s", diff)
	}

	want := []gsPR{
		{Number: 1, Title: "Opened", Author: "ada", Branch: "feat", Base: "main", HeadSHA: "sha1", Status: "open", HeadIsFork: gsPtr(false), OpenedAt: gsPtr(at(1)), UpdatedAt: gsPtr(at(10))},
		{Number: 2, Title: "Draft", Author: "bob", Branch: "feat", Base: "main", HeadSHA: "sha2", Status: "open", Draft: true, HeadIsFork: gsPtr(false), OpenedAt: gsPtr(at(1)), UpdatedAt: gsPtr(at(9))},
		{Number: 3, Title: "From a fork", Author: "eve", Branch: "feat", Base: "main", HeadSHA: "sha3", Status: "open", HeadIsFork: gsPtr(true), OpenedAt: gsPtr(at(1)), UpdatedAt: gsPtr(at(8))},
		{Number: 5, Title: "Merged", Author: "ada", Branch: "feat", Base: "main", HeadSHA: "sha5", Status: "merged", HeadIsFork: gsPtr(false), OpenedAt: gsPtr(at(1)), UpdatedAt: gsPtr(at(6)), MergedAt: &merged},
		{Number: 6, Title: "Closed", Author: "ada", Branch: "feat", Base: "main", HeadSHA: "sha6", Status: "closed", HeadIsFork: gsPtr(false), OpenedAt: gsPtr(at(1)), UpdatedAt: gsPtr(at(5)), ClosedAt: &closed},
		{Number: 7, Title: "Locked", Author: "ada", Branch: "feat", Base: "main", HeadSHA: "sha7", Status: "closed", HeadIsFork: gsPtr(false), OpenedAt: gsPtr(at(1)), UpdatedAt: gsPtr(at(4))},
		{Number: 8, Title: "Nobody", Author: "unknown", Branch: "feat", Base: "main", HeadSHA: "sha8", Status: "open", HeadIsFork: gsPtr(false), OpenedAt: gsPtr(at(1)), UpdatedAt: gsPtr(at(3))},
	}
	gsCheckPRs(t, "after the first sync", want, h.prs(repo))

	reqs := lab.requests()
	var graphql []gsReq
	var rest []string
	for _, r := range reqs {
		if r.Path == "/api/graphql" {
			graphql = append(graphql, r)
			continue
		}
		rest = append(rest, r.String())
	}
	wantRest := []string{
		"GET /api/v4/projects/42",
		"GET /api/v4/projects/42/merge_requests?order_by=updated_at&page=1&per_page=50&sort=desc&state=all",
	}
	if diff := cmp.Diff(wantRest, rest); diff != "" {
		t.Errorf("GitLab requests of the first sync (project first, then one list request) mismatch (-want +got):\n%s", diff)
	}
	if len(graphql) > 1 {
		t.Errorf("GraphQL requests of the first sync = %d, want at most 1", len(graphql))
	}
	for _, r := range reqs {
		if r.Token != gsToken {
			t.Errorf("%s carried the token %q, want the connection's", r, r.Token)
		}
	}

	// A second sync with changed values: title, head sha, status, updated_at, merged_at, closed_at and draft;
	// MR 4 now has a head sha and is stored (EC17).
	newMerged, newClosed := at(12), at(11)
	lab.setMRs(42,
		gsMR{iid: 1, title: "Opened, now merged", author: "ada", state: "merged", sha: "sha1b", source: 42, target: 42, created: at(1), updated: at(13), merged: &newMerged},
		gsMR{iid: 2, title: "Draft, now ready", author: "bob", state: "opened", sha: "sha2b", draft: false, source: 42, target: 42, created: at(1), updated: at(12)},
		gsMR{iid: 3, title: "From a fork", author: "eve", state: "closed", sha: "sha3", source: 77, target: 42, created: at(1), updated: at(11), closed: &newClosed},
		gsMR{iid: 4, title: "Now with a sha", author: "ada", state: "opened", sha: "sha4", draft: true, source: 42, target: 42, created: at(1), updated: at(10)},
	)
	if resp := h.s.get("/repos/" + repo + "/pulls"); resp.Status != http.StatusOK {
		t.Fatalf("second GET /repos/%s/pulls = %d %s, want 200", repo, resp.Status, resp.Body)
	}
	want[0] = gsPR{Number: 1, Title: "Opened, now merged", Author: "ada", Branch: "feat", Base: "main", HeadSHA: "sha1b", Status: "merged", HeadIsFork: gsPtr(false), OpenedAt: gsPtr(at(1)), UpdatedAt: gsPtr(at(13)), MergedAt: &newMerged}
	want[1] = gsPR{Number: 2, Title: "Draft, now ready", Author: "bob", Branch: "feat", Base: "main", HeadSHA: "sha2b", Status: "open", HeadIsFork: gsPtr(false), OpenedAt: gsPtr(at(1)), UpdatedAt: gsPtr(at(12))}
	want[2] = gsPR{Number: 3, Title: "From a fork", Author: "eve", Branch: "feat", Base: "main", HeadSHA: "sha3", Status: "closed", HeadIsFork: gsPtr(true), OpenedAt: gsPtr(at(1)), UpdatedAt: gsPtr(at(11)), ClosedAt: &newClosed}
	four := gsPR{Number: 4, Title: "Now with a sha", Author: "ada", Branch: "feat", Base: "main", HeadSHA: "sha4", Status: "open", Draft: true, HeadIsFork: gsPtr(false), OpenedAt: gsPtr(at(1)), UpdatedAt: gsPtr(at(10))}
	want = append(want[:3], append([]gsPR{four}, want[3:]...)...)
	gsCheckPRs(t, "after the second sync", want, h.prs(repo))
}

// gsCheckPRs compares stored pull requests; times by instant, stats are not compared (the stub has none).
func gsCheckPRs(t *testing.T, when string, want, got []gsPR) {
	t.Helper()
	if len(want) != len(got) {
		t.Errorf("stored pull requests %s = %d, want %d: %+v", when, len(got), len(want), got)
		return
	}
	for i := range want {
		w, g := want[i], got[i]
		if !gsSameTime(w.OpenedAt, g.OpenedAt) || !gsSameTime(w.UpdatedAt, g.UpdatedAt) ||
			!gsSameTime(w.MergedAt, g.MergedAt) || !gsSameTime(w.ClosedAt, g.ClosedAt) {
			t.Errorf("PR #%d %s: times opened %v updated %v merged %v closed %v, want %v %v %v %v", w.Number, when,
				g.OpenedAt, g.UpdatedAt, g.MergedAt, g.ClosedAt, w.OpenedAt, w.UpdatedAt, w.MergedAt, w.ClosedAt)
		}
		w.OpenedAt, w.UpdatedAt, w.MergedAt, w.ClosedAt = nil, nil, nil, nil
		g.OpenedAt, g.UpdatedAt, g.MergedAt, g.ClosedAt = nil, nil, nil, nil
		if diff := cmp.Diff(w, g); diff != "" {
			t.Errorf("PR #%d %s mismatch (-want +got):\n%s", w.Number, when, diff)
		}
	}
}

// ---- AC16 ----

// AC16 (FR23, EC15): without a token the list serves the stored rows and a manual poll is a 500
// config_error; a GitLab failure after the retries is a 502 external_service and no list request follows a failed project read.
func TestGsSync_AC16_NoTokenServesStoredRowsAndFailuresAreReported(t *testing.T) {
	h := gsStart(t, gsOpts{labs: 1})
	lab := h.labs[0]
	conn := h.connect(lab, "Corp", "")
	lab.setProject(gsProject{id: 42, path: "acme/payments", defaultBranch: "main"})
	repo := h.addRepo(lab, 42, "acme/payments")
	h.storedPR(h.db, repo, 5, "Stored title", "open")

	resp := h.s.get("/repos/" + repo + "/pulls")
	if resp.Status != http.StatusOK {
		t.Fatalf("GET /repos/%s/pulls without a token = %d %s, want 200", repo, resp.Status, resp.Body)
	}
	items := resp.JSON(t).([]any)
	if len(items) != 1 || items[0].(map[string]any)["title"] != "Stored title" {
		t.Errorf("GET /repos/:id/pulls without a token = %s, want the one stored row", resp.Body)
	}
	resp = h.s.post("/repos/"+repo+"/poll", nil)
	if code, msg := gsErrorOf(t, resp); resp.Status != http.StatusInternalServerError || code != "config_error" || msg != "GitLab connection Corp has no token" {
		t.Errorf("POST /repos/:id/poll without a token = %d %s, want 500 config_error \"GitLab connection Corp has no token\"", resp.Status, resp.Body)
	}
	if reqs := lab.requests(); len(reqs) != 0 {
		t.Errorf("GitLab requests without a token = %v, want none", gsStrings(reqs))
	}

	// With a token and a failing instance: retried, then reported.
	h.setToken(conn, gsToken)
	lab.setHook(func(w http.ResponseWriter, r *http.Request) bool {
		gsError(w, http.StatusInternalServerError, "boom")
		return true
	})
	resp = h.s.post("/repos/"+repo+"/poll", nil)
	if code, msg := gsErrorOf(t, resp); resp.Status != http.StatusBadGateway || code != "external_service" || msg != "GitLab request failed" {
		t.Errorf("POST /repos/:id/poll with a failing GitLab = %d %s, want 502 external_service \"GitLab request failed\"", resp.Status, resp.Body)
	}
	if n := len(lab.requestsMatching("GET /api/v4/projects/42")); n < 2 {
		t.Errorf("project requests of a failing poll = %d, want the request retried (at least 2)", n)
	}
	if n := len(lab.requestsMatching("GET /api/v4/projects/42/merge_requests")); n != 0 {
		t.Errorf("list requests after a failed project read = %d, want none", n)
	}
	resp = h.s.get("/repos/" + repo + "/pulls")
	if resp.Status != http.StatusOK || len(resp.JSON(t).([]any)) != 1 {
		t.Errorf("GET /repos/:id/pulls with a failing GitLab = %d %s, want 200 with the stored row", resp.Status, resp.Body)
	}
}

// ---- AC17 ----

// gsDetailFixture is the stub MR of AC17: 229 files over three diff pages and 120 commits over two.
// It returns the expected sums of the files.
func gsDetailFixture(lab *gsLab, project int64, iid int) (adds, dels, files int) {
	const filler = 224
	special := []gsDiffEntry{
		{oldPath: "big.bin", newPath: "big.bin", tooLarge: true},
		{oldPath: "collapsed.txt", newPath: "collapsed.txt", collapsed: true},
		{oldPath: "gone.txt", newPath: "gone.txt", deleted: true, diff: "@@ -1,2 +0,0 @@\n-a\n-b\n"},
		{oldPath: "old/name.go", newPath: "new/name.go", diff: "@@ -1,2 +1,2 @@\n-old\n+new\n keep\n"},
		{oldPath: "dup.go", newPath: "dup.go", diff: "@@ -1 +1 @@\n-a\n+b\n"},
		{oldPath: "dup.go", newPath: "dup.go", diff: "@@ -9 +9,3 @@\n z\n+q\n+r\n"},
	}
	entries := slices.Clone(special)
	for i := range filler {
		p := fmt.Sprintf("filler/f%03d.txt", i)
		entries = append(entries, gsDiffEntry{oldPath: p, newPath: p, diff: "@@ -0,0 +1 @@\n+x\n"})
	}
	lab.setDiffs(project, iid, entries...)
	var commits []gsCommitEntry
	for i := range 120 { // newest first, as GitLab lists them
		c := gsCommitEntry{id: fmt.Sprintf("c%03d", i), message: fmt.Sprintf("commit %d", i), author: "dev", date: gsEpoch.Add(time.Duration(120-i) * time.Minute)}
		if i == 0 {
			c.author = "" // no author name: "unknown"
		}
		commits = append(commits, c)
	}
	lab.setCommits(project, iid, commits...)
	// special sums: gone 0/2, rename 1/1, dup 3/1 (merged), fillers 1/0 each.
	return filler + 1 + 3, 2 + 1 + 1, filler + 5
}

type gsDetailFile struct {
	Path      string  `json:"path"`
	Additions int     `json:"additions"`
	Deletions int     `json:"deletions"`
	Patch     *string `json:"patch"`
}

type gsDetail struct {
	ID         string         `json:"id"`
	Body       string         `json:"body"`
	Additions  int            `json:"additions"`
	Deletions  int            `json:"deletions"`
	FilesCount int            `json:"files_count"`
	Files      []gsDetailFile `json:"files"`
	Commits    []struct {
		SHA    string `json:"sha"`
		Author string `json:"author"`
	} `json:"commits"`
	LinkedIssue *struct {
		Number int    `json:"number"`
		State  string `json:"state"`
		Title  string `json:"title"`
	} `json:"linked_issue"`
}

func gsDecodeDetail(t *testing.T, resp response) gsDetail {
	t.Helper()
	if resp.Status != http.StatusOK {
		t.Fatalf("GET /pulls/:id = %d %s, want 200", resp.Status, resp.Body)
	}
	var d gsDetail
	if err := json.Unmarshal(resp.Body, &d); err != nil {
		t.Fatalf("GET /pulls/:id body is not a detail: %v\n%s", err, resp.Body)
	}
	return d
}

// AC17 (FR24, FR25, EC18): the detail of a MR with three diff pages, two commit pages and a linked issue.
func TestGsSync_AC17_DetailFilesCommitsIssueAndStats(t *testing.T) {
	h := gsStart(t, gsOpts{labs: 1})
	lab := h.labs[0]
	h.connect(lab, "Cloud", gsToken)
	lab.setProject(gsProject{id: 42, path: "acme/payments", defaultBranch: "main"})
	desc := "Fixes #7\n\nMore text"
	lab.setMRs(42, gsMR{iid: 5, title: "Big MR", author: "ada", state: "opened", sha: "sha5", source: 42, target: 42, created: gsEpoch, updated: gsEpoch, description: &desc})
	lab.setIssues(42, gsIssue{iid: 7, title: "The bug", description: "details", state: "opened"})
	adds, dels, files := gsDetailFixture(lab, 42, 5)
	lab.setStats(42, map[int]gsStat{5: {add: 300, del: 9, files: 231}})
	repo := h.addRepo(lab, 42, "acme/payments")
	if resp := h.s.get("/repos/" + repo + "/pulls"); resp.Status != http.StatusOK {
		t.Fatalf("GET /repos/%s/pulls = %d %s, want 200", repo, resp.Status, resp.Body)
	}
	pr, ok := h.prByNumber(repo, 5)
	if !ok {
		t.Fatal("the list sync did not store MR 5")
	}
	before := len(lab.requests())

	d := gsDecodeDetail(t, h.s.get("/pulls/"+pr.idOf(h)))
	if d.Body != desc {
		t.Errorf("detail body = %q, want the MR description %q", d.Body, desc)
	}
	// The GraphQL summary decides the stats (FR24, FR26).
	if d.Additions != 300 || d.Deletions != 9 || d.FilesCount != 231 {
		t.Errorf("detail stats = %d/%d/%d, want the GraphQL summary 300/9/231", d.Additions, d.Deletions, d.FilesCount)
	}
	if len(d.Files) != files {
		t.Fatalf("detail files = %d, want %d (two entries of dup.go merged)", len(d.Files), files)
	}
	byPath := map[string]gsDetailFile{}
	for _, f := range d.Files {
		byPath[f.Path] = f
	}
	for _, c := range []struct {
		path      string
		add, del  int
		wantPatch bool
	}{
		{"big.bin", 0, 0, false},
		{"collapsed.txt", 0, 0, false},
		{"gone.txt", 0, 2, true}, // a deleted file is listed under its old path with its - lines
		{"new/name.go", 1, 1, true},
		{"dup.go", 3, 1, true},
		{"filler/f000.txt", 1, 0, true},
	} {
		f, ok := byPath[c.path]
		if !ok {
			t.Errorf("detail has no file %q", c.path)
			continue
		}
		if f.Additions != c.add || f.Deletions != c.del || (f.Patch != nil) != c.wantPatch {
			t.Errorf("file %s = +%d -%d patch %v, want +%d -%d patch %v", c.path, f.Additions, f.Deletions, f.Patch != nil, c.add, c.del, c.wantPatch)
		}
	}
	if _, ok := byPath["old/name.go"]; ok {
		t.Error("detail lists the renamed file under its old path, want new_path only")
	}
	if dup := byPath["dup.go"]; dup.Patch == nil || !strings.Contains(*dup.Patch, "+b") || !strings.Contains(*dup.Patch, "+r") {
		t.Errorf("merged dup.go patch = %v, want both entries' hunks", dup.Patch)
	}

	// Commits: 120 over two pages, oldest first like GitHub's; a missing author name is "unknown".
	if len(d.Commits) != 120 || d.Commits[0].SHA != "c119" || d.Commits[119].SHA != "c000" || d.Commits[119].Author != "unknown" || d.Commits[0].Author != "dev" {
		t.Errorf("detail commits = %d (first %v, last %v), want 120 oldest first c119..c000 with the author-less one unknown", len(d.Commits), d.Commits[0], d.Commits[len(d.Commits)-1])
	}
	if d.LinkedIssue == nil || d.LinkedIssue.Number != 7 || d.LinkedIssue.State != "open" {
		t.Errorf("linked_issue = %+v, want {number 7, state open}", d.LinkedIssue)
	}

	// The requests of the fetch: 3 diff pages, 2 commit pages, the issue.
	var seen []string
	for _, r := range lab.requests()[before:] {
		if r.Path != "/api/graphql" {
			seen = append(seen, r.String())
		}
	}
	for _, want := range []string{
		"GET /api/v4/projects/42/merge_requests/5/diffs?page=1&per_page=100", "GET /api/v4/projects/42/merge_requests/5/diffs?page=2&per_page=100",
		"GET /api/v4/projects/42/merge_requests/5/diffs?page=3&per_page=100",
		"GET /api/v4/projects/42/merge_requests/5/commits?page=1&per_page=100", "GET /api/v4/projects/42/merge_requests/5/commits?page=2&per_page=100",
		"GET /api/v4/projects/42/issues/7",
	} {
		if !slices.Contains(seen, want) {
			t.Errorf("detail fetch did not request %q; requests: %v", want, seen)
		}
	}
	if n := len(h.storedFiles(pr.idOf(h))); n != files {
		t.Errorf("stored files = %d, want %d", n, files)
	}
	if got := h.count(`SELECT count(*) FROM pr_commits WHERE pr_id = ?`, pr.idOf(h)); got != 120 {
		t.Errorf("stored commits = %d, want 120", got)
	}
	stored, _ := h.prByNumber(repo, 5)
	if stored.Additions != 300 || stored.Deletions != 9 || stored.FilesCount != 231 {
		t.Errorf("stored stats = %d/%d/%d, want 300/9/231", stored.Additions, stored.Deletions, stored.FilesCount)
	}

	// An all-zero summary counts as none: the stats are the file sums. The stored files and commits are
	// replaced, not added to.
	lab.setStats(42, map[int]gsStat{5: {}})
	gsDetailFixture(lab, 42, 5)
	d = gsDecodeDetail(t, h.s.get("/pulls/"+pr.idOf(h)))
	if d.Additions != adds || d.Deletions != dels || d.FilesCount != files {
		t.Errorf("detail stats with an all-zero summary = %d/%d/%d, want the file sums %d/%d/%d", d.Additions, d.Deletions, d.FilesCount, adds, dels, files)
	}
	lab.setDiffs(42, 5, gsDiffEntry{oldPath: "only.txt", newPath: "only.txt", diff: "@@ -1 +1 @@\n-a\n+b\n"})
	lab.setCommits(42, 5, gsCommitEntry{id: "only", message: "one", author: "dev", date: gsEpoch})
	d = gsDecodeDetail(t, h.s.get("/pulls/"+pr.idOf(h)))
	if len(d.Files) != 1 || d.Additions != 1 || d.Deletions != 1 || d.FilesCount != 1 {
		t.Errorf("detail after the diff shrank = %d files, stats %d/%d/%d, want 1 file and 1/1/1", len(d.Files), d.Additions, d.Deletions, d.FilesCount)
	}
	if n := len(h.storedFiles(pr.idOf(h))); n != 1 {
		t.Errorf("stored files after the diff shrank = %d, want 1 (replaced)", n)
	}
	if got := h.count(`SELECT count(*) FROM pr_commits WHERE pr_id = ?`, pr.idOf(h)); got != 1 {
		t.Errorf("stored commits after the history shrank = %d, want 1 (replaced)", got)
	}

	// With the stub down the stored detail is served.
	lab.setHook(func(w http.ResponseWriter, r *http.Request) bool {
		gsError(w, http.StatusServiceUnavailable, "down")
		return true
	})
	down := gsDecodeDetail(t, h.s.get("/pulls/"+pr.idOf(h)))
	if len(down.Files) != 1 || down.Files[0].Path != "only.txt" || len(down.Commits) != 1 || down.Body != desc {
		t.Errorf("detail with the stub down = %+v, want the stored one-file detail", down)
	}
}

// idOf is the id of a stored pull request (gsPR has none: looked up by the harness).
func (p gsPR) idOf(h *gsHarness) string {
	h.t.Helper()
	return scalar[string](h.t, h.db, `SELECT id::text FROM pull_requests WHERE number = ? AND head_sha = ?`, p.Number, p.HeadSHA)
}

func (h *gsHarness) count(query string, args ...any) int {
	h.t.Helper()
	return count(h.t, h.db, query, args...)
}

func (h *gsHarness) storedFiles(prID string) []string {
	h.t.Helper()
	var paths []string
	if err := h.db.Raw(`SELECT path FROM pr_files WHERE pr_id = ? ORDER BY path`, prID).Scan(&paths).Error; err != nil {
		h.t.Fatalf("read stored files: %v", err)
	}
	return paths
}

// ---- AC40 / AC20 (rename) ----

const (
	gsOldPath = "acme/platform/payments"
	gsNewPath = "acme/core/payments"
)

// gsRenamedFixture is a connected stub whose project 42 was renamed from gsOldPath to gsNewPath, and
// the stored repository (id 42's row) with a real clone directory.
type gsRenamedFixture struct {
	h        *gsHarness
	lab      *gsLab
	repo     string
	cloneDir string
}

func gsRenamed(t *testing.T, h *gsHarness) gsRenamedFixture {
	t.Helper()
	lab := h.labs[0]
	h.connect(lab, "Cloud", gsToken)
	lab.setProject(gsProject{id: 42, path: gsNewPath, defaultBranch: "develop"})
	lab.setMRs(42, gsMR{iid: 1, title: "Renamed project MR", author: "ada", state: "opened", sha: "sha1", source: 42, target: 42, created: gsEpoch, updated: gsEpoch})
	f := gsRenamedFixture{h: h, lab: lab}
	f.cloneDir = filepath.Join(h.s.cfg.CloneDir, "@gitlab", strings.ReplaceAll(lab.host, ":", "_"), "42")
	if err := os.MkdirAll(f.cloneDir, 0o755); err != nil {
		t.Fatal(err)
	}
	gitRun(t, f.cloneDir, "init", "-q", "-b", "main")
	gitRun(t, f.cloneDir, "remote", "add", "origin", lab.URL+"/"+gsOldPath+".git")
	if err := os.WriteFile(filepath.Join(f.cloneDir, "marker.txt"), []byte("clone\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f.repo = h.addRepo(lab, 42, gsOldPath)
	h.exec(`UPDATE repos SET clone_path = ?, default_branch = 'main' WHERE id = ?`, f.cloneDir, f.repo)
	return f
}

func (h *gsHarness) exec(query string, args ...any) {
	h.t.Helper()
	(&lbFixture{t: h.t, db: h.db}).exec(query, args...)
}

// check asserts the AC40 outcome of one channel: the identity follows the project, the clone and the
// default branch stay, MRs are upserted by id when synced, and the next refresh job fetches from the new path.
func (f gsRenamedFixture) check(t *testing.T, synced bool) {
	t.Helper()
	h := f.h
	want := gsRepoRow{
		FullName: gsNewPath, Owner: "acme/core", Name: "payments", WebURL: f.lab.URL + "/" + gsNewPath,
		DefaultBranch: "main", ProjectID: 42,
	}
	if diff := cmp.Diff(want, h.repoRow(f.repo)); diff != "" {
		t.Errorf("repository after the sync mismatch (-want +got; web_url must be built from the connection, default_branch unchanged):\n%s", diff)
	}
	if synced {
		if prs := h.prs(f.repo); len(prs) != 1 || prs[0].Number != 1 || prs[0].Title != "Renamed project MR" {
			t.Errorf("stored MRs after the sync = %+v, want MR 1 upserted by the project id", prs)
		}
	}
	if b, err := os.ReadFile(filepath.Join(f.cloneDir, "marker.txt")); err != nil || string(b) != "clone\n" {
		t.Errorf("clone marker = %q, %v, want the clone directory unchanged", b, err)
	}
	var dirs []string
	for _, p := range []string{filepath.Join(h.s.cfg.CloneDir, "@gitlab"), filepath.Dir(f.cloneDir)} {
		entries, err := os.ReadDir(p)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			dirs = append(dirs, filepath.Join(p, e.Name()))
		}
	}
	if len(dirs) != 2 {
		t.Errorf("directories under the GitLab clone root = %v, want only the one clone location", dirs)
	}

	// The next refresh job addresses the new path: its first git call points origin there (the stub is not
	// a git server, so the job itself fails after that).
	if resp := h.s.post("/repos/"+f.repo+"/refresh", nil); resp.Status != http.StatusOK {
		t.Fatalf("POST /repos/%s/refresh = %d %s, want 200", f.repo, resp.Status, resp.Body)
	}
	h.s.waitJobs("refresh", 1)
	log, err := os.ReadFile(h.gitLog)
	if err != nil {
		t.Fatalf("read the git call log: %v", err)
	}
	wantURL := f.lab.URL + "/" + gsNewPath + ".git"
	lines := strings.Split(string(log), "\n")
	setURL := -1
	for i, l := range lines {
		if l == wantURL && i > 0 && lines[i-1] == "origin" && i > 1 && lines[i-2] == "set-url" {
			setURL = i
		}
	}
	if setURL < 0 {
		t.Errorf("the refresh job did not run `git remote set-url origin %s`; git calls:\n%s", wantURL, log)
	}
	if strings.Contains(string(log), gsOldPath) && strings.Contains(string(log), "set-url\norigin\n"+f.lab.URL+"/"+gsOldPath) {
		t.Errorf("the refresh job still used the old path; git calls:\n%s", log)
	}
}

// AC40 (FR47, FR15, EC20) and AC20 (FR17): a project renamed on the stub is followed by id through the
// add by the new URL, the list request and the manual poll; the background poll is in gitlab_poll_test.go.
func TestGsSync_AC40_RenamedProjectIsFollowedByIDThroughEveryRoute(t *testing.T) {
	t.Run("POST /repos by the new URL", func(t *testing.T) {
		f := gsRenamed(t, gsStart(t, gsOpts{labs: 1, git: true}))
		resp := f.h.s.post("/repos", map[string]any{"url": f.lab.URL + "/" + gsNewPath})
		if resp.Status != http.StatusOK {
			t.Fatalf("POST /repos by the new URL = %d %s, want 200 with the existing repository", resp.Status, resp.Body)
		}
		if id, _ := resp.Obj(t)["id"].(string); id != f.repo {
			t.Errorf("POST /repos answered repository %q, want the existing %q", id, f.repo)
		}
		if n := f.h.count(`SELECT count(*) FROM repos`); n != 1 {
			t.Errorf("repositories after the add = %d, want 1 (no second repository)", n)
		}
		f.check(t, false)
	})
	t.Run("GET /repos/:id/pulls", func(t *testing.T) {
		f := gsRenamed(t, gsStart(t, gsOpts{labs: 1, git: true}))
		if resp := f.h.s.get("/repos/" + f.repo + "/pulls"); resp.Status != http.StatusOK {
			t.Fatalf("GET /repos/%s/pulls = %d %s, want 200", f.repo, resp.Status, resp.Body)
		}
		f.check(t, true)
		gsFirstRequestsAreByID(t, f.lab)
	})
	t.Run("POST /repos/:id/poll", func(t *testing.T) {
		f := gsRenamed(t, gsStart(t, gsOpts{labs: 1, git: true}))
		resp := f.h.s.post("/repos/"+f.repo+"/poll", nil)
		if resp.Status != http.StatusOK {
			t.Fatalf("POST /repos/%s/poll = %d %s, want 200", f.repo, resp.Status, resp.Body)
		}
		f.check(t, true)
		gsFirstRequestsAreByID(t, f.lab)
	})
}

// gsFirstRequestsAreByID asserts a sync addressed the project by its number, never by the stored (old) path.
func gsFirstRequestsAreByID(t *testing.T, lab *gsLab) {
	t.Helper()
	for _, r := range lab.requests() {
		if strings.HasPrefix(r.Path, "/api/") && strings.Contains(r.Path, "acme") {
			t.Errorf("request %s addresses the project by path, want its id", r)
		}
	}
	if reqs := lab.requestsMatching("GET /api/v4/projects/42/merge_requests"); len(reqs) == 0 {
		t.Error("no merge request list request reached the project's id")
	}
}

// ---- AC41 ----

// AC41 (FR15, FR47, EC34, EC35): a renamed project whose new path another repository still holds keeps its
// stored path with one WARN naming both repositories; the add of that path is a 409 until the holder syncs.
func TestGsSync_AC41_PathCollision(t *testing.T) {
	t.Run("EC34 the sync keeps the stored path until the holder moves", func(t *testing.T) {
		h := gsStart(t, gsOpts{labs: 1})
		lab := h.labs[0]
		h.connect(lab, "Cloud", gsToken)
		lab.setProject(gsProject{id: 11, path: "acme/new", defaultBranch: "main"})   // X, renamed to acme/new
		lab.setProject(gsProject{id: 12, path: "acme/newer", defaultBranch: "main"}) // Y was moved on GitLab, its row is stale
		lab.setMRs(11, gsMR{iid: 1, title: "X's MR", author: "ada", state: "opened", sha: "x1", source: 11, target: 11, created: gsEpoch, updated: gsEpoch})
		lab.setMRs(12, gsMR{iid: 1, title: "Y's MR", author: "ada", state: "opened", sha: "y1", source: 12, target: 12, created: gsEpoch, updated: gsEpoch})
		x := h.addRepo(lab, 11, "acme/old")
		y := h.addRepo(lab, 12, "acme/new")

		if resp := h.s.get("/repos/" + x + "/pulls"); resp.Status != http.StatusOK {
			t.Fatalf("GET /repos/%s/pulls = %d %s, want 200", x, resp.Status, resp.Body)
		}
		if got := h.repoRow(x).FullName; got != "acme/old" {
			t.Errorf("X's full_name after its sync = %q, want it to keep acme/old", got)
		}
		if prs := h.prs(x); len(prs) != 1 || prs[0].Title != "X's MR" {
			t.Errorf("X's stored MRs = %+v, want its MR upserted", prs)
		}
		var named []map[string]any
		for _, e := range h.s.logs.entries(t) {
			if msg, _ := e["msg"].(string); e["level"] == "WARN" && strings.Contains(msg, "project path") {
				named = append(named, e)
			}
		}
		if len(named) != 1 || named[0]["repo_id"] != x || named[0]["other_repo_id"] != y {
			t.Errorf("path WARN lines = %v, want exactly one, naming X (%s) as repo_id and Y (%s) as other_repo_id", named, x, y)
		}

		// Y syncs and stores its new path; then X's next sync stores acme/new.
		if resp := h.s.get("/repos/" + y + "/pulls"); resp.Status != http.StatusOK {
			t.Fatalf("GET /repos/%s/pulls = %d %s, want 200", y, resp.Status, resp.Body)
		}
		if got := h.repoRow(y).FullName; got != "acme/newer" {
			t.Errorf("Y's full_name after its sync = %q, want acme/newer", got)
		}
		if resp := h.s.get("/repos/" + x + "/pulls"); resp.Status != http.StatusOK {
			t.Fatalf("second GET /repos/%s/pulls = %d %s, want 200", x, resp.Status, resp.Body)
		}
		if got := h.repoRow(x).FullName; got != "acme/new" {
			t.Errorf("X's full_name after Y moved = %q, want acme/new", got)
		}
	})
	t.Run("EC35 the add of a path another project still holds is a 409", func(t *testing.T) {
		h := gsStart(t, gsOpts{labs: 1})
		lab := h.labs[0]
		h.connect(lab, "Cloud", gsToken)
		lab.setProject(gsProject{id: 3, path: "acme/new", defaultBranch: "main"})    // the project being added
		lab.setProject(gsProject{id: 12, path: "acme/other", defaultBranch: "main"}) // Y, stored as acme/new, was moved on GitLab
		y := h.addRepo(lab, 12, "acme/new")
		jobs := len(jobRows(t, h.db, ""))

		resp := h.s.post("/repos", map[string]any{"url": lab.URL + "/acme/new"})
		code, _ := gsErrorOf(t, resp)
		envelope, _ := resp.JSON(t).(map[string]any)["error"].(map[string]any)
		details, _ := envelope["details"].(map[string]any)
		if resp.Status != http.StatusConflict || code != "gitlab_path_conflict" || details["repo_id"] != y {
			t.Errorf("POST /repos of a held path = %d %s, want 409 gitlab_path_conflict with details.repo_id %s", resp.Status, resp.Body, y)
		}
		if n := h.count(`SELECT count(*) FROM repos`); n != 1 {
			t.Errorf("repositories after the refused add = %d, want 1 (nothing stored)", n)
		}
		if n := len(jobRows(t, h.db, "")); n != jobs {
			t.Errorf("jobs after the refused add = %d, want %d (no job)", n, jobs)
		}

		// Y syncs and stores its new path; the same add now succeeds.
		if resp := h.s.get("/repos/" + y + "/pulls"); resp.Status != http.StatusOK {
			t.Fatalf("GET /repos/%s/pulls = %d %s, want 200", y, resp.Status, resp.Body)
		}
		resp = h.s.post("/repos", map[string]any{"url": lab.URL + "/acme/new"})
		if resp.Status != http.StatusCreated {
			t.Errorf("POST /repos after the holder moved = %d %s, want 201", resp.Status, resp.Body)
		}
	})
}

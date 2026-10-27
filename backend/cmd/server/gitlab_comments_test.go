package main

import (
	"crypto/tls"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/google/go-cmp/cmp"
	"gorm.io/gorm"

	"dev-digest-go/backend/internal/config"
	"dev-digest-go/backend/internal/platform/postgres/migrate"
	"dev-digest-go/backend/internal/platform/postgres/pgtest"
	"dev-digest-go/backend/migrations"
)

// Component tests of the GitLab merge request comments (GitLab B, plan S20, spec AC1-AC9, AC11,
// AC13-AC15, AC30) through the real server: a real Postgres, the pulls routes and a TLS stub GitLab
// on 127.0.0.1 whose certificate is the extra CA (cfg.ExtraCAFile; SSL_CERT_FILE for the git CA
// bundle, INSIGHTS). The stub plays the GitLab API of merge request !3 of project 42; every post
// goes through the detail fetch first, like the UI (the diff head and the previous paths are
// stored by it). Every package-level name here starts with gb.

const (
	gbToken   = "glpat-gb0123456789abcdefghij"
	gbPath    = "acme/web"
	gbProject = 42
	gbIID     = 3
	gbMRPath  = "/api/v4/projects/42/merge_requests/3"

	gbCreated = "2026-10-20T10:00:00Z" // note timestamps of the stub
	gbWire    = "2026-10-20T10:00:00.000Z"
	gbNew     = "2026-10-21T10:00:00.000Z" // created_at of a note the stub creates
)

var (
	gbBaseSHA  = strings.Repeat("b", 40)
	gbStartSHA = strings.Repeat("c", 40)
	gbHeadSHA  = strings.Repeat("d", 40)
	gbOldSHA   = strings.Repeat("e", 40)
	gbOtherSHA = strings.Repeat("f", 40)
)

// gbPatchA changes a.ts: three lines added at new 3-5, old line 7 removed, so new 12 is the
// unchanged old line 10.
const gbPatchA = "@@ -1,10 +1,12 @@\n l1\n l2\n+a3\n+a4\n+a5\n l3\n l4\n l5\n l6\n-gone7\n l8\n l9\n l10\n"

// gbPatchB has two hunks with a gap between them (new/old lines 4-49).
const gbPatchB = "@@ -1,3 +1,3 @@\n a\n-b\n+c\n d\n@@ -50,2 +50,2 @@\n x\n-y\n+z\n"

// ---- stub GitLab ----

type gbReq struct {
	Method, Path, Query, Token string
	Body                       []byte
}

func (r gbReq) String() string {
	if r.Query == "" {
		return r.Method + " " + r.Path
	}
	return r.Method + " " + r.Path + "?" + r.Query
}

type gbRefs struct{ base, start, head string }

type gbFile struct{ oldPath, newPath, diff string }

// gbMR is merge request !3 of the stub: refs nil answers "diff_refs": null.
type gbMR struct {
	state       string // opened | merged
	sha         string
	refs        *gbRefs
	versions    []gbRefs
	files       []gbFile
	discussions []map[string]any
}

type gbLab struct {
	t    testing.TB
	srv  *httptest.Server
	URL  string
	host string

	mu       sync.Mutex
	reqs     []gbReq
	mr       gbMR
	nextNote int64
	nextDisc int
	hook     func(w http.ResponseWriter, r *http.Request, body []byte) bool
}

func gbNewLab(t testing.TB) *gbLab {
	t.Helper()
	l := &gbLab{t: t, nextNote: 7000}
	l.mr = gbMR{
		state: "opened", sha: gbHeadSHA, refs: &gbRefs{gbBaseSHA, gbStartSHA, gbHeadSHA},
		files: []gbFile{{"a.ts", "a.ts", gbPatchA}},
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

func (l *gbLab) caPEM() []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: l.srv.Certificate().Raw})
}

// set changes the stub's merge request.
func (l *gbLab) set(fn func(m *gbMR)) {
	l.mu.Lock()
	defer l.mu.Unlock()
	fn(&l.mr)
}

func (l *gbLab) setHook(fn func(w http.ResponseWriter, r *http.Request, body []byte) bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.hook = fn
}

func (l *gbLab) requests() []gbReq {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]gbReq(nil), l.reqs...)
}

func (l *gbLab) count() int { return len(l.requests()) }

// since are the requests after the first n.
func (l *gbLab) since(n int) []gbReq { return l.requests()[n:] }

// creates are the POSTs of discussions and notes after the first n requests (the GraphQL stats
// query of a detail fetch is a POST too).
func (l *gbLab) creates(n int) []gbReq {
	var out []gbReq
	for _, r := range l.since(n) {
		if r.Method == http.MethodPost && strings.HasPrefix(r.Path, gbMRPath+"/discussions") {
			out = append(out, r)
		}
	}
	return out
}

func gbSeq(reqs []gbReq) []string {
	out := make([]string, len(reqs))
	for i, r := range reqs {
		out[i] = r.String()
	}
	return out
}

func gbJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (l *gbLab) serveHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body) // drain first: a handler that blocks must not hold an unread POST body
	l.mu.Lock()
	l.reqs = append(l.reqs, gbReq{Method: r.Method, Path: r.URL.Path, Query: r.URL.RawQuery, Token: r.Header.Get("PRIVATE-TOKEN"), Body: body})
	hook := l.hook
	l.mu.Unlock()
	if hook != nil && hook(w, r, body) {
		return
	}
	p := r.URL.Path
	switch {
	case p == "/api/graphql" && r.Method == http.MethodPost:
		gbJSON(w, http.StatusOK, map[string]any{"errors": []map[string]string{{"message": "GraphQL is disabled"}}})
	case strings.HasPrefix(p, gbMRPath):
		l.mrRoute(w, r, body, strings.TrimPrefix(p, gbMRPath))
	default:
		gbJSON(w, http.StatusNotFound, map[string]string{"message": "404 Not Found"})
	}
}

func (l *gbLab) mrRoute(w http.ResponseWriter, r *http.Request, body []byte, tail string) {
	l.mu.Lock()
	m := l.mr
	l.mu.Unlock()
	get := r.Method == http.MethodGet
	switch {
	case tail == "" && get:
		gbJSON(w, http.StatusOK, gbMRJSON(m))
	case tail == "/diffs" && get:
		out := []map[string]any{}
		for _, f := range m.files {
			out = append(out, map[string]any{"old_path": f.oldPath, "new_path": f.newPath, "diff": f.diff,
				"deleted_file": false, "collapsed": false, "too_large": false})
		}
		gbJSON(w, http.StatusOK, out)
	case tail == "/commits" && get:
		gbJSON(w, http.StatusOK, []map[string]any{{"id": gbHeadSHA, "message": "feat: x", "author_name": "ann", "committed_date": gbCreated}})
	case tail == "/versions" && get:
		out := []map[string]any{}
		for i, v := range m.versions {
			if i == 0 { // per_page=1
				out = append(out, map[string]any{"id": 1, "base_commit_sha": v.base, "start_commit_sha": v.start, "head_commit_sha": v.head})
			}
		}
		gbJSON(w, http.StatusOK, out)
	case tail == "/discussions" && get:
		gbJSON(w, http.StatusOK, gbPage(m.discussions, r))
	case tail == "/discussions" && r.Method == http.MethodPost:
		var in struct {
			Body     string         `json:"body"`
			Position map[string]any `json:"position"`
		}
		_ = json.Unmarshal(body, &in)
		l.mu.Lock()
		l.nextNote++
		l.nextDisc++
		note, disc := l.nextNote, l.nextDisc
		l.mu.Unlock()
		gbJSON(w, http.StatusCreated, gbDisc(fmt.Sprintf("%040x", 0xc0de0000+disc),
			gbNote(note, "DiffNote", in.Body, "ada", in.Position, "2026-10-21T10:00:00Z")))
	case strings.HasPrefix(tail, "/discussions/") && strings.HasSuffix(tail, "/notes") && r.Method == http.MethodPost:
		var in struct {
			Body string `json:"body"`
		}
		_ = json.Unmarshal(body, &in)
		l.mu.Lock()
		l.nextNote++
		note := l.nextNote
		l.mu.Unlock()
		gbJSON(w, http.StatusCreated, gbNote(note, "DiscussionNote", in.Body, "ada", nil, "2026-10-21T10:00:00Z"))
	default:
		gbJSON(w, http.StatusNotFound, map[string]string{"message": "404 Not Found"})
	}
}

// gbPage is the page `page` of `per_page` discussions.
func gbPage(items []map[string]any, r *http.Request) []map[string]any {
	var page, per int
	_, _ = fmt.Sscan(r.URL.Query().Get("page"), &page)
	_, _ = fmt.Sscan(r.URL.Query().Get("per_page"), &per)
	if page < 1 {
		page = 1
	}
	if per < 1 {
		per = 20
	}
	lo := min((page-1)*per, len(items))
	hi := min(lo+per, len(items))
	return append([]map[string]any{}, items[lo:hi]...)
}

func gbMRJSON(m gbMR) map[string]any {
	out := map[string]any{
		"iid": gbIID, "title": "Add feature", "description": nil, "state": m.state, "draft": false,
		"source_branch": "feat", "target_branch": "main", "sha": m.sha,
		"source_project_id": gbProject, "target_project_id": gbProject,
		"created_at": gbCreated, "updated_at": gbCreated, "merged_at": nil, "closed_at": nil,
		"web_url": "https://evil.example/mr", "author": map[string]any{"username": "octo"}, "diff_refs": nil,
	}
	if m.state == "merged" {
		out["merged_at"] = "2026-10-21T09:00:00Z"
	}
	if m.refs != nil {
		out["diff_refs"] = map[string]any{"base_sha": m.refs.base, "start_sha": m.refs.start, "head_sha": m.refs.head}
	}
	return out
}

// gbNote is a note of a discussion; typ "" is a plain note (type null), pos nil has no position.
func gbNote(id int64, typ, body, user string, pos map[string]any, created string) map[string]any {
	n := map[string]any{
		"id": id, "type": nil, "system": false, "body": body, "created_at": created,
		"author": map[string]any{"username": user}, "position": nil,
	}
	if typ != "" {
		n["type"] = typ
	}
	if pos != nil {
		n["position"] = pos
	}
	return n
}

func gbSystemNote(id int64) map[string]any {
	n := gbNote(id, "", "added 1 commit", "ann", nil, gbCreated)
	n["system"] = true
	return n
}

func gbDisc(id string, notes ...map[string]any) map[string]any {
	ns := make([]any, len(notes))
	for i, n := range notes {
		ns[i] = n
	}
	return map[string]any{"id": id, "notes": ns}
}

func gbDiscID(n int) string { return fmt.Sprintf("%040x", 0xd15c0000+n) }

// gbPos is a text position of a note; a nil line is JSON null.
func gbPos(path string, oldLine, newLine *int, head string) map[string]any {
	p := map[string]any{
		"position_type": "text", "base_sha": gbBaseSHA, "start_sha": gbStartSHA, "head_sha": head,
		"old_path": path, "new_path": path, "old_line": nil, "new_line": nil,
	}
	if oldLine != nil {
		p["old_line"] = *oldLine
	}
	if newLine != nil {
		p["new_line"] = *newLine
	}
	return p
}

func gbInt(v int) *int { return &v }

// gbDiffThread is a diff discussion: a root note and the replies by the given users.
func gbDiffThread(n int, rootID int64, path string, oldLine, newLine *int, head, user string, replies ...map[string]any) map[string]any {
	root := gbNote(rootID, "DiffNote", fmt.Sprintf("root %d", rootID), user, gbPos(path, oldLine, newLine, head), gbCreated)
	return gbDisc(gbDiscID(n), append([]map[string]any{root}, replies...)...)
}

// ---- harness ----

type gbOpts struct {
	noConnection bool // the repository's host has no connection
	noToken      bool // the connection has no token
	noDetail     bool // skip the detail fetch that stores the diff head and the previous paths
	githubToken  bool
	files        []gbFile // the stub's diff; nil = a.ts
	merged       bool
}

type gbEnv struct {
	t    *testing.T
	s    *stack
	db   *gorm.DB
	lab  *gbLab
	ws   string
	repo string // the GitLab repository acme/web
	pr   string // !3
	web  string // the stored web URL of the repository
}

func gbStart(t *testing.T, o gbOpts) *gbEnv {
	t.Helper()
	db := pgtest.Migrated(t)
	lab := gbNewLab(t)
	if o.files != nil {
		lab.set(func(m *gbMR) { m.files = o.files })
	}
	if o.merged {
		lab.set(func(m *gbMR) { m.state = "merged" })
	}
	caFile := filepath.Join(t.TempDir(), "gb-ca.pem")
	if err := os.WriteFile(caFile, lab.caPEM(), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SSL_CERT_FILE", caFile) // the git CA bundle of the server needs a system bundle (INSIGHTS)

	e := &gbEnv{t: t, db: db.DB, lab: lab, ws: lbID(idWorkspace, 1), repo: lbID(idRepo, 1), web: lab.URL + "/" + gbPath}
	e.exec(`INSERT INTO workspaces (id, name) VALUES (?, 'default')`, e.ws)
	e.exec(`INSERT INTO users (id, email, name) VALUES (?, 'you@local', 'You')`, lbID(idUser, 1))

	env := map[string]string{}
	if o.githubToken {
		env["GITHUB_TOKEN"] = pollToken
	}
	e.s = startStack(t, spec{db: db, noSeed: true, env: env, config: func(c *config.Config) { c.ExtraCAFile = caFile }})

	if !o.noConnection {
		body := map[string]any{"name": "Cloud", "base_url": lab.URL}
		if !o.noToken {
			body["token"] = gbToken
		}
		if resp := e.s.post("/settings/gitlab-connections", body); resp.Status != http.StatusCreated && resp.Status != http.StatusOK {
			t.Fatalf("POST /settings/gitlab-connections = %d %s, want 201", resp.Status, resp.Body)
		}
	}
	e.exec(`INSERT INTO repos (id, workspace_id, owner, name, full_name, provider, host, project_id, web_url)
		VALUES (?, ?, 'acme', 'web', ?, 'gitlab', ?, ?, ?)`, e.repo, e.ws, gbPath, lab.host, gbProject, e.web)
	e.exec(`INSERT INTO pull_requests (workspace_id, repo_id, number, title, author, branch, base, head_sha,
			additions, deletions, files_count, status, opened_at, updated_at, draft, head_is_fork)
		VALUES (?, ?, ?, 'Add feature', 'octo', 'feat', 'main', ?, 0, 0, 0, ?, now(), now(), false, false)`,
		e.ws, e.repo, gbIID, gbHeadSHA, map[bool]string{false: "open", true: "merged"}[o.merged])
	e.pr = scalar[string](t, e.db, `SELECT id::text FROM pull_requests WHERE repo_id = ? AND number = ?`, e.repo, gbIID)
	if !o.noDetail {
		e.detail()
	}
	return e
}

func (e *gbEnv) exec(query string, args ...any) {
	e.t.Helper()
	if err := e.db.Exec(query, args...).Error; err != nil {
		e.t.Fatalf("exec %q: %v", query, err)
	}
}

// detail runs the detail fetch (GET /pulls/:id), which stores the files, the diff head and the
// previous paths, and returns the response.
func (e *gbEnv) detail() response {
	e.t.Helper()
	resp := e.s.get("/pulls/" + e.pr)
	if resp.Status != http.StatusOK {
		e.t.Fatalf("GET /pulls/%s = %d %s, want 200", e.pr, resp.Status, resp.Body)
	}
	return resp
}

func (e *gbEnv) comments() response { e.t.Helper(); return e.s.get("/pulls/" + e.pr + "/comments") }

func (e *gbEnv) post(body map[string]any) response {
	e.t.Helper()
	return e.s.post("/pulls/"+e.pr+"/comments", body)
}

// gbAt is the body of a post at path:line, side RIGHT.
func gbAt(path string, line int, text string) map[string]any {
	return map[string]any{"path": path, "line": line, "side": "RIGHT", "body": text}
}

// gbErr returns code, message and details of an error envelope.
func gbErr(t testing.TB, r response) (code, message string, details map[string]any) {
	t.Helper()
	e, _ := r.Obj(t)["error"].(map[string]any)
	code, _ = e["code"].(string)
	message, _ = e["message"].(string)
	details, _ = e["details"].(map[string]any)
	return code, message, details
}

// gbWantFailure checks a 400 of the comment flow: its code and details.reason.
func gbWantFailure(t testing.TB, what string, r response, code, reason string) map[string]any {
	t.Helper()
	gotCode, _, details := gbErr(t, r)
	if r.Status != http.StatusBadRequest || gotCode != code || details["reason"] != reason {
		t.Errorf("%s = %d %s, want 400 %s with reason %s", what, r.Status, r.Body, code, reason)
	}
	return details
}

// gbSentCreate decodes a create request: its body, its position and the keys of its JSON object.
func gbSentCreate(t testing.TB, r gbReq) (body string, position map[string]any, keys []string) {
	t.Helper()
	var raw map[string]any
	if err := json.Unmarshal(r.Body, &raw); err != nil {
		t.Fatalf("create body %q is not JSON: %v", r.Body, err)
	}
	body, _ = raw["body"].(string)
	position, _ = raw["position"].(map[string]any)
	for k := range raw {
		keys = append(keys, k)
	}
	return body, position, keys
}

// gbComment is one PrReviewComment of the wire.
type gbComment struct {
	id      int64
	path    string
	line    *int
	orig    *int
	side    string
	body    string
	user    string
	created string
	reply   *int64
	old     bool
}

func (c gbComment) json(web string) map[string]any {
	num := func(p *int) any {
		if p == nil {
			return nil
		}
		return float64(*p)
	}
	var reply any
	if c.reply != nil {
		reply = float64(*c.reply)
	}
	created := c.created
	if created == "" {
		created = gbWire
	}
	return map[string]any{
		"id": float64(c.id), "path": c.path, "line": num(c.line), "original_line": num(c.orig), "side": c.side,
		"body": c.body, "user": c.user, "created_at": created,
		"html_url":       fmt.Sprintf("%s/-/merge_requests/%d#note_%d", web, gbIID, c.id),
		"in_reply_to_id": reply, "is_outdated": c.old,
	}
}

func gbID(v int64) *int64 { return &v }

// gbDiff compares two JSON values.
func gbDiff(t testing.TB, what string, want, got any) {
	t.Helper()
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("%s mismatch (-want +got):\n%s", what, diff)
	}
}

// ---- AC1 ----

// AC1 (FR1-FR3, EC1): the read keeps the text diff threads only (not system notes, a general
// discussion, an image or a file position); every non-system note of a kept thread is a comment, the
// replies carrying the root's path, side and line.
func TestGbComments_AC1_ListKeepsOnlyTextDiffThreads(t *testing.T) {
	e := gbStart(t, gbOpts{})
	filePos := gbPos("a.ts", nil, nil, gbHeadSHA)
	filePos["position_type"] = "file"
	imagePos := gbPos("logo.png", nil, gbInt(1), gbHeadSHA)
	imagePos["position_type"] = "image"
	e.lab.set(func(m *gbMR) {
		m.discussions = []map[string]any{
			gbDisc(gbDiscID(1), gbSystemNote(101)),
			gbDisc(gbDiscID(2), gbNote(102, "DiscussionNote", "General remark", "bob", nil, gbCreated)),
			gbDisc(gbDiscID(3), gbNote(103, "DiffNote", "On an image", "bob", imagePos, gbCreated)),
			gbDisc(gbDiscID(4), gbNote(104, "DiffNote", "On a file", "bob", filePos, gbCreated)),
			gbDiffThread(5, 105, "a.ts", nil, gbInt(5), gbHeadSHA, "ann",
				gbNote(106, "DiscussionNote", "first reply", "bob", nil, "2026-10-20T11:00:00Z"),
				gbNote(107, "DiscussionNote", "second reply", "cat", nil, "2026-10-20T12:00:00Z")),
		}
	})
	n := e.lab.count()

	resp := e.comments()
	if resp.Status != http.StatusOK {
		t.Fatalf("GET /pulls/:id/comments = %d %s, want 200", resp.Status, resp.Body)
	}
	want := []any{
		gbComment{id: 105, path: "a.ts", line: gbInt(5), orig: gbInt(5), side: "RIGHT", body: "root 105", user: "ann"}.json(e.web),
		gbComment{id: 106, path: "a.ts", line: gbInt(5), orig: gbInt(5), side: "RIGHT", body: "first reply", user: "bob",
			created: "2026-10-20T11:00:00.000Z", reply: gbID(105)}.json(e.web),
		gbComment{id: 107, path: "a.ts", line: gbInt(5), orig: gbInt(5), side: "RIGHT", body: "second reply", user: "cat",
			created: "2026-10-20T12:00:00.000Z", reply: gbID(105)}.json(e.web),
	}
	gbDiff(t, "GET /pulls/:id/comments", want, resp.JSON(t))

	reqs := e.lab.since(n)
	gbDiff(t, "GitLab requests of the read", []string{"GET " + gbMRPath + "/discussions?page=1&per_page=100"}, gbSeq(reqs))
	for _, r := range reqs {
		if r.Token != gbToken {
			t.Errorf("%s carried the token %q, want the connection's", r, r.Token)
		}
	}
}

// ---- AC2 ----

// AC2 (FR2, EC2, NFR5): 350 discussions of which 150 are text diff threads are read with at most
// 3 requests of per_page=100, and exactly the first 100 comments come back in GitLab order.
func TestGbComments_AC2_ReadsThreePagesAndCapsAtOneHundred(t *testing.T) {
	e := gbStart(t, gbOpts{})
	var all []map[string]any
	var wantIDs []float64
	diffThreads := 0
	for i := range 350 {
		if i%7 < 3 { // 150 diff threads
			diffThreads++
			all = append(all, gbDiffThread(i, int64(1000+i), "a.ts", nil, gbInt(i+1), gbHeadSHA, "ann"))
			if len(wantIDs) < 100 {
				wantIDs = append(wantIDs, float64(1000+i))
			}
		} else {
			all = append(all, gbDisc(gbDiscID(i), gbSystemNote(int64(5000+i))))
		}
	}
	if diffThreads != 150 {
		t.Fatalf("the fixture has %d diff threads, want 150", diffThreads)
	}
	e.lab.set(func(m *gbMR) { m.discussions = all })
	n := e.lab.count()

	resp := e.comments()
	if resp.Status != http.StatusOK {
		t.Fatalf("GET comments = %d, want 200", resp.Status)
	}
	var ids []float64
	for _, c := range resp.JSON(t).([]any) {
		ids = append(ids, c.(map[string]any)["id"].(float64))
	}
	gbDiff(t, "comment ids (the first 100 in GitLab order)", wantIDs, ids)
	gbDiff(t, "GitLab requests", []string{
		"GET " + gbMRPath + "/discussions?page=1&per_page=100",
		"GET " + gbMRPath + "/discussions?page=2&per_page=100",
		"GET " + gbMRPath + "/discussions?page=3&per_page=100",
	}, gbSeq(e.lab.since(n)))
}

// ---- AC3 ----

// AC3 (FR3, FR4, EC3): a root whose head_sha differs from the stored diff head is outdated (no
// line, original_line set); a LEFT-only root has side LEFT and its old line; with no stored diff
// head the stored head_sha is the reference.
func TestGbComments_AC3_OutdatedAndLeftOnlyThreads(t *testing.T) {
	e := gbStart(t, gbOpts{})
	e.lab.set(func(m *gbMR) {
		m.discussions = []map[string]any{
			gbDiffThread(1, 301, "a.ts", nil, gbInt(5), gbOldSHA, "ann"),  // at an older head
			gbDiffThread(2, 302, "a.ts", gbInt(7), nil, gbHeadSHA, "ann"), // LEFT only, at the head
		}
	})
	read := func(what string, want ...gbComment) {
		t.Helper()
		resp := e.comments()
		if resp.Status != http.StatusOK {
			t.Fatalf("%s: GET comments = %d %s, want 200", what, resp.Status, resp.Body)
		}
		var wantJSON []any
		for _, c := range want {
			wantJSON = append(wantJSON, c.json(e.web))
		}
		gbDiff(t, what, wantJSON, resp.JSON(t))
	}

	e.exec(`UPDATE pull_requests SET diff_head = ?, head_sha = ? WHERE id = ?`, gbHeadSHA, gbOtherSHA, e.pr)
	read("with the diff head set (the stored head_sha is another one)",
		gbComment{id: 301, path: "a.ts", orig: gbInt(5), side: "RIGHT", body: "root 301", user: "ann", old: true},
		gbComment{id: 302, path: "a.ts", line: gbInt(7), orig: gbInt(7), side: "LEFT", body: "root 302", user: "ann"})

	e.exec(`UPDATE pull_requests SET diff_head = NULL, head_sha = ? WHERE id = ?`, gbOldSHA, e.pr)
	read("with no diff head: the stored head_sha is compared",
		gbComment{id: 301, path: "a.ts", line: gbInt(5), orig: gbInt(5), side: "RIGHT", body: "root 301", user: "ann"},
		gbComment{id: 302, path: "a.ts", orig: gbInt(7), side: "LEFT", body: "root 302", user: "ann", old: true})
}

// ---- AC4 ----

// AC4 (FR1, EC16): without a token, for a repository that is not connected, and when GitLab fails
// (500, an HTML 200, a connection that answers nothing) the read answers 200 []; the first two send
// no GitLab request. The real 30 s call timeout has no test seam, so "no answer" is a connection
// the stub closes (the same unavailable branch, retried like any transient read).
func TestGbComments_AC4_ListFailuresAnswerAnEmptyList(t *testing.T) {
	const warn = "gitlab review comments not fetched; serving none"
	empty := func(t *testing.T, what string, r response) {
		t.Helper()
		if r.Status != http.StatusOK || strings.TrimSpace(string(r.Body)) != "[]" {
			t.Errorf("%s: GET comments = %d %s, want 200 []", what, r.Status, r.Body)
		}
	}
	t.Run("no token", func(t *testing.T) {
		e := gbStart(t, gbOpts{noToken: true, noDetail: true})
		empty(t, "connection without token", e.comments())
		if n := e.lab.count(); n != 0 {
			t.Errorf("a connection without token sent %d GitLab request(s), want none", n)
		}
		if n := len(logLines(t, e.s.logs, warn)); n != 0 {
			t.Errorf("%q logged %d time(s) for a quiet answer, want 0", warn, n)
		}
	})
	t.Run("not connected", func(t *testing.T) {
		e := gbStart(t, gbOpts{noConnection: true, noDetail: true})
		empty(t, "repository without connection", e.comments())
		if n := e.lab.count(); n != 0 {
			t.Errorf("a repository without connection sent %d GitLab request(s), want none", n)
		}
		if n := len(logLines(t, e.s.logs, warn)); n != 0 {
			t.Errorf("%q logged %d time(s) for a quiet answer, want 0", warn, n)
		}
	})
	t.Run("GitLab fails", func(t *testing.T) {
		e := gbStart(t, gbOpts{})
		fail := map[string]func(w http.ResponseWriter){
			"500": func(w http.ResponseWriter) {
				gbJSON(w, http.StatusInternalServerError, map[string]string{"message": "500 Internal Server Error"})
			},
			"an HTML 200": func(w http.ResponseWriter) {
				w.Header().Set("Content-Type", "text/html")
				_, _ = io.WriteString(w, "<html><body>Sign in to GitLab</body></html>")
			},
			"no answer": func(w http.ResponseWriter) {
				hj, ok := w.(http.Hijacker)
				if !ok {
					t.Error("the stub's response writer cannot hijack")
					return
				}
				conn, _, err := hj.Hijack()
				if err != nil {
					t.Errorf("hijack: %v", err)
					return
				}
				_ = conn.Close()
			},
		}
		for _, name := range []string{"500", "an HTML 200", "no answer"} {
			h := fail[name]
			e.lab.setHook(func(w http.ResponseWriter, r *http.Request, _ []byte) bool {
				if r.Method != http.MethodGet || !strings.HasSuffix(r.URL.Path, "/discussions") {
					return false
				}
				h(w)
				return true
			})
			n := e.lab.count()
			empty(t, name, e.comments())
			if e.lab.count() == n {
				t.Errorf("%s: GitLab saw no request, the failure was not exercised", name)
			}
		}
		if n := len(logLines(t, e.s.logs, warn)); n != 3 {
			t.Errorf("%q logged %d time(s) for three failed reads, want exactly 3", warn, n)
		}
	})
}

// ---- AC5 ----

// AC5 (FR6-FR8, EC4, NFR5, NFR6): RIGHT on an unchanged line sends old and new line, LEFT on a
// removed line only the old line, RIGHT on an added line only the new line; every create carries
// the text position with the three SHAs and both paths and no line_code or line_range; each post
// is one MR read and one create, answers 200 with the created comment and logs one INFO line.
func TestGbComments_AC5_PostSendsThePositionOfEachLineKind(t *testing.T) {
	e := gbStart(t, gbOpts{})
	cases := []struct {
		name      string
		req       map[string]any
		old, newL *int
		line      int
		side      string
	}{
		{"unchanged line", gbAt("a.ts", 12, "unchanged"), gbInt(10), gbInt(12), 12, "RIGHT"},
		{"removed line", map[string]any{"path": "a.ts", "line": 7, "side": "LEFT", "body": "removed"}, gbInt(7), nil, 7, "LEFT"},
		{"added line", gbAt("a.ts", 3, "added"), nil, gbInt(3), 3, "RIGHT"},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			n := e.lab.count()
			resp := e.post(tc.req)
			if resp.Status != http.StatusOK {
				t.Fatalf("POST comment = %d %s, want 200", resp.Status, resp.Body)
			}
			reqs := e.lab.since(n)
			gbDiff(t, "GitLab requests of one post", []string{"GET " + gbMRPath, "POST " + gbMRPath + "/discussions"}, gbSeq(reqs))
			body, pos, keys := gbSentCreate(t, reqs[1])
			wantPos := map[string]any{
				"position_type": "text", "base_sha": gbBaseSHA, "start_sha": gbStartSHA, "head_sha": gbHeadSHA,
				"old_path": "a.ts", "new_path": "a.ts",
			}
			if tc.old != nil {
				wantPos["old_line"] = float64(*tc.old)
			}
			if tc.newL != nil {
				wantPos["new_line"] = float64(*tc.newL)
			}
			gbDiff(t, "position of the create request", wantPos, pos)
			if body != tc.req["body"] {
				t.Errorf("create body = %q, want %q", body, tc.req["body"])
			}
			gbDiff(t, "keys of the create request", []string{"body", "position"}, sortedKeys(keys))
			want := gbComment{
				id: int64(7001 + i), path: "a.ts", line: gbInt(tc.line), orig: gbInt(tc.line), side: tc.side,
				body: tc.req["body"].(string), user: "ada", created: gbNew,
			}
			gbDiff(t, "the created comment", want.json(e.web), resp.JSON(t))
		})
	}
	lines := logLines(t, e.s.logs, "gitlab comment")
	if len(lines) != 3 {
		t.Fatalf("%d \"gitlab comment\" line(s), want exactly 3 (one per post)", len(lines))
	}
	for _, l := range lines {
		if l["level"] != "INFO" || l["operation"] != "post" || l["code"] != "ok" || l["pr_id"] != e.pr {
			t.Errorf("log line %v, want INFO, operation post, code ok, the PR id", l)
		}
	}
	for _, text := range []string{"unchanged", "removed", "added"} {
		for _, l := range lines {
			if strings.Contains(fmt.Sprint(l), text) {
				t.Errorf("log line %v holds the comment body %q", l, text)
			}
		}
	}
}

func sortedKeys(k []string) []string {
	out := slices.Clone(k)
	slices.Sort(out)
	return out
}

// ---- AC6 ----

// AC6 (FR8, FR34, FR35, EC8): a renamed file is posted with old_path and new_path; the detail fetch
// stored the diff head and the previous paths, and GET /pulls/:id shows neither.
func TestGbComments_AC6_RenamedFileUsesBothPaths(t *testing.T) {
	e := gbStart(t, gbOpts{files: []gbFile{{"old/a.ts", "new/a.ts", gbPatchA}, {"keep.ts", "keep.ts", gbPatchB}}})

	if got := scalar[string](t, e.db, `SELECT COALESCE(diff_head, '') FROM pull_requests WHERE id = ?`, e.pr); got != gbHeadSHA {
		t.Errorf("stored diff_head after the detail fetch = %q, want the diff_refs head %q", got, gbHeadSHA)
	}
	for path, want := range map[string]string{"new/a.ts": "old/a.ts", "keep.ts": ""} {
		if got := scalar[string](t, e.db, `SELECT COALESCE(previous_path, '') FROM pr_files WHERE pr_id = ? AND path = ?`, e.pr, path); got != want {
			t.Errorf("stored previous_path of %s = %q, want %q", path, got, want)
		}
	}
	body := string(e.detail().Body)
	for _, leak := range []string{"diff_head", "previous_path", "PreviousPath", "DiffHead"} {
		if strings.Contains(body, leak) {
			t.Errorf("GET /pulls/:id shows %q: %s", leak, body)
		}
	}

	n := e.lab.count()
	resp := e.post(gbAt("new/a.ts", 12, "renamed"))
	if resp.Status != http.StatusOK {
		t.Fatalf("POST on the renamed file = %d %s, want 200", resp.Status, resp.Body)
	}
	_, pos, _ := gbSentCreate(t, e.lab.creates(n)[0])
	gbDiff(t, "position of the renamed file", map[string]any{
		"position_type": "text", "base_sha": gbBaseSHA, "start_sha": gbStartSHA, "head_sha": gbHeadSHA,
		"old_path": "old/a.ts", "new_path": "new/a.ts", "old_line": float64(10), "new_line": float64(12),
	}, pos)
}

// ---- AC7 ----

// AC7 (FR6, EC5): a line between hunks, a file without a stored patch and a path that is not in the
// diff each answer 400 gitlab_comment_failed with their reason and send no request at all.
func TestGbComments_AC7_LineAndFileOutsideTheDiffSendNothing(t *testing.T) {
	e := gbStart(t, gbOpts{files: []gbFile{{"a.ts", "a.ts", gbPatchA}, {"b.ts", "b.ts", gbPatchB}, {"logo.png", "logo.png", ""}}})
	for _, tc := range []struct {
		name, path string
		line       int
		reason     string
		message    string
	}{
		{"line between hunks", "b.ts", 20, "line_not_in_diff", "Line 20 (RIGHT) is not part of the diff of b.ts."},
		{"file without a patch", "logo.png", 1, "line_not_in_diff", "Line 1 (RIGHT) is not part of the diff of logo.png."},
		{"path not in the diff", "nope.ts", 1, "file_not_in_diff", "nope.ts is not part of this merge request's diff."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n := e.lab.count()
			resp := e.post(gbAt(tc.path, tc.line, "x"))
			gbWantFailure(t, "POST "+tc.name, resp, "gitlab_comment_failed", tc.reason)
			if _, msg, _ := gbErr(t, resp); msg != tc.message {
				t.Errorf("message = %q, want %q", msg, tc.message)
			}
			if got := e.lab.since(n); len(got) != 0 {
				t.Errorf("the refused post sent %v, want no GitLab request", gbSeq(got))
			}
		})
	}
}

// ---- AC8 ----

// AC8 (FR7, EC6, EC7): no diff_refs and no versions is diff_not_ready; no diff_refs but a newest
// version at the diff head posts with the version's SHAs; a GitLab head that differs from the diff
// head, or no stored diff head, is diff_changed; none of the refusals sends a create.
func TestGbComments_AC8_DiffRefsVersionsAndChangedHead(t *testing.T) {
	e := gbStart(t, gbOpts{})
	post := func() response { return e.post(gbAt("a.ts", 12, "x")) }

	t.Run("no refs and no versions", func(t *testing.T) {
		e.lab.set(func(m *gbMR) { m.refs, m.versions = nil, nil })
		n := e.lab.count()
		gbWantFailure(t, "POST", post(), "gitlab_comment_failed", "diff_not_ready")
		gbDiff(t, "requests", []string{"GET " + gbMRPath, "GET " + gbMRPath + "/versions?page=1&per_page=1"}, gbSeq(e.lab.since(n)))
	})
	t.Run("the newest version at the diff head", func(t *testing.T) {
		e.lab.set(func(m *gbMR) { m.refs, m.versions = nil, []gbRefs{{gbOtherSHA, gbOldSHA, gbHeadSHA}} })
		n := e.lab.count()
		if resp := post(); resp.Status != http.StatusOK {
			t.Fatalf("POST with the version's refs = %d %s, want 200", resp.Status, resp.Body)
		}
		creates := e.lab.creates(n)
		if len(creates) != 1 {
			t.Fatalf("%d create(s), want 1", len(creates))
		}
		_, pos, _ := gbSentCreate(t, creates[0])
		if pos["base_sha"] != gbOtherSHA || pos["start_sha"] != gbOldSHA || pos["head_sha"] != gbHeadSHA {
			t.Errorf("position SHAs = %v %v %v, want the version's %s %s %s", pos["base_sha"], pos["start_sha"], pos["head_sha"], gbOtherSHA, gbOldSHA, gbHeadSHA)
		}
	})
	t.Run("GitLab head differs from the diff head", func(t *testing.T) {
		e.lab.set(func(m *gbMR) { m.refs, m.versions = &gbRefs{gbBaseSHA, gbStartSHA, gbOldSHA}, nil })
		n := e.lab.count()
		gbWantFailure(t, "POST", post(), "gitlab_comment_failed", "diff_changed")
		if got := e.lab.creates(n); len(got) != 0 {
			t.Errorf("diff_changed sent %v, want no create", gbSeq(got))
		}
	})
	t.Run("no stored diff head", func(t *testing.T) {
		e.lab.set(func(m *gbMR) { m.refs = &gbRefs{gbBaseSHA, gbStartSHA, gbHeadSHA} })
		e.exec(`UPDATE pull_requests SET diff_head = NULL WHERE id = ?`, e.pr)
		n := e.lab.count()
		gbWantFailure(t, "POST", post(), "gitlab_comment_failed", "diff_changed")
		if got := e.lab.creates(n); len(got) != 0 {
			t.Errorf("a missing diff head sent %v, want no create", gbSeq(got))
		}
	})
}

// ---- AC9 ----

// AC9 (FR9, EC13, NFR5): a reply to a note on discussion page 4 reads 4 pages and posts once to
// that discussion's notes with only the body; the answer is a reply to the discussion's first note.
// A deleted note id and a system note id answer reply_target_not_found after at most 10 reads and
// no post.
func TestGbComments_AC9_ReplyFindsTheDiscussionOnLaterPages(t *testing.T) {
	e := gbStart(t, gbOpts{})
	const reply = 9001
	var many []map[string]any
	for i := range 330 {
		switch i {
		case 0: // a discussion that holds a system note
			many = append(many, gbDisc(gbDiscID(i), gbSystemNote(8888)))
		case 315: // page 4: the root and a reply to it
			many = append(many, gbDiffThread(i, 2315, "pkg/x.ts", nil, gbInt(42), gbHeadSHA, "ann",
				gbNote(reply, "DiscussionNote", "earlier reply", "bob", nil, gbCreated)))
		default:
			many = append(many, gbDiffThread(i, int64(2000+i), "a.ts", nil, gbInt(1), gbHeadSHA, "ann"))
		}
	}
	e.lab.set(func(m *gbMR) { m.discussions = many })

	t.Run("a note on page 4", func(t *testing.T) {
		n := e.lab.count()
		resp := e.post(map[string]any{"path": "pkg/x.ts", "line": 42, "body": "thanks @ann", "in_reply_to": reply})
		if resp.Status != http.StatusOK {
			t.Fatalf("POST reply = %d %s, want 200", resp.Status, resp.Body)
		}
		var wantSeq []string
		for p := 1; p <= 4; p++ {
			wantSeq = append(wantSeq, fmt.Sprintf("GET %s/discussions?page=%d&per_page=100", gbMRPath, p))
		}
		wantSeq = append(wantSeq, "POST "+gbMRPath+"/discussions/"+gbDiscID(315)+"/notes")
		reqs := e.lab.since(n)
		gbDiff(t, "GitLab requests of the reply", wantSeq, gbSeq(reqs))
		var sent map[string]any
		if err := json.Unmarshal(reqs[4].Body, &sent); err != nil {
			t.Fatal(err)
		}
		gbDiff(t, "body sent to the notes route", map[string]any{"body": "thanks @\u200bann"}, sent)
		want := gbComment{
			id: 7001, path: "pkg/x.ts", line: gbInt(42), orig: gbInt(42), side: "RIGHT", body: "thanks @\u200bann",
			user: "ada", created: gbNew, reply: gbID(2315),
		}
		gbDiff(t, "the created reply", want.json(e.web), resp.JSON(t))
	})
	t.Run("a system note", func(t *testing.T) {
		n := e.lab.count()
		gbWantFailure(t, "POST reply to a system note", e.post(map[string]any{"path": "a.ts", "line": 1, "body": "x", "in_reply_to": 8888}),
			"gitlab_comment_failed", "reply_target_not_found")
		if got := e.lab.creates(n); len(got) != 0 {
			t.Errorf("a reply to a system note sent %v, want no create", gbSeq(got))
		}
	})
	t.Run("a deleted note", func(t *testing.T) {
		huge := make([]map[string]any, 1500)
		for i := range huge {
			huge[i] = gbDiffThread(i, int64(20000+i), "a.ts", nil, gbInt(1), gbHeadSHA, "ann")
		}
		e.lab.set(func(m *gbMR) { m.discussions = huge })
		n := e.lab.count()
		gbWantFailure(t, "POST reply to a deleted note", e.post(map[string]any{"path": "a.ts", "line": 1, "body": "x", "in_reply_to": 424242}),
			"gitlab_comment_failed", "reply_target_not_found")
		reqs := e.lab.since(n)
		if len(reqs) != 10 {
			t.Errorf("the search sent %d request(s), want 10 (the page limit): %v", len(reqs), gbSeq(reqs))
		}
		if got := e.lab.creates(n); len(got) != 0 {
			t.Errorf("a reply to a deleted note sent %v, want no create", gbSeq(got))
		}
	})
}

// ---- AC11 ----

// AC11 (FR5, EC16): a repository that is not connected answers 422 repo_host_not_connected with
// {host, repo_id}, a connection without a token 400 gitlab_unavailable with the FR5 message; neither
// sends a request, and an invalid body is a 422 validation_error first.
func TestGbComments_AC11_NotConnectedAndNoTokenSendNothing(t *testing.T) {
	t.Run("not connected", func(t *testing.T) {
		e := gbStart(t, gbOpts{noConnection: true, noDetail: true})
		resp := e.post(gbAt("a.ts", 12, "x"))
		code, _, details := gbErr(t, resp)
		if resp.Status != http.StatusUnprocessableEntity || code != "repo_host_not_connected" {
			t.Errorf("POST = %d %s, want 422 repo_host_not_connected", resp.Status, resp.Body)
		}
		gbDiff(t, "details", map[string]any{"host": e.lab.host, "repo_id": e.repo}, details)
		invalid := e.post(map[string]any{"line": 12, "body": "x"})
		if code, _, _ := gbErr(t, invalid); invalid.Status != http.StatusUnprocessableEntity || code != "validation_error" {
			t.Errorf("POST without path = %d %s, want 422 validation_error", invalid.Status, invalid.Body)
		}
		if n := e.lab.count(); n != 0 {
			t.Errorf("%d GitLab request(s), want none", n)
		}
	})
	t.Run("no token", func(t *testing.T) {
		e := gbStart(t, gbOpts{noToken: true, noDetail: true})
		resp := e.post(gbAt("a.ts", 12, "x"))
		code, msg, _ := gbErr(t, resp)
		want := "Connect a token for " + e.lab.host + " in Settings → GitLab Integration to post comments."
		if resp.Status != http.StatusBadRequest || code != "gitlab_unavailable" || msg != want {
			t.Errorf("POST = %d %s, want 400 gitlab_unavailable %q", resp.Status, resp.Body, want)
		}
		invalid := e.post(map[string]any{"path": "a.ts", "line": 0, "body": "x"})
		if code, _, _ := gbErr(t, invalid); invalid.Status != http.StatusUnprocessableEntity || code != "validation_error" {
			t.Errorf("POST with line 0 = %d %s, want 422 validation_error", invalid.Status, invalid.Body)
		}
		if n := e.lab.count(); n != 0 {
			t.Errorf("%d GitLab request(s), want none", n)
		}
	})
}

// ---- AC13 ----

// AC13 (FR12, FR13, NFR2, NFR7): a finding body, a free-text comment and a reply are sent to GitLab
// after the note body rules (quick actions and mentions neutralised, HTML escaped, the finding's
// <sub> line and marker intact); the same inputs on a GitHub PR reach GitHub unchanged.
func TestGbComments_AC13_BodiesPassTheNoteRulesOnlyForGitLab(t *testing.T) {
	e := gbStart(t, gbOpts{githubToken: true})
	e.lab.set(func(m *gbMR) {
		m.discussions = []map[string]any{gbDiffThread(1, 600, "a.ts", nil, gbInt(3), gbHeadSHA, "ann")}
	})
	e.exec(`INSERT INTO repos (id, workspace_id, owner, name, full_name) VALUES (?, ?, 'o', 'r', 'o/r')`, lbID(idRepo, 2), e.ws)
	e.exec(`INSERT INTO pull_requests (workspace_id, repo_id, number, title, author, branch, base, head_sha,
			additions, deletions, files_count, status, opened_at, updated_at, draft)
		VALUES (?, ?, 7, 'GitHub PR', 'octo', 'feat', 'main', 'abc123', 0, 0, 0, 'open', now(), now(), false)`, e.ws, lbID(idRepo, 2))
	ghPR := scalar[string](t, e.db, `SELECT id::text FROM pull_requests WHERE repo_id = ?`, lbID(idRepo, 2))

	const (
		finding = "/close @x <i>\n\nSee `@code <tag>` and <b>x</b>\n\n<sub>DevDigest · 85% confidence</sub>\n\n<!-- devdigest-finding:abc123 -->"
		free    = "/approve\n@ann see <b>x</b>"
		replyTx = "- /merge"
	)
	wantGitLab := map[string]string{
		finding: "\u200b/close @\u200bx &lt;i&gt;\n\nSee `@\u200bcode <\u200btag>` and &lt;b&gt;x&lt;/b&gt;\n\n<sub>DevDigest · 85% confidence</sub>\n\n<!-- devdigest-finding:abc123 -->",
		free:    "\u200b/approve\n@\u200bann see &lt;b&gt;x&lt;/b&gt;",
		replyTx: "- \u200b/merge",
	}
	requests := map[string]map[string]any{
		finding: gbAt("a.ts", 12, finding),
		free:    gbAt("a.ts", 3, free),
		replyTx: {"path": "a.ts", "line": 3, "body": replyTx, "in_reply_to": 600},
	}
	order := []string{finding, free, replyTx}

	for _, in := range order {
		n := e.lab.count()
		resp := e.post(requests[in])
		if resp.Status != http.StatusOK {
			t.Fatalf("GitLab POST of %q = %d %s, want 200", in, resp.Status, resp.Body)
		}
		creates := e.lab.creates(n)
		if len(creates) != 1 {
			t.Fatalf("GitLab POST of %q sent %d create(s), want 1", in, len(creates))
		}
		var sent struct {
			Body string `json:"body"`
		}
		if err := json.Unmarshal(creates[0].Body, &sent); err != nil {
			t.Fatal(err)
		}
		if sent.Body != wantGitLab[in] {
			t.Errorf("GitLab received %q for %q, want %q", sent.Body, in, wantGitLab[in])
		}
		if got := resp.Obj(t)["body"]; got != wantGitLab[in] {
			t.Errorf("the response body = %q, want the sanitised %q", got, wantGitLab[in])
		}
	}

	// The shared GitHub stub answers a new comment; a reply goes to .../comments/<id>/replies.
	e.s.gh.setOverride(func(w http.ResponseWriter, r *http.Request) bool {
		if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/replies") {
			return false
		}
		gbJSON(w, http.StatusCreated, map[string]any{"id": 98, "path": "a.ts", "line": 3, "original_line": 3, "side": "RIGHT",
			"body": "x", "user": map[string]string{"login": "octo"}, "created_at": gbCreated,
			"html_url": "https://github.com/o/r/pull/7#r98", "in_reply_to_id": 1})
		return true
	})
	for _, in := range order {
		req := map[string]any{"path": "a.ts", "line": 3, "side": "RIGHT", "body": in}
		if in == replyTx {
			req["in_reply_to"] = 1
		}
		if resp := e.s.post("/pulls/"+ghPR+"/comments", req); resp.Status != http.StatusOK {
			t.Fatalf("GitHub POST of %q = %d %s, want 200", in, resp.Status, resp.Body)
		}
	}
	var got []string
	for _, r := range e.s.gh.requests() {
		if r.Method != http.MethodPost || !strings.Contains(r.Path, "/pulls/7/comments") {
			continue
		}
		var sent struct {
			Body string `json:"body"`
		}
		if err := json.Unmarshal(r.Body, &sent); err != nil {
			t.Fatalf("GitHub request body %q: %v", r.Body, err)
		}
		got = append(got, sent.Body)
	}
	gbDiff(t, "bodies GitHub received (unchanged)", order, got)
}

// ---- AC14 ----

// AC14 (FR5, EC14): the length limit applies to the body after the rules: 999,990 characters that
// grow past 1,000,000 are 400 body_too_long and send nothing, while exactly 1,000,000 characters go
// through intact. The 50 ms processing bound (NFR8) is the timing test in pulls/app, which cannot
// run under -race; a stub round trip would not isolate it.
func TestGbComments_AC14_BodyLimitAfterTheRules(t *testing.T) {
	e := gbStart(t, gbOpts{})

	// 11 mentions each gain a U+200B: 999,990 + 11 = 1,000,001 characters.
	tooLong := strings.Repeat("a", 999_979) + strings.Repeat("@", 11)
	if got := len([]rune(tooLong)); got != 999_990 {
		t.Fatalf("fixture body has %d characters, want 999,990", got)
	}
	n := e.lab.count()
	resp := e.post(gbAt("a.ts", 12, tooLong))
	gbWantFailure(t, "POST of a body that exceeds the limit after the rules", resp, "gitlab_comment_failed", "body_too_long")
	if got := e.lab.since(n); len(got) != 0 {
		t.Errorf("body_too_long sent %v, want no request", gbSeq(got))
	}

	exact := strings.Repeat("a", 1_000_000)
	n = e.lab.count()
	if resp := e.post(gbAt("a.ts", 12, exact)); resp.Status != http.StatusOK {
		t.Fatalf("POST of exactly 1,000,000 characters = %d, want 200", resp.Status)
	}
	creates := e.lab.creates(n)
	if len(creates) != 1 {
		t.Fatalf("%d create(s), want 1", len(creates))
	}
	if body, _, _ := gbSentCreate(t, creates[0]); body != exact {
		t.Errorf("GitLab received %d characters, want the 1,000,000 sent", len([]rune(body)))
	}
}

// ---- AC15 ----

// AC15 (EC17, FR8): a merged merge request still takes comments.
func TestGbComments_AC15_MergedMergeRequestAcceptsComments(t *testing.T) {
	e := gbStart(t, gbOpts{merged: true})
	if got := scalar[string](t, e.db, `SELECT status FROM pull_requests WHERE id = ?`, e.pr); got != "merged" {
		t.Fatalf("stored status = %q, want merged", got)
	}
	n := e.lab.count()
	resp := e.post(gbAt("a.ts", 12, "late remark"))
	if resp.Status != http.StatusOK {
		t.Fatalf("POST on a merged merge request = %d %s, want 200", resp.Status, resp.Body)
	}
	if got := e.lab.creates(n); len(got) != 1 {
		t.Errorf("%d create(s), want 1", len(got))
	}
}

// ---- AC30 ----

// AC30 (FR34, FR35): rows from before the migration have no diff head and no previous path; the
// down and up of 0011 keeps GitHub and GitLab data and empties the two values; a post before the
// next detail fetch answers diff_changed, the fetch refills them and the post then goes through.
func TestGbComments_AC30_DiffPositionsMigrationRoundTrip(t *testing.T) {
	e := gbStart(t, gbOpts{noDetail: true, files: []gbFile{{"old/a.ts", "new/a.ts", gbPatchA}}})
	e.exec(`INSERT INTO repos (id, workspace_id, owner, name, full_name) VALUES (?, ?, 'o', 'r', 'o/r')`, lbID(idRepo, 2), e.ws)
	e.exec(`INSERT INTO pull_requests (workspace_id, repo_id, number, title, author, branch, base, head_sha,
			additions, deletions, files_count, status, opened_at, updated_at, draft)
		VALUES (?, ?, 7, 'GitHub PR', 'octo', 'feat', 'main', 'abc123', 0, 0, 0, 'open', now(), now(), false)`, e.ws, lbID(idRepo, 2))
	ghPR := scalar[string](t, e.db, `SELECT id::text FROM pull_requests WHERE repo_id = ?`, lbID(idRepo, 2))
	e.exec(`INSERT INTO pr_files (pr_id, path, additions, deletions, patch) VALUES (?, 'g.ts', 1, 0, ?)`, ghPR, "@@ -0,0 +1 @@\n+x")

	nullCounts := func() (heads, prev int) {
		return scalar[int](t, e.db, `SELECT count(*) FROM pull_requests WHERE diff_head IS NOT NULL`),
			scalar[int](t, e.db, `SELECT count(*) FROM pr_files WHERE previous_path IS NOT NULL`)
	}
	if h, p := nullCounts(); h != 0 || p != 0 {
		t.Fatalf("rows before any detail fetch hold %d diff head(s) and %d previous path(s), want none", h, p)
	}

	e.detail()
	if h, p := nullCounts(); h != 1 || p != 1 {
		t.Fatalf("after the detail fetch: %d diff head(s) and %d previous path(s), want 1 and 1", h, p)
	}

	// Down to before 0011 and up again, with the server running.
	sqlDB, err := e.db.DB()
	if err != nil {
		t.Fatal(err)
	}
	m, err := migrate.New(sqlDB, migrations.FS, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	rolledBack := 0
	for {
		v, ok, err := m.Down(t.Context())
		if err != nil || !ok {
			t.Fatalf("Down() = %d, %v, %v, want a rollback", v, ok, err)
		}
		rolledBack++
		if v == 11 {
			break
		}
	}
	if n, err := m.Up(t.Context()); err != nil || n != rolledBack {
		t.Fatalf("Up() = %d, %v, want %d, nil", n, err, rolledBack)
	}

	if n := scalar[int](t, e.db, `SELECT count(*) FROM repos WHERE id IN (?, ?)`, e.repo, lbID(idRepo, 2)); n != 2 {
		t.Errorf("repositories after down and up = %d, want the GitLab and the GitHub one", n)
	}
	if n := scalar[int](t, e.db, `SELECT count(*) FROM pull_requests WHERE id IN (?, ?)`, e.pr, ghPR); n != 2 {
		t.Errorf("pull requests after down and up = %d, want 2", n)
	}
	if n := scalar[int](t, e.db, `SELECT count(*) FROM pr_files WHERE pr_id IN (?, ?)`, e.pr, ghPR); n != 2 {
		t.Errorf("stored files after down and up = %d, want 2", n)
	}
	if h, p := nullCounts(); h != 0 || p != 0 {
		t.Errorf("after down and up: %d diff head(s) and %d previous path(s), want none", h, p)
	}

	n := e.lab.count()
	gbWantFailure(t, "POST before the refill", e.post(gbAt("new/a.ts", 12, "x")), "gitlab_comment_failed", "diff_changed")
	if got := e.lab.creates(n); len(got) != 0 {
		t.Errorf("diff_changed sent %v, want no create", gbSeq(got))
	}

	e.detail()
	if h, p := nullCounts(); h != 1 || p != 1 {
		t.Errorf("after the next detail fetch: %d diff head(s) and %d previous path(s), want 1 and 1", h, p)
	}
	n = e.lab.count()
	resp := e.post(gbAt("new/a.ts", 12, "x"))
	if resp.Status != http.StatusOK {
		t.Fatalf("POST after the refill = %d %s, want 200", resp.Status, resp.Body)
	}
	creates := e.lab.creates(n)
	if len(creates) != 1 {
		t.Fatalf("%d create(s) after the refill, want 1", len(creates))
	}
	if _, pos, _ := gbSentCreate(t, creates[0]); pos["old_path"] != "old/a.ts" {
		t.Errorf("old_path after the refill = %v, want old/a.ts", pos["old_path"])
	}
}

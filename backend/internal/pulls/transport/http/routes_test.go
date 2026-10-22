package pullshttp_test

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"gorm.io/gorm"

	"dev-digest-go/backend/internal/platform/github"
	"dev-digest-go/backend/internal/platform/httpx"
	"dev-digest-go/backend/internal/platform/postgres/pgtest"
	"dev-digest-go/backend/internal/platform/retry"
	githubadapter "dev-digest-go/backend/internal/pulls/adapters/github"
	pgstore "dev-digest-go/backend/internal/pulls/adapters/postgres"
	"dev-digest-go/backend/internal/pulls/app"
	pullshttp "dev-digest-go/backend/internal/pulls/transport/http"
	"dev-digest-go/backend/internal/seed"
	seedpg "dev-digest-go/backend/internal/seed/adapters/postgres"
	"dev-digest-go/backend/internal/tenancy"
	tenancypg "dev-digest-go/backend/internal/tenancy/adapters/postgres"
)

func TestMain(m *testing.M) { os.Exit(pgtest.Main(m)) }

const unknownID = "00000000-0000-4000-8000-000000000000"

// ---- GitHub stub ----

type ghRequest struct {
	Method string
	Path   string
	Query  url.Values
	Body   []byte
}

// ghStub is an httptest GitHub API. Routes are "METHOD /path" without the query; anything else
// answers 404 {"message":"Not Found"}.
type ghStub struct {
	srv    *httptest.Server
	mu     sync.Mutex
	routes map[string]http.HandlerFunc
	reqs   []ghRequest
	stop   chan struct{} // released at cleanup so hanging handlers return
}

func newStub(t *testing.T) *ghStub {
	t.Helper()
	s := &ghStub{routes: map[string]http.HandlerFunc{}, stop: make(chan struct{})}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		s.mu.Lock()
		s.reqs = append(s.reqs, ghRequest{r.Method, r.URL.Path, r.URL.Query(), body})
		h := s.routes[r.Method+" "+r.URL.Path]
		s.mu.Unlock()
		if h == nil {
			writeJSON(w, http.StatusNotFound, map[string]string{"message": "Not Found"})
			return
		}
		h(w, r)
	}))
	t.Cleanup(func() { close(s.stop); s.srv.Close() })
	return s
}

func (s *ghStub) handle(route string, h http.HandlerFunc) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.routes[route] = h
}

// count returns the requests whose "METHOD path" matches the pattern.
func (s *ghStub) count(pattern string) int { return len(s.requests(pattern)) }

func (s *ghStub) requests(pattern string) []ghRequest {
	re := regexp.MustCompile("^(?:" + pattern + ")$")
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []ghRequest
	for _, r := range s.reqs {
		if re.MatchString(r.Method + " " + r.Path) {
			out = append(out, r)
		}
	}
	return out
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func jsonHandler(status int, v any) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, status, v) }
}

// paged serves items honoring page and per_page.
func paged(items []map[string]any) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		per, _ := strconv.Atoi(r.URL.Query().Get("per_page"))
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		if per <= 0 {
			per = 30
		}
		if page <= 0 {
			page = 1
		}
		lo, hi := min((page-1)*per, len(items)), min(page*per, len(items))
		writeJSON(w, http.StatusOK, items[lo:hi])
	}
}

const stubRepo = "/repos/acme/payments-api"

func ghPull(n int, title, state, updated string) map[string]any {
	return map[string]any{
		"number": n, "title": title, "user": map[string]string{"login": "gh-user"},
		"head": map[string]string{"ref": "feat/x", "sha": fmt.Sprintf("sha%d", n)}, "base": map[string]string{"ref": "main"},
		"state": state, "merged_at": nil, "created_at": "2026-10-01T00:00:00Z", "updated_at": updated,
		"body": nil, "additions": 7, "deletions": 3, "changed_files": 2,
	}
}

// stubPullDetail serves /pulls/N, /files and /commits for one PR.
func (s *ghStub) stubPullDetail(pull map[string]any, files, commits []map[string]any) {
	base := fmt.Sprintf("%s/pulls/%v", stubRepo, pull["number"])
	s.handle("GET "+base, jsonHandler(http.StatusOK, pull))
	s.handle("GET "+base+"/files", paged(files))
	s.handle("GET "+base+"/commits", paged(commits))
}

func ghFile(path string, add, del int, patch any) map[string]any {
	return map[string]any{"filename": path, "additions": add, "deletions": del, "patch": patch}
}

func ghCommit(sha string) map[string]any {
	return map[string]any{
		"sha":    sha,
		"commit": map[string]any{"message": "msg " + sha, "author": map[string]any{"name": "Dev", "date": "2026-10-02T03:04:05Z"}},
		"author": map[string]string{"login": "dev"},
	}
}

// ---- environment ----

type env struct {
	t      *testing.T
	db     *gorm.DB
	e      http.Handler
	stub   *ghStub
	token  string // read by the GitHub client on every call; set before sending requests
	wsID   string
	repoID string
	prID   string // seeded PR #482
}

// newEnv wires the real stack (Postgres, seed, use cases, routes) against the GitHub stub.
func newEnv(t *testing.T, token string) *env {
	t.Helper()
	d := pgtest.Migrated(t)
	log := slog.New(slog.DiscardHandler)
	res, err := seed.New(seedpg.NewStore(d.DB), log).Run(t.Context())
	if err != nil {
		t.Fatalf("seed error = %v", err)
	}
	ev := &env{t: t, db: d.DB, stub: newStub(t), token: token, wsID: res.WorkspaceID}
	scan(t, d.DB, &ev.repoID, `SELECT id FROM repos WHERE full_name = 'acme/payments-api'`)
	scan(t, d.DB, &ev.prID, `SELECT id FROM pull_requests WHERE number = 482`)

	client := github.New(github.Options{
		Token:   func() string { return ev.token },
		BaseURL: ev.stub.srv.URL,
		Timeout: 300 * time.Millisecond,
		Retry: &retry.Options{
			Retries: 3, BaseDelay: time.Millisecond, MaxDelay: time.Millisecond,
			Jitter: func(time.Duration) time.Duration { return 0 },
		},
	})
	svc := app.NewService(pgstore.NewStore(d.DB), githubadapter.New(client), log, nil)
	e := httpx.New(httpx.Options{Log: log, CORSOrigins: []string{uiOrigin}})
	pullshttp.AddRoutes(e, svc, tenancy.NewResolver(tenancypg.NewStore(d.DB)))
	ev.e = e
	return ev
}

func scan(t *testing.T, db *gorm.DB, dest any, query string, args ...any) {
	t.Helper()
	if err := db.Raw(query, args...).Scan(dest).Error; err != nil {
		t.Fatalf("query %q: %v", query, err)
	}
}

func (ev *env) exec(query string, args ...any) {
	ev.t.Helper()
	if err := ev.db.Exec(query, args...).Error; err != nil {
		ev.t.Fatalf("exec %q: %v", query, err)
	}
}

// dropDemoReview removes the PR #482 demo review (and its run) the seed writes, so a test
// starts from a pull request that has no review.
func (ev *env) dropDemoReview() {
	ev.t.Helper()
	ev.exec(`DELETE FROM reviews WHERE pr_id = ?`, ev.prID) // findings go with their review
	ev.exec(`DELETE FROM agent_runs WHERE pr_id = ?`, ev.prID)
}

// insertPR stores a PR of the demo repo and returns its id.
func (ev *env) insertPR(number int, title, author string, updated *time.Time, additions, deletions, files int) string {
	ev.t.Helper()
	var id string
	scan(ev.t, ev.db, &id, `INSERT INTO pull_requests
		(workspace_id, repo_id, number, title, author, branch, base, head_sha, additions, deletions, files_count, status, updated_at)
		VALUES (?, ?, ?, ?, ?, 'feat', 'main', ?, ?, ?, ?, 'open', ?) RETURNING id`,
		ev.wsID, ev.repoID, number, title, author, fmt.Sprintf("sha%d", number), additions, deletions, files, updated)
	return id
}

type response struct {
	Status int
	Raw    []byte
}

func (ev *env) do(method, path, body string, headers ...string) response {
	ev.t.Helper()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, rd)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	rec := httptest.NewRecorder()
	ev.e.ServeHTTP(rec, req)
	return response{rec.Code, rec.Body.Bytes()}
}

func (r response) decode(t *testing.T, dest any) {
	t.Helper()
	if err := json.Unmarshal(r.Raw, dest); err != nil {
		t.Fatalf("response %d is not the expected JSON: %v\n%s", r.Status, err, r.Raw)
	}
}

func (r response) object(t *testing.T) map[string]any {
	t.Helper()
	var m map[string]any
	r.decode(t, &m)
	return m
}

func (r response) list(t *testing.T) []map[string]any {
	t.Helper()
	var l []map[string]any
	r.decode(t, &l)
	return l
}

// wantError checks the status and the error envelope code and message.
func (r response) wantError(t *testing.T, status int, code, message string) {
	t.Helper()
	var body struct {
		Error struct{ Code, Message string }
	}
	r.decode(t, &body)
	if r.Status != status || body.Error.Code != code || body.Error.Message != message {
		t.Errorf("response = %d %s %q, want %d %s %q\n%s", r.Status, body.Error.Code, body.Error.Message, status, code, message, r.Raw)
	}
}

func byNumber(list []map[string]any, n int) map[string]any {
	for _, m := range list {
		if int(m["number"].(float64)) == n {
			return m
		}
	}
	return nil
}

func ts(day, hour int) *time.Time {
	t := time.Date(2026, 10, day, hour, 0, 0, 0, time.UTC)
	return &t
}

// ---- validation and not found ----

func TestRoutes_nonUUIDID(t *testing.T) {
	ev := newEnv(t, "")
	routes := []struct{ method, path, body string }{
		{"GET", "/repos/abc/pulls", ""},
		{"GET", "/pulls/abc", ""},
		{"GET", "/pulls/abc/comments", ""},
		{"POST", "/pulls/abc/comments", `{"path":"a","line":1,"body":"x"}`},
		{"POST", "/repos/abc/poll", ""},
	}
	for _, rt := range routes {
		t.Run(rt.method+" "+rt.path, func(t *testing.T) {
			resp := ev.do(rt.method, rt.path, rt.body)
			var body struct {
				Error struct {
					Code    string
					Message string
					Details []struct{ InstancePath string }
				}
			}
			resp.decode(t, &body)
			if resp.Status != 422 || body.Error.Code != "validation_error" || body.Error.Message != "Request validation failed" ||
				len(body.Error.Details) == 0 || body.Error.Details[0].InstancePath != "/id" {
				t.Errorf("%s %s = %d %s\n%s, want 422 validation_error with details[0].instancePath /id", rt.method, rt.path, resp.Status, body.Error.Code, resp.Raw)
			}
		})
	}
}

func TestRoutes_unknownIDs(t *testing.T) {
	ev := newEnv(t, "tok")
	validBody := `{"path":"a.ts","line":1,"body":"x"}`
	repoRoutes := []struct{ method, path string }{{"GET", "/repos/" + unknownID + "/pulls"}, {"POST", "/repos/" + unknownID + "/poll"}}
	for _, rt := range repoRoutes {
		ev.do(rt.method, rt.path, "").wantError(t, 404, "not_found", "Repo not found")
	}
	ev.do("GET", "/pulls/"+unknownID, "").wantError(t, 404, "not_found", "Pull request not found")
	ev.do("GET", "/pulls/"+unknownID+"/comments", "").wantError(t, 404, "not_found", "Pull request not found")
	ev.do("POST", "/pulls/"+unknownID+"/comments", validBody).wantError(t, 404, "not_found", "Pull request not found")
	if n := len(ev.stub.requests(".*")); n != 0 {
		t.Errorf("GitHub requests for unknown ids = %d, want 0", n)
	}
}

func TestRoutes_otherWorkspaceIsUnknown(t *testing.T) {
	ev := newEnv(t, "tok")
	var otherWS, otherRepo string
	scan(t, ev.db, &otherWS, `INSERT INTO workspaces (name) VALUES ('other') RETURNING id`)
	scan(t, ev.db, &otherRepo, `INSERT INTO repos (workspace_id, owner, name, full_name) VALUES (?, 'o', 'r', 'o/r') RETURNING id`, otherWS)
	ev.do("GET", "/repos/"+otherRepo+"/pulls", "").wantError(t, 404, "not_found", "Repo not found")
}

func TestPostComment_validation(t *testing.T) {
	ev := newEnv(t, "tok")
	bodies := []string{
		`{"path":"","line":1,"body":"x"}`,
		`{"path":"a","line":0,"body":"x"}`,
		`{"path":"a","line":1}`,
	}
	for _, b := range bodies {
		resp := ev.do("POST", "/pulls/"+ev.prID+"/comments", b)
		resp.wantError(t, 422, "validation_error", "Request validation failed")
	}
	// A body-less POST is a validation failure too.
	ev.do("POST", "/pulls/"+ev.prID+"/comments", "").wantError(t, 422, "validation_error", "Request validation failed")
	if n := len(ev.stub.requests(".*")); n != 0 {
		t.Errorf("GitHub requests for invalid comments = %d, want 0", n)
	}
}

// ---- poll ----

func TestPoll(t *testing.T) {
	t.Run("syncs and marks polled", func(t *testing.T) {
		ev := newEnv(t, "tok")
		ev.stub.handle("GET "+stubRepo+"/pulls", jsonHandler(200, []map[string]any{
			ghPull(1, "one", "open", "2026-10-03T00:00:00Z"), ghPull(2, "two", "open", "2026-10-02T00:00:00Z"), ghPull(3, "three", "closed", "2026-10-01T00:00:00Z"),
		}))
		resp := ev.do("POST", "/repos/"+ev.repoID+"/poll", "")
		if resp.Status != 200 || string(resp.Raw) != `{"synced":3,"reviewTriggered":false}`+"\n" {
			t.Fatalf("POST poll = %d %s, want 200 {synced:3,reviewTriggered:false}", resp.Status, resp.Raw)
		}
		var rows int
		scan(t, ev.db, &rows, `SELECT count(*) FROM pull_requests WHERE repo_id = ? AND number IN (1,2,3)`, ev.repoID)
		if polled := polledAt(t, ev); rows != 3 || polled == nil {
			t.Errorf("after poll: %d rows, last_polled_at %v, want 3 rows and a timestamp", rows, polled)
		}
	})
	t.Run("no token", func(t *testing.T) {
		ev := newEnv(t, "")
		ev.do("POST", "/repos/"+ev.repoID+"/poll", "").wantError(t, 500, "config_error", "GITHUB_TOKEN is not configured")
		assertNotPolled(t, ev)
	})
	t.Run("github down is 502 after retries", func(t *testing.T) {
		ev := newEnv(t, "tok")
		ev.stub.handle("GET "+stubRepo+"/pulls", jsonHandler(503, map[string]string{"message": "unavailable"}))
		ev.do("POST", "/repos/"+ev.repoID+"/poll", "").wantError(t, 502, "external_service", "GitHub request failed")
		if n := ev.stub.count("GET " + stubRepo + "/pulls"); n != 4 {
			t.Errorf("list requests = %d, want 4 (1 + 3 retries)", n)
		}
		assertNotPolled(t, ev)
	})
	t.Run("github 404 is 502 without retry", func(t *testing.T) {
		ev := newEnv(t, "tok")
		ev.do("POST", "/repos/"+ev.repoID+"/poll", "").wantError(t, 502, "external_service", "GitHub request failed")
		if n := ev.stub.count("GET " + stubRepo + "/pulls"); n != 1 {
			t.Errorf("list requests = %d, want 1", n)
		}
	})
}

func polledAt(t *testing.T, ev *env) *time.Time {
	t.Helper()
	var row struct{ LastPolledAt *time.Time }
	scan(t, ev.db, &row, `SELECT last_polled_at FROM repos WHERE id = ?`, ev.repoID)
	return row.LastPolledAt
}

func assertNotPolled(t *testing.T, ev *env) {
	t.Helper()
	if polled := polledAt(t, ev); polled != nil {
		t.Errorf("last_polled_at = %v, want unchanged (null)", polled)
	}
}

// ---- list ----

func TestList_requestAndSync(t *testing.T) {
	ev := newEnv(t, "tok")
	ev.stub.handle("GET "+stubRepo+"/pulls", jsonHandler(200, []map[string]any{ghPull(7, "Seven", "open", "2026-10-20T00:00:00Z")}))
	ev.stub.stubPullDetail(ghPull(7, "Seven", "open", "2026-10-20T00:00:00Z"), nil, nil)

	resp := ev.do("GET", "/repos/"+ev.repoID+"/pulls", "")
	if resp.Status != 200 {
		t.Fatalf("GET pulls = %d\n%s", resp.Status, resp.Raw)
	}
	q := ev.stub.requests("GET " + stubRepo + "/pulls")[0].Query
	if diff := cmp.Diff(url.Values{"state": {"all"}, "per_page": {"50"}, "sort": {"updated"}, "direction": {"desc"}}, q); diff != "" {
		t.Errorf("list query mismatch (-want +got):\n%s", diff)
	}
	pr := byNumber(resp.list(t), 7)
	if pr == nil {
		t.Fatalf("list has no #7: %s", resp.Raw)
	}
	if id, _ := pr["id"].(string); !regexp.MustCompile(`^[0-9a-f-]{36}$`).MatchString(id) || pr["status"] != "needs_review" {
		t.Errorf("#7 id/status = %v / %v, want a uuid and needs_review", pr["id"], pr["status"])
	}
	if pr["opened_at"] != "2026-10-01T00:00:00.000Z" || pr["updated_at"] != "2026-10-20T00:00:00.000Z" {
		t.Errorf("#7 opened_at/updated_at = %v / %v, want .000Z UTC strings", pr["opened_at"], pr["updated_at"])
	}
}

func TestList_updateKeepsOtherFields(t *testing.T) {
	ev := newEnv(t, "tok")
	ev.insertPR(7, "old title", "a", ts(1, 1), 5, 5, 5)
	updated := ghPull(7, "new title", "closed", "2026-10-09T00:00:00Z")
	updated["user"] = map[string]string{"login": "b"}
	updated["head"] = map[string]string{"ref": "feat/x", "sha": "newsha"}
	ev.stub.handle("GET "+stubRepo+"/pulls", jsonHandler(200, []map[string]any{updated}))

	pr := byNumber(ev.do("GET", "/repos/"+ev.repoID+"/pulls", "").list(t), 7)
	if pr["title"] != "new title" || pr["head_sha"] != "newsha" || pr["status"] != "closed" || pr["author"] != "a" {
		t.Errorf("#7 after sync = %v, want new title/head_sha/closed status and author a", pr)
	}
}

func TestList_orderWithoutToken(t *testing.T) {
	ev := newEnv(t, "")
	ev.insertPR(1, "t", "a", ts(2, 10), 1, 1, 1)
	ev.insertPR(2, "t", "a", ts(2, 11), 1, 1, 1)
	ev.insertPR(3, "t", "a", nil, 1, 1, 1)
	var got []int
	for _, m := range ev.do("GET", "/repos/"+ev.repoID+"/pulls", "").list(t) {
		got = append(got, int(m["number"].(float64)))
	}
	if diff := cmp.Diff([]int{2, 1, 482, 3}, got); diff != "" {
		t.Errorf("order mismatch (-want +got):\n%s", diff)
	}
	if n := len(ev.stub.requests(".*")); n != 0 {
		t.Errorf("GitHub requests without a token = %d, want 0", n)
	}
}

func TestList_backfillsTenOfTwelve(t *testing.T) {
	ev := newEnv(t, "tok")
	for n := 1; n <= 12; n++ {
		ev.insertPR(n, "t", "a", ts(2, n), 0, 0, 0)
		ev.stub.stubPullDetail(ghPull(n, "t", "open", "2026-10-02T00:00:00Z"), nil, nil)
	}
	ev.stub.handle("GET "+stubRepo+"/pulls", jsonHandler(200, []map[string]any{}))

	list := ev.do("GET", "/repos/"+ev.repoID+"/pulls", "").list(t)
	if n := ev.stub.count(`GET ` + stubRepo + `/pulls/\d+`); n != 10 {
		t.Errorf("detail fetches = %d, want 10", n)
	}
	filled := 0
	for _, m := range list {
		if m["additions"].(float64) == 7 && m["deletions"].(float64) == 3 && m["files_count"].(float64) == 2 {
			filled++
		}
	}
	if filled != 10 {
		t.Errorf("PRs with fetched stats = %d, want 10", filled)
	}
}

func TestList_githubFailureServesStored(t *testing.T) {
	ev := newEnv(t, "tok")
	ev.stub.handle("GET "+stubRepo+"/pulls", jsonHandler(500, map[string]string{"message": "boom"}))
	resp := ev.do("GET", "/repos/"+ev.repoID+"/pulls", "")
	if resp.Status != 200 || byNumber(resp.list(t), 482) == nil {
		t.Errorf("GET pulls with GitHub 500 = %d %s, want 200 with the stored #482", resp.Status, resp.Raw)
	}
}

func TestList_onlyFiftyUpsertedOlderRowsKept(t *testing.T) {
	ev := newEnv(t, "tok")
	for n := 1; n <= 5; n++ {
		ev.insertPR(n, fmt.Sprintf("old %d", n), "a", ts(1, n), 1, 1, 1)
	}
	var all []map[string]any
	for n := 100; n < 160; n++ {
		all = append(all, ghPull(n, "gh", "open", "2026-10-15T00:00:00Z"))
	}
	ev.stub.handle("GET "+stubRepo+"/pulls", func(w http.ResponseWriter, r *http.Request) {
		per, _ := strconv.Atoi(r.URL.Query().Get("per_page"))
		writeJSON(w, 200, all[:min(per, len(all))])
	})
	list := ev.do("GET", "/repos/"+ev.repoID+"/pulls", "").list(t)

	var upserted int
	scan(t, ev.db, &upserted, `SELECT count(*) FROM pull_requests WHERE number BETWEEN 100 AND 159`)
	if upserted != 50 {
		t.Errorf("upserted PRs = %d, want 50", upserted)
	}
	for n := 1; n <= 5; n++ {
		if pr := byNumber(list, n); pr == nil || pr["title"] != fmt.Sprintf("old %d", n) {
			t.Errorf("older PR #%d = %v, want it unchanged in the response", n, pr)
		}
	}
}

func TestList_derivedStatuses(t *testing.T) {
	ev := newEnv(t, "")
	recent := time.Now().Add(-3 * 24 * time.Hour)
	old := time.Now().Add(-8 * 24 * time.Hour)
	a := ev.insertPR(1, "t", "a", &recent, 1, 1, 1)
	b := ev.insertPR(2, "t", "a", &old, 1, 1, 1)
	c := ev.insertPR(3, "t", "a", &recent, 1, 1, 1)
	ev.exec(`UPDATE pull_requests SET last_reviewed_sha = head_sha WHERE id IN (?, ?)`, a, b)
	ev.exec(`UPDATE pull_requests SET status = 'merged' WHERE id = ?`, c)
	list := ev.do("GET", "/repos/"+ev.repoID+"/pulls", "").list(t)
	got := map[int]any{1: byNumber(list, 1)["status"], 2: byNumber(list, 2)["status"], 3: byNumber(list, 3)["status"]}
	if diff := cmp.Diff(map[int]any{1: "reviewed", 2: "stale", 3: "merged"}, got); diff != "" {
		t.Errorf("statuses mismatch (-want +got):\n%s", diff)
	}
}

func TestList_seededPullCarriesTheDemoRollups(t *testing.T) {
	ev := newEnv(t, "tok") // GitHub answers 404 for acme/payments-api: the error is swallowed
	pr := byNumber(ev.do("GET", "/repos/"+ev.repoID+"/pulls", "").list(t), 482)
	if pr == nil {
		t.Fatal("list has no #482")
	}
	var reviewID string
	scan(t, ev.db, &reviewID, `SELECT id FROM reviews WHERE pr_id = ?`, ev.prID)
	got := map[string]any{
		"score": pr["score"], "cost_usd": pr["cost_usd"], "latest_review_id": pr["latest_review_id"],
		"latest_review_ids": pr["latest_review_ids"], "findings_counts": pr["findings_counts"],
	}
	want := map[string]any{
		"score": 61.0, "cost_usd": 0.002135, "latest_review_id": reviewID,
		"latest_review_ids": []any{reviewID}, "findings_counts": map[string]any{"CRITICAL": 1.0, "WARNING": 1.0, "SUGGESTION": 0.0},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("seeded #482 rollups mismatch (-want +got):\n%s", diff)
	}
}

func TestList_pullWithoutReviewsHasNullRollups(t *testing.T) {
	ev := newEnv(t, "tok") // GitHub answers 404 for acme/payments-api: the error is swallowed
	ev.dropDemoReview()
	resp := ev.do("GET", "/repos/"+ev.repoID+"/pulls", "")
	pr := byNumber(resp.list(t), 482)
	if pr == nil {
		t.Fatalf("list has no #482: %s", resp.Raw)
	}
	want := map[string]any{
		"id": ev.prID, "number": 482.0, "title": "Add rate limiting to public API endpoints", "author": "marisa.koch",
		"branch": "feat/rate-limit-public", "base": "main", "head_sha": "a1b2c3d4e5f6",
		"additions": 247.0, "deletions": 38.0, "files_count": 9.0, "status": "needs_review",
		"opened_at": nil, "updated_at": nil,
		"score": nil, "cost_usd": nil, "latest_review_id": nil, "last_reviewed_at": nil,
		"latest_review_ids": []any{}, "findings_counts": nil,
	}
	if diff := cmp.Diff(want, pr); diff != "" {
		t.Errorf("seeded #482 mismatch (-want +got):\n%s", diff)
	}
}

func TestList_rollupsOverHTTP(t *testing.T) {
	ev := newEnv(t, "")
	ev.dropDemoReview()
	var agentA, agentB, a2, b1 string
	scan(t, ev.db, &agentA, `INSERT INTO agents (workspace_id, name, provider, model, system_prompt) VALUES (?, 'A', 'openai', 'm', 'p') RETURNING id`, ev.wsID)
	scan(t, ev.db, &agentB, `INSERT INTO agents (workspace_id, name, provider, model, system_prompt) VALUES (?, 'B', 'openai', 'm', 'p') RETURNING id`, ev.wsID)
	review := func(agent string, score int, at time.Time) string {
		var id string
		scan(t, ev.db, &id, `INSERT INTO reviews (workspace_id, pr_id, agent_id, kind, score, created_at) VALUES (?, ?, ?, 'review', ?, ?) RETURNING id`,
			ev.wsID, ev.prID, agent, score, at)
		return id
	}
	base := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	review(agentA, 50, base)
	a2 = review(agentA, 70, base.Add(time.Hour))
	b1 = review(agentB, 90, base.Add(2*time.Hour))
	for _, f := range []struct{ review, sev string }{{a2, "CRITICAL"}, {a2, "WARNING"}, {a2, "WARNING"}, {b1, "SUGGESTION"}} {
		ev.exec(`INSERT INTO findings (review_id, file, start_line, end_line, severity, category, title, rationale, confidence)
			VALUES (?, 'a', 1, 1, ?, 'bug', 't', 'r', 0.5)`, f.review, f.sev)
	}
	for _, cost := range []any{0.5, nil, 0.25} {
		ev.exec(`INSERT INTO agent_runs (workspace_id, pr_id, cost_usd) VALUES (?, ?, ?)`, ev.wsID, ev.prID, cost)
	}

	pr := byNumber(ev.do("GET", "/repos/"+ev.repoID+"/pulls", "").list(t), 482)
	got := map[string]any{
		"score": pr["score"], "cost_usd": pr["cost_usd"], "latest_review_id": pr["latest_review_id"],
		"last_reviewed_at": pr["last_reviewed_at"], "latest_review_ids": pr["latest_review_ids"], "findings_counts": pr["findings_counts"],
	}
	want := map[string]any{
		"score": 90.0, "cost_usd": 0.75, "latest_review_id": b1, "last_reviewed_at": "2026-10-01T02:00:00.000Z",
		"latest_review_ids": []any{b1, a2}, "findings_counts": map[string]any{"CRITICAL": 1.0, "WARNING": 2.0, "SUGGESTION": 1.0},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("rollups mismatch (-want +got):\n%s", diff)
	}
}

func TestList_retriesTransientThenSyncs(t *testing.T) {
	ev := newEnv(t, "tok")
	calls := 0
	ev.stub.handle("GET "+stubRepo+"/pulls", func(w http.ResponseWriter, _ *http.Request) {
		calls++
		if calls <= 2 {
			writeJSON(w, 503, map[string]string{"message": "later"})
			return
		}
		writeJSON(w, 200, []map[string]any{ghPull(9, "nine", "open", "2026-10-05T00:00:00Z")})
	})
	ev.stub.stubPullDetail(ghPull(9, "nine", "open", "2026-10-05T00:00:00Z"), nil, nil)
	list := ev.do("GET", "/repos/"+ev.repoID+"/pulls", "").list(t)
	if n := ev.stub.count("GET " + stubRepo + "/pulls"); n != 3 || byNumber(list, 9) == nil {
		t.Errorf("list requests = %d, #9 present = %v, want 3 requests and #9 synced", n, byNumber(list, 9) != nil)
	}
}

func TestList_notFoundIsNotRetried(t *testing.T) {
	ev := newEnv(t, "tok")
	if resp := ev.do("GET", "/repos/"+ev.repoID+"/pulls", ""); resp.Status != 200 {
		t.Fatalf("GET pulls = %d, want 200", resp.Status)
	}
	if n := ev.stub.count("GET " + stubRepo + "/pulls"); n != 1 {
		t.Errorf("list requests on 404 = %d, want 1", n)
	}
}

func TestList_hangingGitHubServesStoredAfterOneRequest(t *testing.T) {
	ev := newEnv(t, "tok")
	ev.stub.handle("GET "+stubRepo+"/pulls", func(_ http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-ev.stub.stop:
		}
	})
	start := time.Now()
	resp := ev.do("GET", "/repos/"+ev.repoID+"/pulls", "")
	if resp.Status != 200 || byNumber(resp.list(t), 482) == nil {
		t.Errorf("GET pulls with hanging GitHub = %d %s, want 200 with stored rows", resp.Status, resp.Raw)
	}
	if n := ev.stub.count("GET " + stubRepo + "/pulls"); n != 1 {
		t.Errorf("list requests = %d, want exactly 1 (a timeout is not retried)", n)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("response took %v, want it bounded by the client timeout", d)
	}
}

// ---- detail ----

var detailRollupKeys = []string{"score", "cost_usd", "latest_review_id", "last_reviewed_at", "latest_review_ids", "findings_counts"}

func TestDetail_freshFromGitHub(t *testing.T) {
	ev := newEnv(t, "tok")
	id := ev.insertPR(7, "stored title", "a", ts(1, 1), 0, 0, 0)
	pull := ghPull(7, "Seven", "open", "2026-10-20T00:00:00Z")
	pull["body"] = "Fixes #12"
	ev.stub.stubPullDetail(pull,
		[]map[string]any{ghFile("a.go", 1, 0, "@@ one"), ghFile("b.bin", 4, 4, nil), ghFile("a.go", 2, 1, "@@ two")},
		[]map[string]any{ghCommit("c1"), ghCommit("c2"), ghCommit("c3")})
	ev.stub.handle("GET "+stubRepo+"/issues/12", jsonHandler(200, map[string]any{"number": 12, "title": "Bug", "body": nil, "state": "open"}))

	resp := ev.do("GET", "/pulls/"+id, "")
	if resp.Status != 200 {
		t.Fatalf("GET /pulls/:id = %d\n%s", resp.Status, resp.Raw)
	}
	d := resp.object(t)
	if d["status"] != "open" || d["id"] != id || d["title"] != "Seven" || d["body"] != "Fixes #12" {
		t.Errorf("detail scalars = status %v id %v title %v body %v", d["status"], d["id"], d["title"], d["body"])
	}
	wantFiles := []any{
		map[string]any{"path": "a.go", "additions": 3.0, "deletions": 1.0, "patch": "@@ one\n@@ two"},
		map[string]any{"path": "b.bin", "additions": 4.0, "deletions": 4.0}, // no patch: key omitted
	}
	if diff := cmp.Diff(wantFiles, d["files"]); diff != "" {
		t.Errorf("files mismatch (-want +got):\n%s", diff)
	}
	if commits, _ := d["commits"].([]any); len(commits) != 3 {
		t.Errorf("commits = %v, want 3", d["commits"])
	}
	wantIssue := map[string]any{"number": 12.0, "title": "Bug", "body": nil, "state": "open"}
	if diff := cmp.Diff(wantIssue, d["linked_issue"]); diff != "" {
		t.Errorf("linked_issue mismatch (-want +got):\n%s", diff)
	}
	for _, k := range detailRollupKeys {
		if _, ok := d[k]; ok {
			t.Errorf("detail has rollup key %q, want it absent", k)
		}
	}

	var stored struct {
		Body                          string
		Additions, Deletions, FilesCt int
	}
	scan(t, ev.db, &stored, `SELECT body, additions, deletions, files_count AS files_ct FROM pull_requests WHERE id = ?`, id)
	var files, commits int
	scan(t, ev.db, &files, `SELECT count(*) FROM pr_files WHERE pr_id = ?`, id)
	scan(t, ev.db, &commits, `SELECT count(*) FROM pr_commits WHERE pr_id = ?`, id)
	if stored.Body != "Fixes #12" || stored.Additions != 7 || stored.Deletions != 3 || stored.FilesCt != 2 || files != 2 || commits != 3 {
		t.Errorf("stored mirror = %+v, %d files, %d commits, want the fresh values (7/3/2, 2 files, 3 commits)", stored, files, commits)
	}
}

func TestDetail_seededWithoutToken(t *testing.T) {
	ev := newEnv(t, "")
	resp := ev.do("GET", "/pulls/"+ev.prID, "")
	d := resp.object(t)
	if resp.Status != 200 || d["status"] != "needs_review" {
		t.Fatalf("GET seeded detail = %d status %v, want 200 needs_review", resp.Status, d["status"])
	}
	files, _ := d["files"].([]any)
	if len(files) != 4 {
		t.Fatalf("files = %d, want the 4 seeded", len(files))
	}
	for _, f := range files {
		patch, _ := f.(map[string]any)["patch"].(string)
		var want string
		scan(t, ev.db, &want, `SELECT patch FROM pr_files WHERE pr_id = ? AND path = ?`, ev.prID, f.(map[string]any)["path"])
		if patch == "" || patch != want {
			t.Errorf("file %v patch mismatch with the seeded patch", f.(map[string]any)["path"])
		}
	}
	if commits, _ := d["commits"].([]any); len(commits) != 1 {
		t.Errorf("commits = %v, want 1", d["commits"])
	} else if c := commits[0].(map[string]any); c["sha"] != "a1b2c3d4e5f6" || c["committed_at"] != nil {
		t.Errorf("commit = %v, want a1b2c3d4e5f6 with committed_at null", c)
	}
	if _, ok := d["linked_issue"]; ok {
		t.Errorf("detail has linked_issue, want the key absent")
	}
	for _, k := range detailRollupKeys {
		if _, ok := d[k]; ok {
			t.Errorf("detail has rollup key %q, want it absent", k)
		}
	}
	if d["body"] == nil || d["opened_at"] != nil || d["updated_at"] != nil {
		t.Errorf("seeded body/opened_at/updated_at = %v / %v / %v", d["body"], d["opened_at"], d["updated_at"])
	}
}

func TestDetail_storedFileWithoutPatchIsNull(t *testing.T) {
	ev := newEnv(t, "")
	ev.exec(`INSERT INTO pr_files (pr_id, path, additions, deletions) VALUES (?, 'bin.dat', 1, 1)`, ev.prID)
	for _, f := range ev.do("GET", "/pulls/"+ev.prID, "").object(t)["files"].([]any) {
		m := f.(map[string]any)
		if m["path"] != "bin.dat" {
			continue
		}
		if patch, ok := m["patch"]; !ok || patch != nil {
			t.Errorf("stored file without patch: patch = %v (present %v), want null", patch, ok)
		}
	}
}

func TestDetail_capsFilesAndCommits(t *testing.T) {
	ev := newEnv(t, "tok")
	id := ev.insertPR(8, "t", "a", ts(1, 1), 0, 0, 0)
	var files, commits []map[string]any
	for i := 0; i < 3100; i++ {
		files = append(files, ghFile(fmt.Sprintf("f%d.go", i), 1, 0, "@@"))
	}
	for i := 0; i < 300; i++ {
		commits = append(commits, ghCommit(fmt.Sprintf("c%d", i)))
	}
	ev.stub.stubPullDetail(ghPull(8, "t", "open", "2026-10-20T00:00:00Z"), files, commits)
	d := ev.do("GET", "/pulls/"+id, "").object(t)
	if f, c := len(d["files"].([]any)), len(d["commits"].([]any)); f != 3000 || c != 250 {
		t.Errorf("files/commits = %d/%d, want 3000/250", f, c)
	}
}

func TestDetail_issueNotFoundOmitsLinkedIssue(t *testing.T) {
	ev := newEnv(t, "tok")
	id := ev.insertPR(8, "t", "a", ts(1, 1), 0, 0, 0)
	pull := ghPull(8, "t", "open", "2026-10-20T00:00:00Z")
	pull["body"] = "mentions #99"
	ev.stub.stubPullDetail(pull, nil, nil)
	resp := ev.do("GET", "/pulls/"+id, "")
	if _, ok := resp.object(t)["linked_issue"]; resp.Status != 200 || ok {
		t.Errorf("GET detail with issue 404 = %d, linked_issue present %v, want 200 without it", resp.Status, ok)
	}
}

func TestDetail_githubFailureServesStoredRawStatus(t *testing.T) {
	ev := newEnv(t, "tok")
	ev.stub.handle("GET "+stubRepo+"/pulls/482", jsonHandler(500, map[string]string{"message": "boom"}))
	d := ev.do("GET", "/pulls/"+ev.prID, "").object(t)
	if d["status"] != "needs_review" || len(d["files"].([]any)) != 4 {
		t.Errorf("stored detail = status %v, %d files, want raw stored status and the 4 files", d["status"], len(d["files"].([]any)))
	}
}

// ---- comments ----

func TestComments_mapping(t *testing.T) {
	ev := newEnv(t, "tok")
	ev.stub.handle("GET "+stubRepo+"/pulls/482/comments", jsonHandler(200, []map[string]any{
		{"id": 1, "path": "a.go", "line": nil, "original_line": 4, "side": nil, "body": "outdated", "user": nil,
			"created_at": "2026-10-03T04:05:06Z", "html_url": "https://x/1", "in_reply_to_id": nil},
		{"id": 2, "path": "a.go", "line": 9, "original_line": 9, "side": "LEFT", "body": "reply", "user": map[string]string{"login": "bob"},
			"created_at": "2026-10-03T04:06:00Z", "html_url": "https://x/2", "in_reply_to_id": 1},
	}))
	resp := ev.do("GET", "/pulls/"+ev.prID+"/comments", "")
	want := []map[string]any{
		{"id": 1.0, "path": "a.go", "line": nil, "original_line": 4.0, "side": "RIGHT", "body": "outdated", "user": "unknown",
			"created_at": "2026-10-03T04:05:06.000Z", "html_url": "https://x/1", "in_reply_to_id": nil, "is_outdated": true},
		{"id": 2.0, "path": "a.go", "line": 9.0, "original_line": 9.0, "side": "LEFT", "body": "reply", "user": "bob",
			"created_at": "2026-10-03T04:06:00.000Z", "html_url": "https://x/2", "in_reply_to_id": 1.0, "is_outdated": false},
	}
	if diff := cmp.Diff(want, resp.list(t)); diff != "" {
		t.Errorf("comments mismatch (-want +got):\n%s", diff)
	}
	if q := ev.stub.requests("GET " + stubRepo + "/pulls/482/comments")[0].Query.Get("per_page"); q != "100" {
		t.Errorf("per_page = %q, want 100", q)
	}
}

func TestComments_emptyWhenUnavailable(t *testing.T) {
	for name, token := range map[string]string{"no token": "", "github 500": "tok"} {
		t.Run(name, func(t *testing.T) {
			ev := newEnv(t, token)
			ev.stub.handle("GET "+stubRepo+"/pulls/482/comments", jsonHandler(500, map[string]string{"message": "boom"}))
			resp := ev.do("GET", "/pulls/"+ev.prID+"/comments", "")
			if resp.Status != 200 || strings.TrimSpace(string(resp.Raw)) != "[]" {
				t.Errorf("GET comments = %d %s, want 200 []", resp.Status, resp.Raw)
			}
		})
	}
}

func TestPostComment(t *testing.T) {
	created := map[string]any{"id": 77, "path": "a.ts", "line": 3, "original_line": 3, "side": "RIGHT", "body": "x", "user": map[string]string{"login": "me"},
		"created_at": "2026-10-04T00:00:00Z", "html_url": "https://x/77", "in_reply_to_id": nil}

	t.Run("new comment on the stored head", func(t *testing.T) {
		ev := newEnv(t, "tok")
		ev.stub.handle("POST "+stubRepo+"/pulls/482/comments", jsonHandler(201, created))
		resp := ev.do("POST", "/pulls/"+ev.prID+"/comments", `{"path":"a.ts","line":3,"body":"x"}`)
		if resp.Status != 200 || resp.object(t)["id"] != 77.0 || resp.object(t)["user"] != "me" {
			t.Fatalf("POST comment = %d %s, want 200 with the created comment", resp.Status, resp.Raw)
		}
		var sent map[string]any
		_ = json.Unmarshal(ev.stub.requests("POST .*")[0].Body, &sent)
		want := map[string]any{"body": "x", "commit_id": "a1b2c3d4e5f6", "path": "a.ts", "line": 3.0, "side": "RIGHT"}
		if diff := cmp.Diff(want, sent); diff != "" {
			t.Errorf("create request mismatch (-want +got):\n%s", diff)
		}
	})
	t.Run("reply", func(t *testing.T) {
		ev := newEnv(t, "tok")
		ev.stub.handle("POST "+stubRepo+"/pulls/482/comments/5/replies", jsonHandler(201, created))
		resp := ev.do("POST", "/pulls/"+ev.prID+"/comments", `{"path":"a.ts","line":3,"body":"x","in_reply_to":5}`)
		if resp.Status != 200 || ev.stub.count("POST "+stubRepo+"/pulls/482/comments/5/replies") != 1 || ev.stub.count("POST "+stubRepo+"/pulls/482/comments") != 0 {
			t.Errorf("reply = %d, replies %d, creates %d, want 200 with one reply request", resp.Status,
				ev.stub.count("POST "+stubRepo+"/pulls/482/comments/5/replies"), ev.stub.count("POST "+stubRepo+"/pulls/482/comments"))
		}
	})
	t.Run("no token", func(t *testing.T) {
		ev := newEnv(t, "")
		ev.do("POST", "/pulls/"+ev.prID+"/comments", `{"path":"a.ts","line":3,"body":"x"}`).
			wantError(t, 400, "github_unavailable", "Connect a GitHub token to post comments.")
	})
	t.Run("github rejects", func(t *testing.T) {
		ev := newEnv(t, "tok")
		ev.stub.handle("POST "+stubRepo+"/pulls/482/comments", jsonHandler(422, map[string]string{"message": "Validation Failed"}))
		resp := ev.do("POST", "/pulls/"+ev.prID+"/comments", `{"path":"a.ts","line":3,"body":"x"}`)
		resp.wantError(t, 400, "github_comment_failed", "Validation Failed")
		var body struct {
			Error struct{ Details struct{ Cause string } }
		}
		resp.decode(t, &body)
		if body.Error.Details.Cause == "" {
			t.Errorf("details.cause is empty: %s", resp.Raw)
		}
		if n := ev.stub.count("POST .*"); n != 1 {
			t.Errorf("create requests = %d, want exactly 1", n)
		}
	})
	t.Run("transient failure is never retried", func(t *testing.T) {
		ev := newEnv(t, "tok")
		ev.stub.handle("POST "+stubRepo+"/pulls/482/comments", jsonHandler(503, map[string]string{"message": "later"}))
		ev.do("POST", "/pulls/"+ev.prID+"/comments", `{"path":"a.ts","line":3,"body":"x"}`).
			wantError(t, 400, "github_comment_failed", "later")
		if n := ev.stub.count("POST .*"); n != 1 {
			t.Errorf("create requests on 503 = %d, want exactly 1", n)
		}
	})
}

func TestTokenIsReadPerCall(t *testing.T) {
	ev := newEnv(t, "")
	ev.do("POST", "/pulls/"+ev.prID+"/comments", `{"path":"a.ts","line":3,"body":"x"}`).
		wantError(t, 400, "github_unavailable", "Connect a GitHub token to post comments.")
	ev.token = "now-set"
	ev.stub.handle("POST "+stubRepo+"/pulls/482/comments", jsonHandler(201, map[string]any{"id": 1, "path": "a.ts", "side": "RIGHT",
		"created_at": "2026-10-04T00:00:00Z", "html_url": "u", "body": "x"}))
	if resp := ev.do("POST", "/pulls/"+ev.prID+"/comments", `{"path":"a.ts","line":3,"body":"x"}`); resp.Status != 200 {
		t.Errorf("POST comment after the token was set = %d %s, want 200", resp.Status, resp.Raw)
	}
}

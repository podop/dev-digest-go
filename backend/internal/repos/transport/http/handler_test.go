package httpapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/labstack/echo/v5"
	"gorm.io/gorm"

	"dev-digest-go/backend/internal/platform/httpx"
	"dev-digest-go/backend/internal/platform/postgres/pgtest"
	reposdb "dev-digest-go/backend/internal/repos/adapters/postgres"
	"dev-digest-go/backend/internal/repos/app"
	"dev-digest-go/backend/internal/repos/domain"
	httpapi "dev-digest-go/backend/internal/repos/transport/http"
	"dev-digest-go/backend/internal/tenancy"
	tenancypg "dev-digest-go/backend/internal/tenancy/adapters/postgres"
)

func TestMain(m *testing.M) { os.Exit(pgtest.Main(m)) }

const cloneDir = "/data/workspace"

type queued struct {
	WorkspaceID string
	Kind        string
	Payload     any
}

// fakeQueue is the job-queue port; it records jobs instead of running them.
type fakeQueue struct {
	mu   sync.Mutex
	jobs []queued
	err  error
}

func (q *fakeQueue) Enqueue(_ context.Context, ws, kind string, payload any) (string, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.err != nil {
		return "", q.err
	}
	q.jobs = append(q.jobs, queued{ws, kind, payload})
	return "job", nil
}

func (q *fakeQueue) reset() {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.jobs = nil
}

func (q *fakeQueue) kinds() []string {
	q.mu.Lock()
	defer q.mu.Unlock()
	var out []string
	for _, j := range q.jobs {
		out = append(out, j.Kind)
	}
	return out
}

type env struct {
	e     *echo.Echo
	db    *gorm.DB
	queue *fakeQueue
	ws    string
	user  string
}

// newEnv builds the routes over a migrated database holding the default workspace and the
// system user (no demo data).
func newEnv(t *testing.T, opts ...app.ServiceOption) *env {
	t.Helper()
	d := pgtest.Migrated(t)
	var ws, user string
	if err := d.DB.Raw(`INSERT INTO workspaces (name) VALUES ('default') RETURNING id`).Scan(&ws).Error; err != nil {
		t.Fatal(err)
	}
	if err := d.DB.Raw(`INSERT INTO users (email, name) VALUES ('you@local', 'You') RETURNING id`).Scan(&user).Error; err != nil {
		t.Fatal(err)
	}
	return wire(t, d.DB, ws, user, opts...)
}

func wire(t *testing.T, db *gorm.DB, ws, user string, opts ...app.ServiceOption) *env {
	t.Helper()
	queue := &fakeQueue{}
	store := reposdb.NewStore(db)
	e := httpx.New(httpx.Options{Log: slog.New(slog.DiscardHandler), CORSOrigins: []string{uiOrigin}})
	httpapi.AddRoutes(e, app.NewService(store, queue, cloneDir, opts...), tenancy.NewResolver(tenancypg.NewStore(db)))
	return &env{e: e, db: db, queue: queue, ws: ws, user: user}
}

type response struct {
	status int
	body   string
}

func (v *env) do(t *testing.T, method, path, body string, headers ...string) response {
	t.Helper()
	var req *http.Request
	if body == "" {
		req = httptest.NewRequest(method, path, nil)
	} else {
		req = httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
	}
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	rec := httptest.NewRecorder()
	v.e.ServeHTTP(rec, req)
	return response{rec.Code, strings.TrimSpace(rec.Body.String())}
}

func decode[T any](t *testing.T, r response) T {
	t.Helper()
	var v T
	if err := json.Unmarshal([]byte(r.body), &v); err != nil {
		t.Fatalf("response body %q is not the expected JSON: %v", r.body, err)
	}
	return v
}

func (v *env) count(t *testing.T, table string) int64 {
	t.Helper()
	var n int64
	if err := v.db.Table(table).Count(&n).Error; err != nil {
		t.Fatal(err)
	}
	return n
}

func post(t *testing.T, v *env, url string) response {
	t.Helper()
	return v.do(t, http.MethodPost, "/repos", `{"url":`+quote(url)+`}`)
}

func quote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func TestAdd_Created(t *testing.T) {
	v := newEnv(t)
	res := post(t, v, " https://github.com/o/r.git ")
	if res.status != http.StatusCreated {
		t.Fatalf("POST /repos status = %d (%s), want 201", res.status, res.body)
	}
	got := decode[map[string]any](t, res)
	id, _ := got["id"].(string)
	want := map[string]any{
		"id": id, "workspace_id": v.ws, "owner": "o", "name": "r", "full_name": "o/r",
		"default_branch": "main", "clone_path": nil, "last_polled_at": nil, "created_by": v.user,
		"provider": "github", "host": "github.com", "web_url": "https://github.com/o/r", "connection_id": nil,
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("POST /repos body mismatch (-want +got):\n%s", diff)
	}
	wantJobs := []queued{{v.ws, "clone", app.ClonePayload{RepoID: id, Owner: "o", Name: "r"}}}
	if diff := cmp.Diff(wantJobs, v.queue.jobs); diff != "" {
		t.Errorf("enqueued jobs mismatch (-want +got):\n%s", diff)
	}
}

func TestAdd_ExistingAndCaseSensitive(t *testing.T) {
	v := newEnv(t)
	first := decode[map[string]any](t, post(t, v, "https://github.com/o/r"))
	v.queue.reset()

	for _, url := range []string{"git@github.com:o/r", "https://github.com/o/r.git/", "https://github.com/o/r"} {
		res := post(t, v, url)
		if res.status != http.StatusOK {
			t.Errorf("POST /repos %q status = %d, want 200", url, res.status)
		}
		if got := decode[map[string]any](t, res); got["id"] != first["id"] {
			t.Errorf("POST /repos %q id = %v, want %v", url, got["id"], first["id"])
		}
	}
	if len(v.queue.jobs) != 0 {
		t.Errorf("jobs after duplicate adds = %v, want none", v.queue.kinds())
	}

	res := post(t, v, "https://github.com/O/r")
	if res.status != http.StatusCreated {
		t.Fatalf("POST /repos O/r status = %d, want 201", res.status)
	}
	if got := decode[map[string]any](t, res); got["id"] == first["id"] {
		t.Error("POST /repos O/r returned the o/r repo, want a new one")
	}
}

func TestAdd_Concurrent(t *testing.T) {
	v := newEnv(t)
	const n = 8
	statuses := make([]int, n)
	ids := make([]string, n)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range n {
		wg.Go(func() {
			<-start
			res := post(t, v, "https://github.com/o/c")
			statuses[i] = res.status
			var body struct {
				ID string `json:"id"`
			}
			_ = json.Unmarshal([]byte(res.body), &body)
			ids[i] = body.ID
		})
	}
	close(start)
	wg.Wait()

	created := 0
	for i, s := range statuses {
		switch s {
		case http.StatusCreated:
			created++
		case http.StatusOK:
		default:
			t.Errorf("concurrent POST #%d status = %d, want 201 or 200", i, s)
		}
		if ids[i] != ids[0] {
			t.Errorf("concurrent POST #%d id = %s, want %s", i, ids[i], ids[0])
		}
	}
	if created != 1 {
		t.Errorf("201 responses = %d, want exactly 1", created)
	}
	if got := v.count(t, "repos"); got != 1 {
		t.Errorf("repo rows = %d, want 1", got)
	}
	if got := len(v.queue.jobs); got != 1 {
		t.Errorf("enqueued jobs = %d, want 1", got)
	}
}

func TestAdd_InvalidURL(t *testing.T) {
	v := newEnv(t)
	for _, url := range []string{
		"http://github.com/o/r",
		"ssh://git@github.com/o/r",
		"https://github.com/o/r/tree/main",
		"https://gitlab.com/o/r/-/merge_requests/1",
		"https://gitlab.com/o",
		"https://github.com/../r",
		"https://github.com/o/.git",
		"https://github.com/-x/r",
		"",
	} {
		t.Run(url, func(t *testing.T) {
			res := post(t, v, url)
			if res.status != http.StatusUnprocessableEntity {
				t.Fatalf("POST /repos %q status = %d (%s), want 422", url, res.status, res.body)
			}
			var got httpx.ErrorBody
			if err := json.Unmarshal([]byte(res.body), &got); err != nil {
				t.Fatal(err)
			}
			if got.Error.Code != "validation_error" || got.Error.Message != "Request validation failed" {
				t.Errorf("error = %+v, want validation_error / Request validation failed", got.Error)
			}
			wantDetails := []any{map[string]any{
				"keyword": "custom", "instancePath": "/url", "schemaPath": "#/url/custom",
				"message": domain.InvalidURLMessage,
				"params": map[string]any{"issue": map[string]any{
					"code": "custom", "path": []any{"url"},
					"message": domain.InvalidURLMessage,
				}},
			}}
			if diff := cmp.Diff(wantDetails, got.Error.Details); diff != "" {
				t.Errorf("details mismatch (-want +got):\n%s", diff)
			}
		})
	}
	if got := v.count(t, "repos"); got != 0 {
		t.Errorf("repo rows = %d, want 0", got)
	}
	if len(v.queue.jobs) != 0 {
		t.Errorf("jobs = %v, want none", v.queue.kinds())
	}
}

func TestAdd_BadBodyShape(t *testing.T) {
	v := newEnv(t)
	tests := []struct {
		name, body, msg string
	}{
		{"no body", "", "Required"},
		{"missing url", `{}`, "Required"},
		{"number url", `{"url":5}`, "Expected string, received number"},
		{"null url", `{"url":null}`, "Expected string, received null"},
		{"array body", `[]`, "Expected object, received array"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			res := v.do(t, http.MethodPost, "/repos", tc.body)
			if res.status != http.StatusUnprocessableEntity {
				t.Fatalf("status = %d (%s), want 422", res.status, res.body)
			}
			var got httpx.ErrorBody
			if err := json.Unmarshal([]byte(res.body), &got); err != nil {
				t.Fatal(err)
			}
			details, _ := got.Error.Details.([]any)
			if len(details) != 1 || details[0].(map[string]any)["message"] != tc.msg {
				t.Errorf("details = %v, want one issue %q", got.Error.Details, tc.msg)
			}
		})
	}
}

func TestAdd_EnqueueFailureKeepsRow(t *testing.T) {
	v := newEnv(t)
	v.queue.err = errors.New("queue down")
	res := post(t, v, "https://github.com/o/r")
	if res.status != http.StatusInternalServerError || !strings.Contains(res.body, `"internal_error"`) {
		t.Errorf("POST /repos status = %d (%s), want 500 internal_error", res.status, res.body)
	}
	if strings.Contains(res.body, "queue down") {
		t.Errorf("body %s leaks the internal error", res.body)
	}
	if got := v.count(t, "repos"); got != 1 {
		t.Errorf("repo rows = %d, want the row to stay", got)
	}
}

var isoMillis = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}Z$`)

func TestList_OrderKeysAndTimestamps(t *testing.T) {
	v := newEnv(t)
	for _, name := range []string{"a", "b", "c"} {
		post(t, v, "https://github.com/o/"+name)
	}
	if err := v.db.Exec(`UPDATE repos SET last_polled_at = '2026-10-21 09:15:00.5+00' WHERE name = 'b'`).Error; err != nil {
		t.Fatal(err)
	}

	res := v.do(t, http.MethodGet, "/repos", "")
	if res.status != http.StatusOK {
		t.Fatalf("GET /repos status = %d, want 200", res.status)
	}
	got := decode[[]map[string]any](t, res)
	var names []string
	for _, r := range got {
		names = append(names, r["name"].(string))
		if len(r) != 13 {
			t.Errorf("repo keys = %d, want all 13 Repo keys present: %v", len(r), r)
		}
		if p, ok := r["last_polled_at"].(string); ok && !isoMillis.MatchString(p) {
			t.Errorf("last_polled_at = %q, want ISO-8601 with milliseconds", p)
		}
	}
	if diff := cmp.Diff([]string{"a", "b", "c"}, names); diff != "" {
		t.Errorf("GET /repos order mismatch (-want +got):\n%s", diff)
	}
	if got[1]["last_polled_at"] != "2026-10-21T09:15:00.500Z" {
		t.Errorf("last_polled_at = %v, want 2026-10-21T09:15:00.500Z", got[1]["last_polled_at"])
	}
}

func TestList_EmptyIsArray(t *testing.T) {
	v := newEnv(t)
	if res := v.do(t, http.MethodGet, "/repos", ""); res.status != 200 || res.body != "[]" {
		t.Errorf("GET /repos = %d %s, want 200 []", res.status, res.body)
	}
}

func TestList_IgnoresAuthHeaders(t *testing.T) {
	v := newEnv(t)
	post(t, v, "https://github.com/o/r")
	plain := v.do(t, http.MethodGet, "/repos", "")
	authed := v.do(t, http.MethodGet, "/repos", "", "Authorization", "Bearer x", "Cookie", "session=abc")
	if plain != authed {
		t.Errorf("GET /repos with auth headers = %+v, want %+v", authed, plain)
	}
}

func TestRefresh(t *testing.T) {
	v := newEnv(t)
	fresh := decode[map[string]any](t, post(t, v, "https://github.com/o/fresh"))["id"].(string)
	cloned := decode[map[string]any](t, post(t, v, "https://github.com/o/cloned"))["id"].(string)
	if err := v.db.Exec(`UPDATE repos SET clone_path = '/data/workspace/o/cloned' WHERE id = ?`, cloned).Error; err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name string
		id   string
		want queued
	}{
		{"not cloned enqueues clone", fresh, queued{v.ws, "clone", app.ClonePayload{RepoID: fresh, Owner: "o", Name: "fresh"}}},
		{"cloned enqueues refresh", cloned, queued{v.ws, "refresh", app.RefreshPayload{RepoID: cloned}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			v.queue.reset()
			// No body and no Content-Type, as the UI sends it.
			res := v.do(t, http.MethodPost, "/repos/"+tc.id+"/refresh", "")
			if res.status != http.StatusOK || res.body != `{"status":"refreshing"}` {
				t.Fatalf("POST refresh = %d %s, want 200 {\"status\":\"refreshing\"}", res.status, res.body)
			}
			if diff := cmp.Diff([]queued{tc.want}, v.queue.jobs); diff != "" {
				t.Errorf("enqueued jobs mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestUnknownAndInvalidIDs(t *testing.T) {
	v := newEnv(t)
	const unknown = "6f1c8b64-6f0b-4b6a-9d0e-3f8f0a1b2c3d"
	notFound := `{"error":{"code":"not_found","message":"Repo not found"}}`
	for _, tc := range []struct{ method, path string }{
		{http.MethodPost, "/repos/" + unknown + "/refresh"},
		{http.MethodDelete, "/repos/" + unknown},
	} {
		if res := v.do(t, tc.method, tc.path, ""); res.status != http.StatusNotFound || res.body != notFound {
			t.Errorf("%s %s = %d %s, want 404 %s", tc.method, tc.path, res.status, res.body, notFound)
		}
	}
	for _, tc := range []struct{ method, path string }{
		{http.MethodPost, "/repos/not-a-uuid/refresh"},
		{http.MethodDelete, "/repos/not-a-uuid"},
	} {
		res := v.do(t, tc.method, tc.path, "")
		if res.status != http.StatusUnprocessableEntity || !strings.Contains(res.body, `"validation_error"`) {
			t.Errorf("%s %s = %d %s, want 422 validation_error", tc.method, tc.path, res.status, res.body)
		}
	}
	if len(v.queue.jobs) != 0 {
		t.Errorf("jobs = %v, want none", v.queue.kinds())
	}
}

func TestDelete(t *testing.T) {
	v := newEnv(t)
	id := decode[map[string]any](t, post(t, v, "https://github.com/o/r"))["id"].(string)
	cloneDir := t.TempDir()
	if err := v.db.Exec(`UPDATE repos SET clone_path = ? WHERE id = ?`, cloneDir, id).Error; err != nil {
		t.Fatal(err)
	}
	var prID string
	if err := v.db.Raw(`INSERT INTO pull_requests (workspace_id, repo_id, number, title, author, branch, base, head_sha)
		VALUES (?, ?, 1, 't', 'a', 'b', 'main', 'sha') RETURNING id`, v.ws, id).Scan(&prID).Error; err != nil {
		t.Fatal(err)
	}
	if err := v.db.Exec(`INSERT INTO pr_files (pr_id, path) VALUES (?, 'a.go')`, prID).Error; err != nil {
		t.Fatal(err)
	}
	if err := v.db.Exec(`INSERT INTO pr_commits (pr_id, sha, message, author) VALUES (?, 'abc', 'm', 'a')`, prID).Error; err != nil {
		t.Fatal(err)
	}

	res := v.do(t, http.MethodDelete, "/repos/"+id, "")
	if want := `{"deleted":"` + id + `"}`; res.status != http.StatusOK || res.body != want {
		t.Fatalf("DELETE /repos/:id = %d %s, want 200 %s", res.status, res.body, want)
	}
	for _, table := range []string{"repos", "pull_requests", "pr_files", "pr_commits"} {
		if n := v.count(t, table); n != 0 {
			t.Errorf("rows in %s = %d, want 0 after delete", table, n)
		}
	}
	if _, err := os.Stat(cloneDir); err != nil {
		t.Errorf("clone directory after delete: %v, want it to remain", err)
	}
	if res := v.do(t, http.MethodDelete, "/repos/"+id, ""); res.status != http.StatusNotFound {
		t.Errorf("second DELETE status = %d, want 404", res.status)
	}
}

func TestDelete_OtherWorkspaceIs404(t *testing.T) {
	v := newEnv(t)
	var otherWS, otherRepo string
	if err := v.db.Raw(`INSERT INTO workspaces (name) VALUES ('other') RETURNING id`).Scan(&otherWS).Error; err != nil {
		t.Fatal(err)
	}
	if err := v.db.Raw(`INSERT INTO repos (workspace_id, owner, name, full_name) VALUES (?, 'x', 'y', 'x/y') RETURNING id`, otherWS).Scan(&otherRepo).Error; err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct{ method, path string }{
		{http.MethodDelete, "/repos/" + otherRepo},
		{http.MethodPost, "/repos/" + otherRepo + "/refresh"},
	} {
		res := v.do(t, tc.method, tc.path, "")
		if res.status != http.StatusNotFound || !strings.Contains(res.body, "Repo not found") {
			t.Errorf("%s %s = %d %s, want 404 Repo not found", tc.method, tc.path, res.status, res.body)
		}
	}
	if n := v.count(t, "repos"); n != 1 {
		t.Errorf("repo rows = %d, want the other workspace's row to remain", n)
	}
	if got := decode[[]any](t, v.do(t, http.MethodGet, "/repos", "")); len(got) != 0 {
		t.Errorf("GET /repos = %v, want none of the other workspace's repos", got)
	}
	if len(v.queue.jobs) != 0 {
		t.Errorf("jobs = %v, want none", v.queue.kinds())
	}
}

func TestNotProvisioned_Is500(t *testing.T) {
	d := pgtest.Migrated(t) // no workspace, no user
	v := wire(t, d.DB, "", "")
	for _, tc := range []struct{ method, path, body string }{
		{http.MethodGet, "/repos", ""},
		{http.MethodPost, "/repos", `{"url":"https://github.com/o/r"}`},
		{http.MethodPost, "/repos/6f1c8b64-6f0b-4b6a-9d0e-3f8f0a1b2c3d/refresh", ""},
		{http.MethodDelete, "/repos/6f1c8b64-6f0b-4b6a-9d0e-3f8f0a1b2c3d", ""},
	} {
		res := v.do(t, tc.method, tc.path, tc.body)
		want := `{"error":{"code":"internal_error","message":"Internal error"}}`
		if res.status != http.StatusInternalServerError || res.body != want {
			t.Errorf("%s %s = %d %s, want 500 %s", tc.method, tc.path, res.status, res.body, want)
		}
	}
}

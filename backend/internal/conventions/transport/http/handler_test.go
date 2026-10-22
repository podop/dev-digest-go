package conventionshttp_test

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/labstack/echo/v5"
	"gorm.io/gorm"

	pgstore "dev-digest-go/backend/internal/conventions/adapters/postgres"
	"dev-digest-go/backend/internal/conventions/app"
	conventionshttp "dev-digest-go/backend/internal/conventions/transport/http"
	"dev-digest-go/backend/internal/platform/httpx"
	"dev-digest-go/backend/internal/platform/postgres/pgtest"
	"dev-digest-go/backend/internal/tenancy"
	tenancypg "dev-digest-go/backend/internal/tenancy/adapters/postgres"
)

func TestMain(m *testing.M) { os.Exit(pgtest.Main(m)) }

const (
	uiOrigin      = "http://localhost:3100"
	foreignOrigin = "http://evil.example"
	unknownID     = "00000000-0000-4000-8000-000000000001"
)

type queued struct {
	Workspace, Kind string
	Payload         any
}

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

type env struct {
	e      *echo.Echo
	db     *gorm.DB
	queue  *fakeQueue
	ws     string
	repo   string // cloned
	noClon string // not cloned
}

func newEnv(t *testing.T) *env {
	t.Helper()
	d := pgtest.Migrated(t)
	v := &env{db: d.DB, queue: &fakeQueue{}}
	var user string
	must(t, d.DB.Raw(`INSERT INTO workspaces (name) VALUES ('default') RETURNING id`).Scan(&v.ws).Error)
	must(t, d.DB.Raw(`INSERT INTO users (email, name) VALUES ('you@local', 'You') RETURNING id`).Scan(&user).Error)
	v.repo = v.addRepo(t, v.ws, "o/r", "/clones/o/r")
	v.noClon = v.addRepo(t, v.ws, "o/nc", "")

	store := pgstore.NewStore(d.DB)
	v.e = httpx.New(httpx.Options{Log: slog.New(slog.DiscardHandler), CORSOrigins: []string{uiOrigin}})
	conventionshttp.AddRoutes(v.e, app.NewService(store, store, v.queue, app.ServiceOptions{}),
		tenancy.NewResolver(tenancypg.NewStore(d.DB)))
	return v
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func (v *env) addRepo(t *testing.T, ws, name, clone string) string {
	t.Helper()
	var c *string
	if clone != "" {
		c = &clone
	}
	var id string
	must(t, v.db.Raw(`INSERT INTO repos (workspace_id, owner, name, full_name, clone_path) VALUES (?, 'o', ?, ?, ?) RETURNING id`,
		ws, name, name, c).Scan(&id).Error)
	return id
}

func (v *env) addConvention(t *testing.T, ws, repo, rule, status string, confidence *float64) string {
	t.Helper()
	var id string
	must(t, v.db.Raw(`INSERT INTO conventions (workspace_id, repo_id, rule, confidence, status, evidence)
		VALUES (?, ?, ?, ?, ?, '[{"path":"a.ts","start_line":1,"end_line":2,"snippet":"x"}]') RETURNING id`,
		ws, repo, rule, confidence, status).Scan(&id).Error)
	return id
}

func (v *env) addScan(t *testing.T, repo, status string, startedAt time.Time) string {
	t.Helper()
	var id string
	must(t, v.db.Raw(`INSERT INTO convention_scans (workspace_id, repo_id, status, started_at) VALUES (?, ?, ?, ?) RETURNING id`,
		v.ws, repo, status, startedAt).Scan(&id).Error)
	return id
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

type errorBody struct {
	Error struct {
		Code    string          `json:"code"`
		Message string          `json:"message"`
		Details json.RawMessage `json:"details"`
	} `json:"error"`
}

func wantError(t *testing.T, r response, status int, code, msg string) errorBody {
	t.Helper()
	e := decode[errorBody](t, r)
	if r.status != status || e.Error.Code != code || e.Error.Message != msg {
		t.Errorf("response = %d %s, want %d {code %q, message %q}", r.status, r.body, status, code, msg)
	}
	return e
}

func (v *env) count(t *testing.T, table string) int64 {
	t.Helper()
	var n int64
	must(t, v.db.Table(table).Count(&n).Error)
	return n
}

func f(x float64) *float64 { return &x }

// AC260
func TestGetConventions_ReturnsLatestScanAndOrderedRules(t *testing.T) {
	v := newEnv(t)
	empty := v.do(t, http.MethodGet, "/repos/"+v.repo+"/conventions", "")
	if empty.status != 200 || empty.body != `{"scan":null,"conventions":[]}` {
		t.Errorf("GET of an unscanned repo = %d %s, want 200 {\"scan\":null,\"conventions\":[]}", empty.status, empty.body)
	}

	v.addConvention(t, v.ws, v.repo, "pending 0.9", "pending", f(0.9))
	v.addConvention(t, v.ws, v.repo, "accepted 0.5", "accepted", f(0.5))
	v.addConvention(t, v.ws, v.repo, "rejected 0.8", "rejected", f(0.8))
	v.addScan(t, v.repo, "done", time.Now().Add(-time.Hour))
	newer := v.addScan(t, v.repo, "failed", time.Now())

	res := v.do(t, http.MethodGet, "/repos/"+v.repo+"/conventions", "")
	if res.status != 200 {
		t.Fatalf("GET status = %d (%s), want 200", res.status, res.body)
	}
	got := decode[struct {
		Scan        map[string]any   `json:"scan"`
		Conventions []map[string]any `json:"conventions"`
	}](t, res)
	if got.Scan["id"] != newer {
		t.Errorf("scan id = %v, want the newer scan %s", got.Scan["id"], newer)
	}
	var order []any
	for _, c := range got.Conventions {
		order = append(order, c["rule"])
	}
	if diff := cmp.Diff([]any{"accepted 0.5", "pending 0.9", "rejected 0.8"}, order); diff != "" {
		t.Errorf("convention order mismatch (-want +got):\n%s", diff)
	}
}

func TestGetConventions_WireShape(t *testing.T) {
	v := newEnv(t)
	id := v.addConvention(t, v.ws, v.repo, "a rule", "pending", nil) // NULL confidence is sent as 0
	scanID := v.addScan(t, v.repo, "running", time.Date(2026, 10, 22, 9, 15, 0, 123000000, time.UTC))

	got := decode[struct {
		Scan        map[string]any   `json:"scan"`
		Conventions []map[string]any `json:"conventions"`
	}](t, v.do(t, http.MethodGet, "/repos/"+v.repo+"/conventions", ""))

	wantScan := map[string]any{
		"id": scanID, "repo_id": v.repo, "status": "running", "sampled_files": []any{}, "proposed": float64(0),
		"kept": float64(0), "dropped": []any{}, "model": nil, "cost_usd": nil, "error": nil,
		"started_at": "2026-10-22T09:15:00.123Z", "finished_at": nil,
	}
	if diff := cmp.Diff(wantScan, got.Scan); diff != "" {
		t.Errorf("scan JSON mismatch (-want +got):\n%s", diff)
	}
	if len(got.Conventions) != 1 {
		t.Fatalf("conventions = %v, want 1", got.Conventions)
	}
	c := got.Conventions[0]
	wantKeys := []string{"id", "repo_id", "scan_id", "category", "rule", "evidence", "confidence", "status", "edited", "skill_id", "created_at", "updated_at"}
	for _, k := range wantKeys {
		if _, ok := c[k]; !ok {
			t.Errorf("convention JSON lacks key %q: %v", k, c)
		}
	}
	if len(c) != len(wantKeys) {
		t.Errorf("convention JSON has %d keys, want %d: %v", len(c), len(wantKeys), c)
	}
	wantEvidence := []any{map[string]any{"path": "a.ts", "start_line": float64(1), "end_line": float64(2), "snippet": "x"}}
	if c["id"] != id || c["confidence"] != float64(0) || c["scan_id"] != nil || c["skill_id"] != nil ||
		c["edited"] != false || cmp.Diff(wantEvidence, c["evidence"]) != "" {
		t.Errorf("convention = %v, want confidence 0, null scan_id and skill_id, the stored evidence", c)
	}
}

func TestGetConventions_Errors(t *testing.T) {
	v := newEnv(t)
	wantError(t, v.do(t, http.MethodGet, "/repos/"+unknownID+"/conventions", ""), 404, "not_found", "Repo not found")
	if res := v.do(t, http.MethodGet, "/repos/not-a-uuid/conventions", ""); res.status != 422 {
		t.Errorf("GET with a bad id = %d %s, want 422", res.status, res.body)
	}
}

// AC209: a repo of another workspace is a 404.
func TestRepoOfAnotherWorkspaceIsNotFound(t *testing.T) {
	v := newEnv(t)
	var other string
	must(t, v.db.Raw(`INSERT INTO workspaces (name) VALUES ('other') RETURNING id`).Scan(&other).Error)
	foreign := v.addRepo(t, other, "x/y", "/clones/x/y")
	wantError(t, v.do(t, http.MethodGet, "/repos/"+foreign+"/conventions", ""), 404, "not_found", "Repo not found")
	wantError(t, v.do(t, http.MethodPost, "/repos/"+foreign+"/conventions/extract", ""), 404, "not_found", "Repo not found")
	conv := v.addConvention(t, other, foreign, "foreign", "pending", nil)
	wantError(t, v.do(t, http.MethodPatch, "/conventions/"+conv, `{"status":"accepted"}`), 404, "not_found", "Convention not found")
}

// AC261, AC262, AC263
func TestExtract(t *testing.T) {
	t.Run("starts a scan with no body and no content type", func(t *testing.T) {
		v := newEnv(t)
		res := v.do(t, http.MethodPost, "/repos/"+v.repo+"/conventions/extract", "")
		if res.status != 202 {
			t.Fatalf("POST status = %d (%s), want 202", res.status, res.body)
		}
		scan := decode[map[string]any](t, res)
		if scan["status"] != "running" || scan["repo_id"] != v.repo || scan["proposed"] != float64(0) || scan["kept"] != float64(0) ||
			scan["finished_at"] != nil || cmp.Diff([]any{}, scan["sampled_files"]) != "" || cmp.Diff([]any{}, scan["dropped"]) != "" {
			t.Errorf("scan = %v, want a running, empty scan", scan)
		}
		if len(v.queue.jobs) != 1 || v.queue.jobs[0].Kind != "conventions.extract" {
			t.Fatalf("jobs = %+v, want one conventions.extract", v.queue.jobs)
		}
		raw, _ := json.Marshal(v.queue.jobs[0].Payload)
		want := `{"scanId":"` + scan["id"].(string) + `","workspaceId":"` + v.ws + `","repoId":"` + v.repo + `"}`
		if string(raw) != want {
			t.Errorf("job payload = %s, want %s", raw, want)
		}
	})

	t.Run("ignores a JSON body", func(t *testing.T) {
		v := newEnv(t)
		if res := v.do(t, http.MethodPost, "/repos/"+v.repo+"/conventions/extract", `{"anything":[1,2]}`); res.status != 202 {
			t.Errorf("POST with a body = %d %s, want 202", res.status, res.body)
		}
	})

	t.Run("repo without clone", func(t *testing.T) {
		v := newEnv(t)
		res := v.do(t, http.MethodPost, "/repos/"+v.noClon+"/conventions/extract", "")
		if res.status != 422 || res.body != `{"error":{"code":"not_cloned","message":"The repo is not cloned yet — wait for the clone to finish"}}` {
			t.Errorf("POST = %d %s, want 422 not_cloned", res.status, res.body)
		}
		if n := v.count(t, "convention_scans"); n != 0 {
			t.Errorf("scan rows = %d, want none", n)
		}
	})

	t.Run("unknown repo", func(t *testing.T) {
		v := newEnv(t)
		wantError(t, v.do(t, http.MethodPost, "/repos/"+unknownID+"/conventions/extract", ""), 404, "not_found", "Repo not found")
	})

	t.Run("running scan", func(t *testing.T) {
		v := newEnv(t)
		v.addScan(t, v.repo, "running", time.Now().Add(-time.Minute))
		wantError(t, v.do(t, http.MethodPost, "/repos/"+v.repo+"/conventions/extract", ""), 409, "scan_running",
			"A conventions scan is already running for this repo")
		if n := v.count(t, "convention_scans"); n != 1 || len(v.queue.jobs) != 0 {
			t.Errorf("scan rows = %d, jobs = %d, want 1 and 0", n, len(v.queue.jobs))
		}
	})

	t.Run("stale running scan is failed and replaced", func(t *testing.T) {
		v := newEnv(t)
		old := v.addScan(t, v.repo, "running", time.Now().Add(-11*time.Minute))
		if res := v.do(t, http.MethodPost, "/repos/"+v.repo+"/conventions/extract", ""); res.status != 202 {
			t.Fatalf("POST = %d %s, want 202", res.status, res.body)
		}
		var got struct {
			Status string
			Error  string
		}
		must(t, v.db.Raw(`SELECT status, error FROM convention_scans WHERE id = ?`, old).Scan(&got).Error)
		if got.Status != "failed" || got.Error != "The scan was interrupted" {
			t.Errorf("old scan = %+v, want failed with The scan was interrupted", got)
		}
	})

	t.Run("enqueue failure", func(t *testing.T) {
		v := newEnv(t)
		v.queue.err = errors.New("queue is shutting down")
		res := v.do(t, http.MethodPost, "/repos/"+v.repo+"/conventions/extract", "")
		wantError(t, res, 500, "internal_error", "Internal error")
		var got struct {
			Status string
			Error  string
		}
		must(t, v.db.Raw(`SELECT status, error FROM convention_scans`).Scan(&got).Error)
		if got.Status != "failed" || !strings.Contains(got.Error, "queue is shutting down") {
			t.Errorf("scan = %+v, want failed with the enqueue error", got)
		}
	})

	t.Run("bad repo id", func(t *testing.T) {
		v := newEnv(t)
		if res := v.do(t, http.MethodPost, "/repos/nope/conventions/extract", ""); res.status != 422 {
			t.Errorf("POST = %d %s, want 422", res.status, res.body)
		}
	})
}

// AC277
func TestPatch(t *testing.T) {
	v := newEnv(t)
	id := v.addConvention(t, v.ws, v.repo, "Original", "pending", f(0.7))
	patch := func(body string) response { return v.do(t, http.MethodPatch, "/conventions/"+id, body) }

	type conv struct {
		Status   string `json:"status"`
		Rule     string `json:"rule"`
		Category string `json:"category"`
		Edited   bool   `json:"edited"`
	}
	tests := []struct {
		name string
		body string
		want conv
	}{
		{"accept", `{"status":"accepted"}`, conv{"accepted", "Original", "other", false}},
		{"trimmed rule marks edited", `{"rule":"  New rule  "}`, conv{"accepted", "New rule", "other", true}},
		{"status change keeps edited", `{"status":"pending"}`, conv{"pending", "New rule", "other", true}},
		{"category", `{"category":"naming"}`, conv{"pending", "New rule", "naming", true}},
		{"unknown keys are ignored", `{"status":"rejected","extra":1}`, conv{"rejected", "New rule", "naming", true}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			res := patch(tc.body)
			if res.status != 200 {
				t.Fatalf("PATCH %s = %d %s, want 200", tc.body, res.status, res.body)
			}
			if got := decode[conv](t, res); got != tc.want {
				t.Errorf("PATCH %s = %+v, want %+v", tc.body, got, tc.want)
			}
		})
	}

	if c := decode[map[string]any](t, patch(`{"status":"accepted"}`)); c["confidence"] != 0.7 {
		t.Errorf("confidence after a patch = %v, want 0.7", c["confidence"])
	}
}

type issue struct {
	Keyword      string `json:"keyword"`
	InstancePath string `json:"instancePath"`
	Message      string `json:"message"`
}

func TestPatch_ValidationErrors(t *testing.T) {
	v := newEnv(t)
	id := v.addConvention(t, v.ws, v.repo, "Original", "pending", f(0.7))
	tests := []struct {
		name string
		body string
		ct   string
		want []issue
	}{
		{"empty object", `{}`, "", []issue{{"custom", "/", "Nothing to update"}}},
		{"only unknown keys", `{"x":1}`, "", []issue{{"custom", "/", "Nothing to update"}}},
		{"empty rule", `{"rule":""}`, "", []issue{{"too_small", "/rule", "String must contain at least 1 character(s)"}}},
		{"blank rule", `{"rule":"   "}`, "", []issue{{"too_small", "/rule", "String must contain at least 1 character(s)"}}},
		{"long rule", `{"rule":"` + strings.Repeat("a", 501) + `"}`, "", []issue{{"too_big", "/rule", "String must contain at most 500 character(s)"}}},
		{"bad status", `{"status":"done"}`, "", []issue{{"invalid_enum_value", "/status", "Invalid enum value. Expected 'pending' | 'accepted' | 'rejected', received 'done'"}}},
		{"bad category", `{"category":"x"}`, "", []issue{{"invalid_enum_value", "/category",
			"Invalid enum value. Expected 'naming' | 'structure' | 'error-handling' | 'async' | 'types' | 'imports' | 'testing' | 'api' | 'data-access' | 'style' | 'other', received 'x'"}}},
		{"status of the wrong type", `{"status":1}`, "", []issue{{"invalid_type", "/status", "Expected string, received number"}}},
		{"null rule", `{"rule":null}`, "", []issue{{"invalid_type", "/rule", "Expected string, received null"}}},
		{"all fields invalid", `{"status":"x","rule":"","category":false}`, "", []issue{
			{"invalid_enum_value", "/status", "Invalid enum value. Expected 'pending' | 'accepted' | 'rejected', received 'x'"},
			{"too_small", "/rule", "String must contain at least 1 character(s)"},
			{"invalid_type", "/category", "Expected string, received boolean"}}},
		{"array body", `[1]`, "", []issue{{"invalid_type", "/", "Expected object, received array"}}},
		{"null body", `null`, "", []issue{{"invalid_type", "/", "Expected object, received null"}}},
		{"text/plain JSON text", `{"status":"accepted"}`, "text/plain", []issue{{"invalid_type", "/", "Expected object, received string"}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			headers := []string{}
			if tc.ct != "" {
				headers = append(headers, "Content-Type", tc.ct)
			}
			res := v.do(t, http.MethodPatch, "/conventions/"+id, tc.body, headers...)
			e := wantError(t, res, 422, "validation_error", "Request validation failed")
			var got []issue
			if err := json.Unmarshal(e.Error.Details, &got); err != nil {
				t.Fatalf("details %s: %v", e.Error.Details, err)
			}
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("issues mismatch (-want +got):\n%s", diff)
			}
		})
	}

	t.Run("no body", func(t *testing.T) {
		res := v.do(t, http.MethodPatch, "/conventions/"+id, "")
		wantError(t, res, 422, "validation_error", "Request validation failed")
	})
	t.Run("bad id", func(t *testing.T) {
		if res := v.do(t, http.MethodPatch, "/conventions/nope", `{"status":"accepted"}`); res.status != 422 {
			t.Errorf("PATCH bad id = %d %s, want 422", res.status, res.body)
		}
	})
	t.Run("nothing changed after the failures", func(t *testing.T) {
		var got struct {
			Rule, Status string
			Edited       bool
		}
		must(t, v.db.Raw(`SELECT rule, status, edited FROM conventions WHERE id = ?`, id).Scan(&got).Error)
		if got.Rule != "Original" || got.Status != "pending" || got.Edited {
			t.Errorf("stored convention = %+v, want it untouched", got)
		}
	})
}

func TestPatch_UnknownConvention(t *testing.T) {
	v := newEnv(t)
	wantError(t, v.do(t, http.MethodPatch, "/conventions/"+unknownID, `{"status":"accepted"}`), 404, "not_found", "Convention not found")
}

// AC205: a foreign page cannot change a convention.
func TestPatch_ForeignOriginIsRefused(t *testing.T) {
	v := newEnv(t)
	id := v.addConvention(t, v.ws, v.repo, "Original", "pending", f(0.7))
	for _, ct := range []string{"application/json", "text/plain"} {
		res := v.do(t, http.MethodPatch, "/conventions/"+id, `{"status":"accepted"}`, "Content-Type", ct, "Origin", foreignOrigin)
		wantError(t, res, 403, "forbidden", res.messageOr(t))
	}
	res := v.do(t, http.MethodPost, "/repos/"+v.repo+"/conventions/extract", "", "Origin", foreignOrigin)
	if res.status != 403 || v.count(t, "convention_scans") != 0 {
		t.Errorf("extract from a foreign origin = %d %s, scans = %d, want 403 and no scan", res.status, res.body, v.count(t, "convention_scans"))
	}
	var status string
	must(t, v.db.Raw(`SELECT status FROM conventions WHERE id = ?`, id).Scan(&status).Error)
	if status != "pending" {
		t.Errorf("status after refused requests = %s, want pending", status)
	}
	if ok := v.do(t, http.MethodPatch, "/conventions/"+id, `{"status":"accepted"}`, "Origin", uiOrigin); ok.status != 200 {
		t.Errorf("PATCH from the UI origin = %d %s, want 200", ok.status, ok.body)
	}
}

// messageOr returns the message the refusal carries (its wording belongs to httpx).
func (r response) messageOr(t *testing.T) string {
	t.Helper()
	return decode[errorBody](t, r).Error.Message
}

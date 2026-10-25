package workspacehttp_test

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/labstack/echo/v5"

	"dev-digest-go/backend/internal/platform/apperr"
	"dev-digest-go/backend/internal/platform/httpx"
	"dev-digest-go/backend/internal/tenancy"
	"dev-digest-go/backend/internal/workspace/app"
	"dev-digest-go/backend/internal/workspace/domain"
	workspacehttp "dev-digest-go/backend/internal/workspace/transport/http"
)

const (
	workspaceID   = "11111111-2222-4333-8444-555555555555"
	userID        = "66666666-7777-4888-8999-000000000000"
	uiOrigin      = "http://localhost:3100"
	foreignOrigin = "http://evil.example"
)

// fakeService records the calls it gets and answers the configured values.
type fakeService struct {
	info    app.Info
	stats   domain.Stats
	file    app.ExportFile
	deleted domain.Deleted
	err     error

	calls       []string
	gotWS       []string
	gotUser     string
	gotName     string
	gotConfirm  string
	renameCalls int
	resetCalls  int
}

func (f *fakeService) record(call, ws string) {
	f.calls = append(f.calls, call)
	f.gotWS = append(f.gotWS, ws)
}

func (f *fakeService) Info(_ context.Context, ws string) (app.Info, error) {
	f.record("Info", ws)
	return f.info, f.err
}

func (f *fakeService) Rename(_ context.Context, ws, user, name string) (app.Info, error) {
	f.record("Rename", ws)
	f.renameCalls++
	f.gotUser, f.gotName = user, name
	return f.info, f.err
}

func (f *fakeService) Stats(_ context.Context, ws string) (domain.Stats, error) {
	f.record("Stats", ws)
	return f.stats, f.err
}

func (f *fakeService) Export(_ context.Context, ws string) (app.ExportFile, error) {
	f.record("Export", ws)
	return f.file, f.err
}

func (f *fakeService) Reset(_ context.Context, ws, confirm string) (domain.Deleted, error) {
	f.record("Reset", ws)
	f.resetCalls++
	f.gotConfirm = confirm
	return f.deleted, f.err
}

type fakeTenants struct{ err error }

func (f fakeTenants) Resolve(context.Context) (tenancy.Identity, error) {
	if f.err != nil {
		return tenancy.Identity{}, f.err
	}
	return tenancy.Identity{WorkspaceID: workspaceID, UserID: userID}, nil
}

func newEcho(svc *fakeService, tenants fakeTenants) *echo.Echo {
	e := httpx.New(httpx.Options{
		Log:          slog.New(slog.DiscardHandler),
		CORSOrigins:  []string{uiOrigin},
		AllowedHosts: []string{"example.com"},
	})
	workspacehttp.AddRoutes(e, svc, tenants)
	return e
}

type response struct {
	status int
	body   string
	header http.Header
}

// do sends one request with the allowed Host; headers are name/value pairs.
func do(t *testing.T, e *echo.Echo, method, path, body string, headers ...string) response {
	t.Helper()
	var req *http.Request
	if body == "" {
		req = httptest.NewRequest(method, path, nil)
	} else {
		req = httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
	}
	req.Host = "example.com"
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	return response{status: rec.Code, body: strings.TrimSpace(rec.Body.String()), header: rec.Header()}
}

func decode[T any](t *testing.T, r response) T {
	t.Helper()
	var v T
	if err := json.Unmarshal([]byte(r.body), &v); err != nil {
		t.Fatalf("response body %q is not the expected JSON: %v", r.body, err)
	}
	return v
}

func ptr[T any](v T) *T { return &v }

func sampleInfo() app.Info {
	polled := time.Date(2026, 10, 21, 9, 15, 0, 0, time.UTC)
	return app.Info{
		WorkspaceID: workspaceID,
		Name:        "Acme",
		DataDir:     "/data",
		CloneDir:    "/data/workspace",
		Repos: []app.RepoInfo{
			{ID: "r1", FullName: "acme/api", ClonePath: ptr("/data/workspace/acme/api"), LastPolledAt: &polled},
			{ID: "r2", FullName: "acme/web"},
		},
	}
}

const sampleInfoJSON = `{"workspaceId":"` + workspaceID + `","name":"Acme","dataDir":"/data","cloneDir":"/data/workspace","repos":[` +
	`{"id":"r1","full_name":"acme/api","clone_path":"/data/workspace/acme/api","last_polled_at":"2026-10-21T09:15:00.000Z","cloned":true},` +
	`{"id":"r2","full_name":"acme/web","clone_path":null,"last_polled_at":null,"cloned":false}]}`

// issueMessages returns the messages of the 422 details of a response.
func issueMessages(t *testing.T, r response) []string {
	t.Helper()
	got := decode[httpx.ErrorBody](t, r)
	details, _ := got.Error.Details.([]any)
	var out []string
	for _, d := range details {
		out = append(out, d.(map[string]any)["message"].(string))
	}
	return out
}

func TestInfo_Shape(t *testing.T) {
	t.Run("name, data dir, clone dir and repos", func(t *testing.T) {
		svc := &fakeService{info: sampleInfo()}
		r := do(t, newEcho(svc, fakeTenants{}), http.MethodGet, "/workspace", "")
		if r.status != http.StatusOK || r.body != sampleInfoJSON {
			t.Errorf("GET /workspace = %d %s, want 200 %s", r.status, r.body, sampleInfoJSON)
		}
		if diff := cmp.Diff([]string{workspaceID}, svc.gotWS); diff != "" {
			t.Errorf("Info() workspaces mismatch (-want +got):\n%s", diff)
		}
	})
	t.Run("no repos is an empty array", func(t *testing.T) {
		svc := &fakeService{info: app.Info{WorkspaceID: workspaceID, Name: "default", DataDir: "/data", CloneDir: "/data/workspace"}}
		r := do(t, newEcho(svc, fakeTenants{}), http.MethodGet, "/workspace", "")
		want := `{"workspaceId":"` + workspaceID + `","name":"default","dataDir":"/data","cloneDir":"/data/workspace","repos":[]}`
		if r.status != http.StatusOK || r.body != want {
			t.Errorf("GET /workspace = %d %s, want 200 %s", r.status, r.body, want)
		}
	})
}

func TestRename_PassesIdentityAndTextAsSent(t *testing.T) {
	svc := &fakeService{info: sampleInfo()}
	r := do(t, newEcho(svc, fakeTenants{}), http.MethodPut, "/workspace", `{"name":"  Acme  "}`, "Origin", uiOrigin)
	if r.status != http.StatusOK || r.body != sampleInfoJSON {
		t.Fatalf("PUT /workspace = %d %s, want 200 %s", r.status, r.body, sampleInfoJSON)
	}
	if svc.gotName != "  Acme  " || svc.gotUser != userID || svc.gotWS[0] != workspaceID {
		t.Errorf("Rename(ws=%v, user=%q, name=%q), want (%s, %s, the untrimmed text: the service trims)",
			svc.gotWS, svc.gotUser, svc.gotName, workspaceID, userID)
	}
}

func TestRename_ServiceRefusalIsTheEnvelope(t *testing.T) {
	issue := httpx.NewValidationError(httpx.Issue{Code: "custom", Path: []any{"name"}, Message: "bad name"})
	svc := &fakeService{err: issue}
	r := do(t, newEcho(svc, fakeTenants{}), http.MethodPut, "/workspace", `{"name":""}`)
	if r.status != http.StatusUnprocessableEntity {
		t.Fatalf("PUT /workspace status = %d (%s), want 422", r.status, r.body)
	}
	got := decode[httpx.ErrorBody](t, r)
	if got.Error.Code != "validation_error" || len(issueMessages(t, r)) != 1 {
		t.Errorf("PUT /workspace error = %+v, want validation_error with one issue", got.Error)
	}
}

// badBodies are the request bodies the handlers refuse before any use case runs: PUT wants
// {name: string}, POST reset-history {confirm: string}, nothing else.
func badBodies(field string) []struct {
	name, body, contentType string
	wantMessages            []string
} {
	return []struct {
		name, body, contentType string
		wantMessages            []string
	}{
		{"missing field", `{}`, "application/json", []string{"Required"}},
		{"field is a number", `{"` + field + `":5}`, "application/json", []string{"Expected string, received number"}},
		{"field is null", `{"` + field + `":null}`, "application/json", []string{"Expected string, received null"}},
		{"extra key", `{"` + field + `":"x","other":1}`, "application/json", []string{"Unrecognized key(s) in object: 'other'"}},
		{"only a foreign key", `{"other":1}`, "application/json", []string{"Required", "Unrecognized key(s) in object: 'other'"}},
		{"array body", `["x"]`, "application/json", []string{"Expected object, received array"}},
		{"string body", `"x"`, "application/json", []string{"Expected object, received string"}},
		{"null body", `null`, "application/json", []string{"Expected object, received null"}},
		{"text/plain body is a string", `{"` + field + `":"x"}`, "text/plain;charset=UTF-8", []string{"Expected object, received string"}},
	}
}

func TestBodies_AreRefusedBeforeTheUseCase(t *testing.T) {
	routes := []struct {
		method, path, field string
	}{
		{http.MethodPut, "/workspace", "name"},
		{http.MethodPost, "/workspace/reset-history", "confirm"},
	}
	for _, rt := range routes {
		for _, tc := range badBodies(rt.field) {
			t.Run(rt.method+" "+rt.path+" "+tc.name, func(t *testing.T) {
				svc := &fakeService{}
				r := do(t, newEcho(svc, fakeTenants{}), rt.method, rt.path, tc.body, "Content-Type", tc.contentType)
				if r.status != http.StatusUnprocessableEntity {
					t.Fatalf("%s %s status = %d (%s), want 422", rt.method, rt.path, r.status, r.body)
				}
				if got := decode[httpx.ErrorBody](t, r).Error.Code; got != "validation_error" {
					t.Errorf("error code = %q, want validation_error", got)
				}
				if diff := cmp.Diff(tc.wantMessages, issueMessages(t, r)); diff != "" {
					t.Errorf("issue messages mismatch (-want +got):\n%s", diff)
				}
				if len(svc.calls) != 0 {
					t.Errorf("use case calls = %v, want none", svc.calls)
				}
			})
		}
	}
}

func TestBodies_NoBodyIsRequired(t *testing.T) {
	for _, rt := range []struct{ method, path string }{
		{http.MethodPut, "/workspace"},
		{http.MethodPost, "/workspace/reset-history"},
	} {
		svc := &fakeService{}
		r := do(t, newEcho(svc, fakeTenants{}), rt.method, rt.path, "")
		if r.status != http.StatusUnprocessableEntity {
			t.Fatalf("%s %s without body status = %d (%s), want 422", rt.method, rt.path, r.status, r.body)
		}
		if diff := cmp.Diff([]string{"Required"}, issueMessages(t, r)); diff != "" {
			t.Errorf("%s %s issue messages mismatch (-want +got):\n%s", rt.method, rt.path, diff)
		}
		if len(svc.calls) != 0 {
			t.Errorf("%s %s use case calls = %v, want none", rt.method, rt.path, svc.calls)
		}
	}
}

func TestBodies_FrameworkRules(t *testing.T) {
	for _, rt := range []struct{ method, path string }{
		{http.MethodPut, "/workspace"},
		{http.MethodPost, "/workspace/reset-history"},
	} {
		tests := []struct {
			name, body, contentType string
			want                    int
		}{
			{"other media type", `<x/>`, "application/xml", http.StatusUnsupportedMediaType},
			{"form post", `confirm=x`, "application/x-www-form-urlencoded", http.StatusUnsupportedMediaType},
			{"invalid JSON", `{"name":`, "application/json", http.StatusBadRequest},
		}
		for _, tc := range tests {
			t.Run(rt.method+" "+tc.name, func(t *testing.T) {
				svc := &fakeService{}
				r := do(t, newEcho(svc, fakeTenants{}), rt.method, rt.path, tc.body, "Content-Type", tc.contentType)
				if r.status != tc.want {
					t.Errorf("%s %s (%s) status = %d (%s), want %d", rt.method, rt.path, tc.name, r.status, r.body, tc.want)
				}
				if len(svc.calls) != 0 {
					t.Errorf("use case calls = %v, want none", svc.calls)
				}
			})
		}
	}
}

func TestStats_Shape(t *testing.T) {
	svc := &fakeService{stats: domain.Stats{Agents: 3, Skills: 2, EvalCases: 5, Learnings: 7, Repos: 1, Runs: 120}}
	r := do(t, newEcho(svc, fakeTenants{}), http.MethodGet, "/workspace/stats", "")
	want := `{"agents":3,"skills":2,"eval_cases":5,"learnings":7,"repos":1,"runs":120}`
	if r.status != http.StatusOK || r.body != want {
		t.Errorf("GET /workspace/stats = %d %s, want 200 %s", r.status, r.body, want)
	}
	if diff := cmp.Diff([]string{workspaceID}, svc.gotWS); diff != "" {
		t.Errorf("Stats() workspaces mismatch (-want +got):\n%s", diff)
	}
}

func sampleFile() app.ExportFile {
	now := time.Date(2026, 10, 25, 12, 30, 45, 123_000_000, time.UTC)
	return app.ExportFile{
		FileName: domain.ExportFileName("Acme Corp", now),
		Bundle: domain.BuildBundle(domain.ExportData{
			Agents:   []domain.AgentRecord{{ID: "a1", Name: "Reviewer", Enabled: true}},
			Settings: map[string]json.RawMessage{"theme": json.RawMessage(`"dark"`)},
		}, domain.Meta{Name: "Acme Corp", Version: "", Now: now}),
	}
}

func TestExport_IsADownload(t *testing.T) {
	svc := &fakeService{file: sampleFile()}
	r := do(t, newEcho(svc, fakeTenants{}), http.MethodGet, "/workspace/export", "", "Origin", uiOrigin)
	if r.status != http.StatusOK {
		t.Fatalf("GET /workspace/export status = %d (%s), want 200", r.status, r.body)
	}
	wantHeaders := map[string]string{
		"Content-Type":                  "application/json",
		"Content-Disposition":           `attachment; filename="devdigest-workspace-acme-corp-2026-10-25.json"`,
		"Access-Control-Expose-Headers": "Content-Disposition",
		"Cache-Control":                 "no-store",
		"Access-Control-Allow-Origin":   uiOrigin,
	}
	for name, want := range wantHeaders {
		if got := r.header.Get(name); got != want {
			t.Errorf("GET /workspace/export header %s = %q, want %q", name, got, want)
		}
	}
	got := decode[map[string]any](t, r)
	manifest, _ := got["manifest"].(map[string]any)
	if manifest["kind"] != "workspace" || manifest["name"] != "Acme Corp" || manifest["format"] != "devdigest-plugin/v1" ||
		manifest["version"] != "dev" || manifest["exported_at"] != "2026-10-25T12:30:45.123Z" {
		t.Errorf("manifest = %v, want kind workspace, name Acme Corp, version dev, exported_at 2026-10-25T12:30:45.123Z", manifest)
	}
	agents, _ := got["agents"].([]any)
	if len(agents) != 1 || agents[0].(map[string]any)["name"] != "Reviewer" {
		t.Errorf("agents = %v, want the one agent", got["agents"])
	}
	if strings.Contains(r.body, `"a1"`) {
		t.Errorf("export body %s holds a row id, want none", r.body)
	}
	if diff := cmp.Diff([]string{workspaceID}, svc.gotWS); diff != "" {
		t.Errorf("Export() workspaces mismatch (-want +got):\n%s", diff)
	}
}

func TestExport_FileNameCannotBreakTheHeader(t *testing.T) {
	file := sampleFile()
	file.FileName = "a\"b\r\nSet-Cookie: x=1/../é.json"
	svc := &fakeService{file: file}
	r := do(t, newEcho(svc, fakeTenants{}), http.MethodGet, "/workspace/export", "")
	if r.status != http.StatusOK {
		t.Fatalf("GET /workspace/export status = %d (%s), want 200", r.status, r.body)
	}
	want := `attachment; filename="a_b__Set-Cookie__x_1_..__.json"`
	if got := r.header.Get("Content-Disposition"); got != want {
		t.Errorf("Content-Disposition = %q, want %q", got, want)
	}
	if got := r.header.Get("Set-Cookie"); got != "" {
		t.Errorf("Set-Cookie = %q, want none", got)
	}
}

func TestExport_FailureIsAnEnvelopeWithoutAttachmentHeaders(t *testing.T) {
	svc := &fakeService{err: errors.New("read export data: db down")}
	r := do(t, newEcho(svc, fakeTenants{}), http.MethodGet, "/workspace/export", "")
	want := `{"error":{"code":"internal_error","message":"Internal error"}}`
	if r.status != http.StatusInternalServerError || r.body != want {
		t.Errorf("GET /workspace/export = %d %s, want 500 %s", r.status, r.body, want)
	}
	if got := r.header.Get("Content-Disposition"); got != "" {
		t.Errorf("Content-Disposition on an error = %q, want none", got)
	}
}

func TestReset_Shape(t *testing.T) {
	svc := &fakeService{deleted: domain.Deleted{
		AgentRuns: 1, RunTraces: 2, Reviews: 3, Findings: 4, MultiAgentRuns: 5, EvalSuiteRuns: 6,
		EvalRuns: 7, CIRuns: 8, ComposedReviews: 9, Learnings: 10, PRsUnmarked: 11,
	}}
	r := do(t, newEcho(svc, fakeTenants{}), http.MethodPost, "/workspace/reset-history", `{"confirm":"Acme"}`, "Origin", uiOrigin)
	want := `{"deleted":{"agent_runs":1,"run_traces":2,"reviews":3,"findings":4,"multi_agent_runs":5,` +
		`"eval_suite_runs":6,"eval_runs":7,"ci_runs":8,"composed_reviews":9,"learnings":10},"prs_unmarked":11}`
	if r.status != http.StatusOK || r.body != want {
		t.Errorf("POST /workspace/reset-history = %d %s, want 200 %s", r.status, r.body, want)
	}
}

func TestReset_ConfirmIsPassedOnUntrimmed(t *testing.T) {
	svc := &fakeService{}
	r := do(t, newEcho(svc, fakeTenants{}), http.MethodPost, "/workspace/reset-history", `{"confirm":" Acme "}`)
	if r.status != http.StatusOK {
		t.Fatalf("POST /workspace/reset-history status = %d (%s), want 200", r.status, r.body)
	}
	if svc.gotConfirm != " Acme " || svc.gotWS[0] != workspaceID {
		t.Errorf("Reset(ws=%v, confirm=%q), want (%s, \" Acme \")", svc.gotWS, svc.gotConfirm, workspaceID)
	}
}

func TestReset_RefusalsAreEnvelopes(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantStatus int
		want       string
	}{
		{
			"confirmation mismatch",
			apperr.Validation("Type the workspace name exactly to confirm").WithCode(app.CodeConfirmationMismatch),
			http.StatusUnprocessableEntity,
			`{"error":{"code":"confirmation_mismatch","message":"Type the workspace name exactly to confirm"}}`,
		},
		{
			"run active",
			apperr.Conflict("2 runs are in progress — wait for them to finish or cancel them").WithCode(app.CodeRunActive),
			http.StatusConflict,
			`{"error":{"code":"run_active","message":"2 runs are in progress — wait for them to finish or cancel them"}}`,
		},
		{
			"failure",
			errors.New("reset run history: lock timeout"),
			http.StatusInternalServerError,
			`{"error":{"code":"internal_error","message":"Internal error"}}`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			svc := &fakeService{err: tc.err}
			r := do(t, newEcho(svc, fakeTenants{}), http.MethodPost, "/workspace/reset-history", `{"confirm":"x"}`)
			if r.status != tc.wantStatus || r.body != tc.want {
				t.Errorf("POST /workspace/reset-history = %d %s, want %d %s", r.status, r.body, tc.wantStatus, tc.want)
			}
		})
	}
}

// TestGuards covers the global guards on every route: the Host allow-list, and the Origin check
// plus the JSON-only rule on the state-changing routes (the cross-site "simple request" is
// text/plain without preflight).
func TestGuards_ForeignHostIsRefused(t *testing.T) {
	routes := []struct{ method, path, body string }{
		{http.MethodGet, "/workspace", ""},
		{http.MethodPut, "/workspace", `{"name":"x"}`},
		{http.MethodGet, "/workspace/stats", ""},
		{http.MethodGet, "/workspace/export", ""},
		{http.MethodPost, "/workspace/reset-history", `{"confirm":"x"}`},
	}
	for _, rt := range routes {
		t.Run(rt.method+" "+rt.path, func(t *testing.T) {
			svc := &fakeService{}
			e := newEcho(svc, fakeTenants{})
			req := httptest.NewRequest(rt.method, rt.path, strings.NewReader(rt.body))
			req.Header.Set("Content-Type", "application/json")
			req.Host = "rebind.evil.example"
			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, req)
			if rec.Code != http.StatusForbidden {
				t.Errorf("%s %s with a foreign Host = %d (%s), want 403", rt.method, rt.path, rec.Code, rec.Body.String())
			}
			if len(svc.calls) != 0 {
				t.Errorf("use case calls = %v, want none", svc.calls)
			}
		})
	}
}

func TestGuards_ForeignOriginIsRefusedOnStateChanges(t *testing.T) {
	routes := []struct{ method, path, body string }{
		{http.MethodPut, "/workspace", `{"name":"pwned"}`},
		{http.MethodPost, "/workspace/reset-history", `{"confirm":"default"}`},
	}
	for _, rt := range routes {
		for _, contentType := range []string{"text/plain;charset=UTF-8", "application/json"} {
			t.Run(rt.method+" "+rt.path+" "+contentType, func(t *testing.T) {
				svc := &fakeService{}
				r := do(t, newEcho(svc, fakeTenants{}), rt.method, rt.path, rt.body,
					"Content-Type", contentType, "Origin", foreignOrigin)
				want := `{"error":{"code":"forbidden","message":"Origin not allowed"}}`
				if r.status != http.StatusForbidden || r.body != want {
					t.Errorf("%s %s from a foreign Origin = %d %s, want 403 %s", rt.method, rt.path, r.status, r.body, want)
				}
				if len(svc.calls) != 0 {
					t.Errorf("use case calls = %v, want none", svc.calls)
				}
			})
		}
	}
}

func TestGuards_PreflightFromUIOriginIsAnswered(t *testing.T) {
	r := do(t, newEcho(&fakeService{}, fakeTenants{}), http.MethodOptions, "/workspace/reset-history", "",
		"Origin", uiOrigin, "Access-Control-Request-Method", "POST", "Access-Control-Request-Headers", "content-type")
	if r.status != http.StatusNoContent {
		t.Fatalf("OPTIONS /workspace/reset-history = %d (%s), want 204", r.status, r.body)
	}
	if got := r.header.Get("Access-Control-Allow-Origin"); got != uiOrigin {
		t.Errorf("Access-Control-Allow-Origin = %q, want %q", got, uiOrigin)
	}
}

func TestMethods_OtherVerbsAreNotRoutes(t *testing.T) {
	for _, rt := range []struct{ method, path string }{
		{http.MethodGet, "/workspace/reset-history"},
		{http.MethodPost, "/workspace/export"},
		{http.MethodPut, "/workspace/stats"},
		{http.MethodDelete, "/workspace"},
	} {
		svc := &fakeService{}
		r := do(t, newEcho(svc, fakeTenants{}), rt.method, rt.path, "")
		if r.status != http.StatusNotFound {
			t.Errorf("%s %s = %d (%s), want 404", rt.method, rt.path, r.status, r.body)
		}
		if len(svc.calls) != 0 {
			t.Errorf("%s %s use case calls = %v, want none", rt.method, rt.path, svc.calls)
		}
	}
}

func TestNotProvisioned_Is500(t *testing.T) {
	routes := []struct{ method, path, body string }{
		{http.MethodGet, "/workspace", ""},
		{http.MethodPut, "/workspace", `{"name":"x"}`},
		{http.MethodGet, "/workspace/stats", ""},
		{http.MethodGet, "/workspace/export", ""},
		{http.MethodPost, "/workspace/reset-history", `{"confirm":"x"}`},
	}
	for _, rt := range routes {
		svc := &fakeService{}
		r := do(t, newEcho(svc, fakeTenants{err: tenancy.ErrNotProvisioned}), rt.method, rt.path, rt.body)
		want := `{"error":{"code":"internal_error","message":"Internal error"}}`
		if r.status != http.StatusInternalServerError || r.body != want {
			t.Errorf("%s %s = %d %s, want 500 %s", rt.method, rt.path, r.status, r.body, want)
		}
		if len(svc.calls) != 0 {
			t.Errorf("%s %s use case calls = %v, want none", rt.method, rt.path, svc.calls)
		}
	}
}

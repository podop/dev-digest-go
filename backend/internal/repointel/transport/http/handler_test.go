package httpapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/labstack/echo/v5"

	"dev-digest-go/backend/internal/platform/httpx"
	"dev-digest-go/backend/internal/repointel/domain"
	httpapi "dev-digest-go/backend/internal/repointel/transport/http"
	"dev-digest-go/backend/internal/tenancy"
)

const (
	repoID        = "5f0c3a9e-1b2d-4c3e-8f4a-0123456789ab"
	workspaceID   = "11111111-2222-4333-8444-555555555555"
	uiOrigin      = "http://localhost:3100"
	foreignOrigin = "http://evil.example"
)

type resyncCall struct{ workspaceID, repoID string }

// fakeService answers IndexState from a map and records Resync calls.
type fakeService struct {
	mu     sync.Mutex
	states map[string]domain.StateView
	jobID  string
	err    error
	calls  []resyncCall
	reads  []string
}

func (f *fakeService) IndexState(_ context.Context, id string) domain.StateView {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads = append(f.reads, id)
	if v, ok := f.states[id]; ok {
		return v
	}
	return domain.Synthesised(id)
}

func (f *fakeService) Resync(_ context.Context, ws, id string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, resyncCall{ws, id})
	if f.err != nil {
		return "", f.err
	}
	return f.jobID, nil
}

type fakeTenants struct{ err error }

func (f fakeTenants) Resolve(context.Context) (tenancy.Identity, error) {
	if f.err != nil {
		return tenancy.Identity{}, f.err
	}
	return tenancy.Identity{WorkspaceID: workspaceID}, nil
}

func newServer(svc *fakeService, tenants fakeTenants) *echo.Echo {
	e := httpx.New(httpx.Options{
		Log:          slog.New(slog.DiscardHandler),
		CORSOrigins:  []string{uiOrigin},
		AllowedHosts: []string{"example.com"},
	})
	httpapi.AddRoutes(e, svc, tenants)
	return e
}

func do(t *testing.T, e *echo.Echo, method, path, body string, headers ...string) (int, string) {
	t.Helper()
	var req *http.Request
	if body == "" {
		req = httptest.NewRequest(method, path, nil)
	} else {
		req = httptest.NewRequest(method, path, strings.NewReader(body))
	}
	req.Host = "example.com"
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	return rec.Code, strings.TrimSpace(rec.Body.String())
}

var updatedAtPattern = regexp.MustCompile(`^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d\.\d{3}Z$`)

func TestIndexState_PersistedFullHasExactlyTheContractKeys(t *testing.T) {
	st := domain.IndexState{
		RepoID: repoID, LastIndexedSHA: "abc123", IndexerVersion: domain.IndexerVersion,
		Status: domain.StatusFull, FilesIndexed: 12, FilesSkipped: 3,
		Stats:     map[string]any{"durationMs": float64(1234), "bounded": float64(7), "softBudgetReached": false},
		UpdatedAt: time.Date(2026, 10, 22, 8, 30, 15, 123_000_000, time.UTC),
	}
	svc := &fakeService{states: map[string]domain.StateView{repoID: domain.NewStateView(st)}}

	code, body := do(t, newServer(svc, fakeTenants{}), http.MethodGet, "/repos/"+repoID+"/index-state", "")

	want := `{"repoId":"` + repoID + `","status":"full","filesIndexed":12,"filesSkipped":3,"durationMs":1234,` +
		`"lastIndexedSha":"abc123","indexerVersion":3,"updatedAt":"2026-10-22T08:30:15.123Z"}`
	if code != http.StatusOK {
		t.Fatalf("GET index-state status = %d (%s), want 200", code, body)
	}
	if diff := cmp.Diff(want, body); diff != "" {
		t.Errorf("GET index-state body mismatch (-want +got):\n%s", diff)
	}
	if got := strings.Split(strings.Split(body, `"updatedAt":"`)[1], `"`)[0]; !updatedAtPattern.MatchString(got) {
		t.Errorf("updatedAt = %q, want ISO-8601 UTC with milliseconds", got)
	}
}

func TestIndexState_DegradedAndFailedStates(t *testing.T) {
	updated := time.Date(2026, 1, 2, 3, 4, 5, 6_000_000, time.UTC)
	tests := []struct {
		name  string
		state domain.IndexState
		want  string
	}{
		{
			name: "degraded with reason and degradedReason",
			state: domain.IndexState{
				RepoID: repoID, IndexerVersion: 2, Status: domain.StatusDegraded, UpdatedAt: updated,
				Stats: map[string]any{"reason": "no_clone", "degradedReason": "no_data"},
			},
			want: `{"repoId":"` + repoID + `","status":"degraded","filesIndexed":0,"filesSkipped":0,"durationMs":0,` +
				`"reason":"no_clone","lastIndexedSha":"","indexerVersion":2,"updatedAt":"2026-01-02T03:04:05.006Z",` +
				`"degraded":true,"degradedReason":"no_data"}`,
		},
		{
			name: "failed without degradedReason",
			state: domain.IndexState{
				RepoID: repoID, IndexerVersion: 2, Status: domain.StatusFailed, UpdatedAt: updated,
				Stats: map[string]any{"reason": "index_failed"},
			},
			want: `{"repoId":"` + repoID + `","status":"failed","filesIndexed":0,"filesSkipped":0,"durationMs":0,` +
				`"reason":"index_failed","lastIndexedSha":"","indexerVersion":2,"updatedAt":"2026-01-02T03:04:05.006Z",` +
				`"degraded":true,"degradedReason":"index_failed"}`,
		},
		{
			name: "partial keeps an empty reason and carries no degraded keys",
			state: domain.IndexState{
				RepoID: repoID, IndexerVersion: 2, Status: domain.StatusPartial, UpdatedAt: updated,
				Stats: map[string]any{"degradedReason": "no_data", "reason": ""},
			},
			want: `{"repoId":"` + repoID + `","status":"partial","filesIndexed":0,"filesSkipped":0,"durationMs":0,` +
				`"reason":"","lastIndexedSha":"","indexerVersion":2,"updatedAt":"2026-01-02T03:04:05.006Z"}`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			svc := &fakeService{states: map[string]domain.StateView{repoID: domain.NewStateView(tc.state)}}
			code, body := do(t, newServer(svc, fakeTenants{}), http.MethodGet, "/repos/"+repoID+"/index-state", "")
			if code != http.StatusOK {
				t.Fatalf("GET index-state status = %d (%s), want 200", code, body)
			}
			if diff := cmp.Diff(tc.want, body); diff != "" {
				t.Errorf("GET index-state body mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestIndexState_UnknownRepoIsSynthesisedWithTheIdAsSent(t *testing.T) {
	upper := "5F0C3A9E-1B2D-4C3E-8F4A-0123456789AB"
	svc := &fakeService{}

	code, body := do(t, newServer(svc, fakeTenants{err: errors.New("tenancy is not consulted")}), http.MethodGet, "/repos/"+upper+"/index-state", "")

	want := `{"repoId":"` + upper + `","status":"degraded","filesIndexed":0,"filesSkipped":0,"durationMs":0,` +
		`"reason":"no_data","lastIndexedSha":"","indexerVersion":3,"updatedAt":"1970-01-01T00:00:00.000Z",` +
		`"degraded":true,"degradedReason":"no_data"}`
	if code != http.StatusOK {
		t.Fatalf("GET index-state status = %d (%s), want 200", code, body)
	}
	if diff := cmp.Diff(want, body); diff != "" {
		t.Errorf("GET index-state body mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff([]string{upper}, svc.reads); diff != "" {
		t.Errorf("service reads mismatch (-want +got):\n%s", diff)
	}
}

func TestRoutes_NonUUIDIDIs422(t *testing.T) {
	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/repos/abc/index-state"},
		{http.MethodPost, "/repos/abc/resync"},
	} {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			svc := &fakeService{}
			code, body := do(t, newServer(svc, fakeTenants{}), tc.method, tc.path, "")
			if code != http.StatusUnprocessableEntity {
				t.Fatalf("%s %s status = %d (%s), want 422", tc.method, tc.path, code, body)
			}
			var got httpx.ErrorBody
			mustDecode(t, body, &got)
			details, _ := got.Error.Details.([]any)
			if got.Error.Code != "validation_error" || len(details) != 1 {
				t.Fatalf("%s %s error = %+v, want validation_error with one issue", tc.method, tc.path, got.Error)
			}
			if path := details[0].(map[string]any)["instancePath"]; path != "/id" {
				t.Errorf("%s %s instancePath = %v, want /id", tc.method, tc.path, path)
			}
			if len(svc.reads)+len(svc.calls) != 0 {
				t.Errorf("service was called for an invalid id: reads %v, calls %v", svc.reads, svc.calls)
			}
		})
	}
}

func TestResync_AcceptedAnswersTheJobID(t *testing.T) {
	svc := &fakeService{jobID: "9a1b2c3d-0000-4000-8000-000000000001"}

	code, body := do(t, newServer(svc, fakeTenants{}), http.MethodPost, "/repos/"+repoID+"/resync", "")

	if code != http.StatusAccepted {
		t.Fatalf("POST resync status = %d (%s), want 202", code, body)
	}
	if want := `{"status":"accepted","jobId":"9a1b2c3d-0000-4000-8000-000000000001"}`; body != want {
		t.Errorf("POST resync body = %s, want %s", body, want)
	}
	if diff := cmp.Diff([]resyncCall{{workspaceID, repoID}}, svc.calls, cmp.AllowUnexported(resyncCall{})); diff != "" {
		t.Errorf("Resync calls mismatch (-want +got):\n%s", diff)
	}
}

func TestResync_EnqueueFailureIsStill202(t *testing.T) {
	svc := &fakeService{err: errors.New("runner is shutting down")}

	code, body := do(t, newServer(svc, fakeTenants{}), http.MethodPost, "/repos/"+repoID+"/resync", "")

	if code != http.StatusAccepted {
		t.Fatalf("POST resync status = %d (%s), want 202", code, body)
	}
	if want := `{"status":"accepted","degraded":true,"reason":"no_handler"}`; body != want {
		t.Errorf("POST resync body = %s, want %s", body, want)
	}
}

func TestResync_TenancyFailureIsAnError(t *testing.T) {
	svc := &fakeService{}
	code, body := do(t, newServer(svc, fakeTenants{err: tenancy.ErrNotProvisioned}), http.MethodPost, "/repos/"+repoID+"/resync", "")
	if code != http.StatusInternalServerError {
		t.Errorf("POST resync status = %d (%s), want 500", code, body)
	}
	if len(svc.calls) != 0 {
		t.Errorf("Resync calls = %v, want none", svc.calls)
	}
}

func TestResync_BodyIsIgnored(t *testing.T) {
	tests := []struct {
		name, contentType, body string
	}{
		{"json object", "application/json", `{"repoId":"other","force":true}`},
		{"json array", "application/json; charset=utf-8", `[1,2,3]`},
		{"plain text", "text/plain", `not json at all`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			svc := &fakeService{jobID: "job-1"}
			code, body := do(t, newServer(svc, fakeTenants{}), http.MethodPost, "/repos/"+repoID+"/resync", tc.body,
				"Content-Type", tc.contentType, "Origin", uiOrigin)
			if code != http.StatusAccepted {
				t.Fatalf("POST resync status = %d (%s), want 202", code, body)
			}
			if diff := cmp.Diff([]resyncCall{{workspaceID, repoID}}, svc.calls, cmp.AllowUnexported(resyncCall{})); diff != "" {
				t.Errorf("Resync calls mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestResync_ForeignOriginIsRefused(t *testing.T) {
	for _, contentType := range []string{"", "text/plain"} {
		svc := &fakeService{jobID: "job-1"}
		headers := []string{"Origin", foreignOrigin}
		if contentType != "" {
			headers = append(headers, "Content-Type", contentType)
		}
		code, body := do(t, newServer(svc, fakeTenants{}), http.MethodPost, "/repos/"+repoID+"/resync", "", headers...)
		if code != http.StatusForbidden {
			t.Errorf("POST resync from a foreign origin (Content-Type %q) = %d (%s), want 403", contentType, code, body)
		}
		if len(svc.calls) != 0 {
			t.Errorf("Resync calls = %v, want none", svc.calls)
		}
	}
}

func TestRoutes_UnlistedHostIsRefused(t *testing.T) {
	e := newServer(&fakeService{}, fakeTenants{})
	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/repos/" + repoID + "/index-state"},
		{http.MethodPost, "/repos/" + repoID + "/resync"},
	} {
		req := httptest.NewRequest(tc.method, tc.path, nil)
		req.Host = "rebind.evil.example"
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s %s with a foreign Host = %d, want 403", tc.method, tc.path, rec.Code)
		}
	}
}

func mustDecode(t *testing.T, body string, v any) {
	t.Helper()
	if err := json.Unmarshal([]byte(body), v); err != nil {
		t.Fatalf("response body %q is not the expected JSON: %v", body, err)
	}
}

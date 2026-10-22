package onboardinghttp_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/onboarding/app"
	"dev-digest-go/backend/internal/onboarding/domain"
	onboardinghttp "dev-digest-go/backend/internal/onboarding/transport/http"
	"dev-digest-go/backend/internal/platform/apperr"
	"dev-digest-go/backend/internal/platform/httpx"
	"dev-digest-go/backend/internal/tenancy"
)

const (
	workspaceID = "11111111-2222-4333-8444-555555555555"
	repoID      = "7e1d0000-0000-4000-8000-000000000001"
	uiOrigin    = "http://localhost:3100"
)

type call struct{ Method, WorkspaceID, RepoID string }

type fakeAPI struct {
	mu    sync.Mutex
	calls []call
	state domain.State
	err   error
}

var _ app.API = (*fakeAPI)(nil)

func (f *fakeAPI) record(c call) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, c)
}

func (f *fakeAPI) recorded() []call {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]call(nil), f.calls...)
}

func (f *fakeAPI) Get(_ context.Context, ws, id string) (domain.State, error) {
	f.record(call{"Get", ws, id})
	return f.state, f.err
}

func (f *fakeAPI) Generate(_ context.Context, ws, id string) (domain.State, error) {
	f.record(call{"Generate", ws, id})
	return f.state, f.err
}

type fakeTenants struct{}

func (fakeTenants) Resolve(context.Context) (tenancy.Identity, error) {
	return tenancy.Identity{WorkspaceID: workspaceID}, nil
}

func newServer(t *testing.T, api app.API) *httptest.Server {
	t.Helper()
	e := httpx.New(httpx.Options{
		Log:          slog.New(slog.DiscardHandler),
		CORSOrigins:  []string{uiOrigin},
		AllowedHosts: []string{"127.0.0.1"},
	})
	onboardinghttp.AddRoutes(e, api, fakeTenants{})
	return newServerFor(t, e)
}

func newServerFor(t *testing.T, h http.Handler) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv
}

type response struct {
	Status int
	Body   string
}

func do(t *testing.T, srv *httptest.Server, method, path, contentType, body string, headers map[string]string) response {
	t.Helper()
	var rdr io.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	}
	req, err := http.NewRequestWithContext(t.Context(), method, srv.URL+path, rdr)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			t.Errorf("close body: %v", err)
		}
	}()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body of %s %s: %v", method, path, err)
	}
	return response{Status: resp.StatusCode, Body: string(b)}
}

func sampleTour() domain.Tour {
	return domain.Tour{
		RepoID: repoID, GeneratedAt: time.Date(2026, 1, 1, 12, 0, 0, 5_000_000, time.UTC), IndexedSHA: "abc123",
		FilesIndexed: 42, Provider: "openrouter", Model: "m", TokensIn: 10, TokensOut: 20,
		PromptVersion: 1, Language: "en",
		Architecture: domain.Architecture{
			Summary: "s",
			Nodes:   []domain.Node{{ID: "a", Label: "A", Kind: domain.KindEntry}, {ID: "b", Label: "B", Kind: domain.KindStore}},
			Edges:   []domain.Edge{{From: "a", To: "b"}},
		},
		CriticalPaths: []domain.PathItem{{Path: "a.ts", Reason: "r"}},
		RunSteps:      []domain.RunStep{{Command: "make"}, {Command: "make run", Comment: "go"}},
		ReadingPath:   []domain.PathItem{},
		FirstTasks:    []domain.FirstTask{{Title: "t", Path: "a.ts", Complexity: domain.ComplexityHigh}},
	}
}

const readyJSON = `{"status":"ready","stale":false,"tour":{"repo_id":"` + repoID + `","generated_at":"2026-01-01T12:00:00.005Z",` +
	`"indexed_sha":"abc123","files_indexed":42,"provider":"openrouter","model":"m","tokens_in":10,"tokens_out":20,` +
	`"cost_usd":null,"prompt_version":1,"language":"en",` +
	`"architecture":{"summary":"s","nodes":[{"id":"a","label":"A","kind":"entry"},{"id":"b","label":"B","kind":"store"}],"edges":[{"from":"a","to":"b"}]},` +
	`"critical_paths":[{"path":"a.ts","reason":"r"}],"run_steps":[{"command":"make"},{"command":"make run","comment":"go"}],` +
	`"reading_path":[],"first_tasks":[{"title":"t","path":"a.ts","complexity":"high"}]}}`

func TestGet_Shapes(t *testing.T) {
	tour := sampleTour()
	staleTour := domain.State{Tour: &tour, Stale: true, StaleReason: domain.StaleIndexChanged}
	freshTour := domain.State{Tour: &tour}
	tests := []struct {
		name  string
		state domain.State
		want  string
	}{
		{"none", domain.State{}, `{"status":"none"}`},
		{"ready: key order, null cost, [] lists, no stale_reason", freshTour, readyJSON},
		{"stale: reason right after stale", staleTour, strings.Replace(readyJSON, `"stale":false,`, `"stale":true,"stale_reason":"index_changed",`, 1)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			api := &fakeAPI{state: tc.state}
			srv := newServer(t, api)
			got := do(t, srv, http.MethodGet, "/repos/"+repoID+"/onboarding", "", "", nil)
			if got.Status != http.StatusOK {
				t.Fatalf("GET status = %d (%s), want 200", got.Status, got.Body)
			}
			if got.Body != tc.want+"\n" {
				t.Errorf("GET body =\n%s\nwant\n%s", got.Body, tc.want)
			}
			if diff := cmp.Diff([]call{{"Get", workspaceID, repoID}}, api.recorded()); diff != "" {
				t.Errorf("use-case calls mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestPost_GeneratesWithoutReadingTheBody(t *testing.T) {
	tour := sampleTour()
	tests := []struct {
		name, contentType, body string
	}{
		{"no body, no content type (the UI)", "", ""},
		{"text body", "text/plain", "ignore me"},
		{"json body", "application/json", `{"force":true}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			api := &fakeAPI{state: domain.State{Tour: &tour}}
			srv := newServer(t, api)
			got := do(t, srv, http.MethodPost, "/repos/"+repoID+"/onboarding", tc.contentType, tc.body, map[string]string{"Origin": uiOrigin})
			if got.Status != http.StatusOK || got.Body != readyJSON+"\n" {
				t.Errorf("POST = %d %s, want 200 with the ready tour", got.Status, got.Body)
			}
			if diff := cmp.Diff([]call{{"Generate", workspaceID, repoID}}, api.recorded()); diff != "" {
				t.Errorf("use-case calls mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestPost_ForeignOriginIs403BeforeAnyWork(t *testing.T) {
	api := &fakeAPI{}
	srv := newServer(t, api)
	got := do(t, srv, http.MethodPost, "/repos/"+repoID+"/onboarding", "", "", map[string]string{"Origin": "https://evil.example"})
	if got.Status != http.StatusForbidden {
		t.Errorf("POST with a foreign Origin = %d %s, want 403", got.Status, got.Body)
	}
	if calls := api.recorded(); len(calls) != 0 {
		t.Errorf("use-case calls = %v, want none", calls)
	}
}

func TestRoutes_ANonUUIDIdIs422WithTheInstancePath(t *testing.T) {
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		t.Run(method, func(t *testing.T) {
			api := &fakeAPI{}
			srv := newServer(t, api)
			got := do(t, srv, method, "/repos/not-a-uuid/onboarding", "", "", map[string]string{"Origin": uiOrigin})
			var body struct {
				Error struct {
					Code    string `json:"code"`
					Details []struct {
						InstancePath string `json:"instancePath"`
					} `json:"details"`
				} `json:"error"`
			}
			if err := json.Unmarshal([]byte(got.Body), &body); err != nil {
				t.Fatalf("decode %q: %v", got.Body, err)
			}
			if got.Status != http.StatusUnprocessableEntity || body.Error.Code != "validation_error" ||
				len(body.Error.Details) != 1 || body.Error.Details[0].InstancePath != "/id" {
				t.Errorf("%s = %d %s, want 422 validation_error at /id", method, got.Status, got.Body)
			}
			if calls := api.recorded(); len(calls) != 0 {
				t.Errorf("use-case calls = %v, want none", calls)
			}
		})
	}
}

func TestPost_ErrorsRenderTheEnvelope(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantStatus int
		want       string
	}{
		{"repo not found", apperr.NotFound("Repo not found").WithCode("repo_not_found"), 404,
			`{"error":{"code":"repo_not_found","message":"Repo not found"}}`},
		{"already running", apperr.Conflict("A tour is already being generated for this repository").WithCode("generation_in_progress"), 409,
			`{"error":{"code":"generation_in_progress","message":"A tour is already being generated for this repository"}}`},
		{"index not ready", apperr.Validation("The repository index is not ready yet").WithCode("index_not_ready"), 422,
			`{"error":{"code":"index_not_ready","message":"The repository index is not ready yet"}}`},
		{"provider not configured", apperr.Validation("The openai provider is not configured").WithCode("provider_not_configured").
			WithDetails(map[string]string{"provider": "openai"}), 422,
			`{"error":{"code":"provider_not_configured","message":"The openai provider is not configured","details":{"provider":"openai"}}}`},
		{"model call failed", apperr.ExternalService("The model call failed").WithCode("generation_failed").
			WithCause(io.ErrUnexpectedEOF), 502,
			`{"error":{"code":"generation_failed","message":"The model call failed"}}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := newServer(t, &fakeAPI{err: tc.err})
			got := do(t, srv, http.MethodPost, "/repos/"+repoID+"/onboarding", "", "", map[string]string{"Origin": uiOrigin})
			if got.Status != tc.wantStatus || strings.TrimSpace(got.Body) != tc.want {
				t.Errorf("POST = %d %s, want %d %s", got.Status, got.Body, tc.wantStatus, tc.want)
			}
		})
	}
}

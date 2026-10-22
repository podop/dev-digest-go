package briefhttp_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/brief/app"
	"dev-digest-go/backend/internal/brief/domain"
	briefhttp "dev-digest-go/backend/internal/brief/transport/http"
	"dev-digest-go/backend/internal/platform/apperr"
	"dev-digest-go/backend/internal/platform/httpx"
	reviewer "dev-digest-go/backend/internal/reviewer/domain"
	"dev-digest-go/backend/internal/tenancy"
)

const (
	workspaceID = "11111111-2222-4333-8444-555555555555"
	prID        = "7e1d0000-0000-4000-8000-000000000001"
)

type fakeAPI struct {
	mu       sync.Mutex
	state    domain.State
	err      error
	gets     int
	gens     int
	gotWS    string
	gotPR    string
	lastCall string
}

var _ app.API = (*fakeAPI)(nil)

func (f *fakeAPI) Get(_ context.Context, ws, pr string) (domain.State, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.gets++
	f.gotWS, f.gotPR, f.lastCall = ws, pr, "get"
	return f.state, f.err
}

func (f *fakeAPI) Generate(_ context.Context, ws, pr string) (domain.State, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.gens++
	f.gotWS, f.gotPR, f.lastCall = ws, pr, "generate"
	return f.state, f.err
}

type fakeTenants struct{}

func (fakeTenants) Resolve(context.Context) (tenancy.Identity, error) {
	return tenancy.Identity{WorkspaceID: workspaceID}, nil
}

// do sends one request through a real Echo instance with the API-wide middleware and returns the
// status and the trimmed body.
func do(t *testing.T, api *fakeAPI, method, path string, build func(*http.Request)) (int, string) {
	t.Helper()
	e := httpx.New(httpx.Options{
		Log:          slog.New(slog.DiscardHandler),
		AllowedHosts: []string{"127.0.0.1"},
	})
	briefhttp.AddRoutes(e, api, fakeTenants{})
	srv := httptest.NewServer(e)
	t.Cleanup(srv.Close)
	req, err := http.NewRequestWithContext(t.Context(), method, srv.URL+path, nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if build != nil {
		build(req)
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
		t.Fatalf("read body: %v", err)
	}
	return resp.StatusCode, strings.TrimSpace(string(b))
}

func ptr[T any](v T) *T { return &v }

func fullBrief() domain.Brief {
	return domain.Brief{
		Summary: "Adds a limiter.",
		Risks: []domain.Risk{{Kind: "security", Title: "Header trusted", Explanation: "Spoofable key.",
			Severity: domain.RiskHigh, FileRefs: []string{"src/limit.ts:6-9"}}},
		ReviewFocus: []domain.FocusItem{{File: "src/limit.ts", Line: 7, Reason: "Key from a header."}},
		Intent: &reviewer.Intent{
			Text: "Rate limit", InScope: []string{"limiter"}, ChangeType: ptr("feature"), Confidence: ptr("high"), DerivedFrom: "explicit",
			Sources: []reviewer.IntentSource{{Kind: "title", Ref: "title", Status: "used"}, {Kind: "ticket", Ref: "#12", Status: "failed", Detail: ptr("not found")}},
		},
		Blast: &domain.Blast{
			ChangedSymbols: []domain.BlastSymbol{{Name: "limit", File: "src/limit.ts", Kind: "function"}},
			Downstream:     []domain.BlastGroup{{Symbol: "limit", Callers: []domain.BlastCaller{{Name: "register", File: "src/server.ts", Line: 22}}, EndpointsAffected: []string{"GET /health"}}},
			Summary:        "1 changed symbol",
		},
		MissingInputs: []domain.MissingInput{{Input: domain.InputSpecs, Reason: domain.ReasonUnreadable, Detail: "1 of 4"}, {Input: domain.InputLinkedIssue, Reason: domain.ReasonNotLinked}},
		SpecsUsed:     []string{"docs/a.md"},
		HeadSHA:       "0123456789abcdef0123456789abcdef01234567",
		GeneratedAt:   time.Date(2026, 1, 1, 12, 0, 0, 5_000_000, time.UTC),
		PromptVersion: "v1",
		Provider:      "openai",
		Model:         "gpt-4.1",
		TokensIn:      1200,
		TokensOut:     180,
		CostUSD:       ptr(0.00042),
		ModelRequests: 1,
	}
}

const fullBriefJSON = `{"summary":"Adds a limiter.",` +
	`"risks":{"risks":[{"kind":"security","title":"Header trusted","explanation":"Spoofable key.","severity":"high","file_refs":["src/limit.ts:6-9"]}]},` +
	`"review_focus":[{"file":"src/limit.ts","line":7,"reason":"Key from a header."}],` +
	`"intent":{"intent":"Rate limit","in_scope":["limiter"],"out_of_scope":[],"change_type":"feature","confidence":"high","derived_from":"explicit",` +
	`"sources":[{"kind":"title","ref":"title","status":"used","detail":null},{"kind":"ticket","ref":"#12","status":"failed","detail":"not found"}]},` +
	`"blast":{"changed_symbols":[{"name":"limit","file":"src/limit.ts","kind":"function"}],` +
	`"downstream":[{"symbol":"limit","callers":[{"name":"register","file":"src/server.ts","line":22}],"endpoints_affected":["GET /health"],"crons_affected":[]}],` +
	`"summary":"1 changed symbol","degraded":false},` +
	`"missing_inputs":[{"input":"specs","reason":"unreadable","detail":"1 of 4"},{"input":"linked_issue","reason":"not_linked"}],` +
	`"specs_used":["docs/a.md"],"head_sha":"0123456789abcdef0123456789abcdef01234567","generated_at":"2026-01-01T12:00:00.005Z",` +
	`"prompt_version":"v1","provider":"openai","model":"gpt-4.1","tokens_in":1200,"tokens_out":180,"cost_usd":0.00042,"model_requests":1}`

func TestGet_WireShape(t *testing.T) {
	b := fullBrief()
	api := &fakeAPI{state: domain.State{Brief: &b, Stale: true}}
	status, body := do(t, api, http.MethodGet, "/pulls/"+prID+"/brief", nil)
	want := `{"brief":` + fullBriefJSON + `,"stale":true}`
	if status != http.StatusOK {
		t.Fatalf("status = %d (%s), want 200", status, body)
	}
	if diff := cmp.Diff(want, body); diff != "" {
		t.Errorf("body mismatch (-want +got):\n%s", diff)
	}
	if api.gotWS != workspaceID || api.gotPR != prID || api.gets != 1 || api.gens != 0 {
		t.Errorf("calls = get %d, generate %d for (%s, %s), want one Get for (%s, %s)", api.gets, api.gens, api.gotWS, api.gotPR, workspaceID, prID)
	}
}

func TestGet_NoBrief(t *testing.T) {
	status, body := do(t, &fakeAPI{}, http.MethodGet, "/pulls/"+prID+"/brief", nil)
	if status != http.StatusOK || body != `{"brief":null,"stale":false}` {
		t.Errorf("GET = %d %s, want 200 {\"brief\":null,\"stale\":false}", status, body)
	}
}

func TestWire_NilListsAreEmptyListsAndNullablesArePresent(t *testing.T) {
	b := domain.Brief{
		Summary: "S", HeadSHA: "abc", GeneratedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), PromptVersion: "v1", Provider: "p", Model: "m", ModelRequests: 2,
		Intent: &reviewer.Intent{Text: "I"}, Blast: &domain.Blast{Summary: "s", Degraded: true, Reason: "no_data"},
	}
	_, body := do(t, &fakeAPI{state: domain.State{Brief: &b}}, http.MethodGet, "/pulls/"+prID+"/brief", nil)
	want := `{"brief":{"summary":"S","risks":{"risks":[]},"review_focus":[],` +
		`"intent":{"intent":"I","in_scope":[],"out_of_scope":[],"change_type":null,"confidence":null,"derived_from":null,"sources":[]},` +
		`"blast":{"changed_symbols":[],"downstream":[],"summary":"s","degraded":true,"reason":"no_data"},` +
		`"missing_inputs":[],"specs_used":[],"head_sha":"abc","generated_at":"2026-01-01T00:00:00.000Z","prompt_version":"v1",` +
		`"provider":"p","model":"m","tokens_in":0,"tokens_out":0,"cost_usd":null,"model_requests":2},"stale":false}`
	if diff := cmp.Diff(want, body); diff != "" {
		t.Errorf("body mismatch (-want +got):\n%s", diff)
	}
}

func TestPost_GeneratesAndReturnsTheBrief(t *testing.T) {
	b := fullBrief()
	api := &fakeAPI{state: domain.State{Brief: &b}}
	status, body := do(t, api, http.MethodPost, "/pulls/"+prID+"/brief", nil)
	if want := `{"brief":` + fullBriefJSON + `,"stale":false}`; status != http.StatusOK || body != want {
		t.Errorf("POST = %d %s, want 200 %s", status, body, want)
	}
	if api.gens != 1 || api.gets != 0 || api.gotWS != workspaceID || api.gotPR != prID {
		t.Errorf("calls = get %d, generate %d for (%s, %s), want one Generate", api.gets, api.gens, api.gotWS, api.gotPR)
	}
}

// The three POST routes accept no body and no content type, and ignore any body they receive.
func TestPost_IgnoresTheBody(t *testing.T) {
	tests := []struct {
		name  string
		build func(*http.Request)
	}{
		{"no body", nil},
		{"json body", func(r *http.Request) {
			r.Header.Set("Content-Type", "application/json")
			r.Body = io.NopCloser(strings.NewReader(`{"force":true}`))
			r.ContentLength = 14
		}},
		{"text body", func(r *http.Request) {
			r.Header.Set("Content-Type", "text/plain")
			r.Body = io.NopCloser(strings.NewReader(`hello`))
			r.ContentLength = 5
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			b := fullBrief()
			api := &fakeAPI{state: domain.State{Brief: &b}}
			status, body := do(t, api, http.MethodPost, "/pulls/"+prID+"/brief", tc.build)
			if status != http.StatusOK || !strings.HasPrefix(body, `{"brief":{"summary":"Adds a limiter."`) || api.gens != 1 {
				t.Errorf("POST = %d %s (generations %d), want 200 and one generation", status, body, api.gens)
			}
		})
	}
}

func TestPost_ForeignOriginIs403AndNothingRuns(t *testing.T) {
	api := &fakeAPI{}
	status, body := do(t, api, http.MethodPost, "/pulls/"+prID+"/brief", func(r *http.Request) {
		r.Header.Set("Origin", "https://evil.example")
	})
	if status != http.StatusForbidden || api.gens != 0 {
		t.Errorf("POST (foreign Origin) = %d %s (generations %d), want 403 and no generation", status, body, api.gens)
	}
}

func TestHead_NeverGenerates(t *testing.T) {
	api := &fakeAPI{}
	status, _ := do(t, api, http.MethodHead, "/pulls/"+prID+"/brief", nil)
	if status != http.StatusOK || api.gens != 0 {
		t.Errorf("HEAD = %d (generations %d), want 200 and no generation", status, api.gens)
	}
}

func TestOtherMethodsAreNotRoutes(t *testing.T) {
	api := &fakeAPI{}
	for _, m := range []string{http.MethodPut, http.MethodDelete, http.MethodPatch} {
		if status, _ := do(t, api, m, "/pulls/"+prID+"/brief", nil); status != http.StatusNotFound {
			t.Errorf("%s = %d, want 404", m, status)
		}
	}
	if api.gets+api.gens != 0 {
		t.Errorf("use cases called %d times, want 0", api.gets+api.gens)
	}
}

func TestNonUUIDIs422(t *testing.T) {
	for _, m := range []string{http.MethodGet, http.MethodPost} {
		api := &fakeAPI{}
		status, body := do(t, api, m, "/pulls/x/brief", nil)
		if status != http.StatusUnprocessableEntity || !strings.Contains(body, `"code":"validation_error"`) ||
			!strings.Contains(body, `"instancePath":"/id"`) {
			t.Errorf("%s /pulls/x/brief = %d %s, want 422 validation_error at /id", m, status, body)
		}
		if api.gets+api.gens != 0 {
			t.Errorf("%s: use case called, want not", m)
		}
	}
}

func TestErrorsAreRenderedInTheEnvelope(t *testing.T) {
	tests := []struct {
		name   string
		err    error
		status int
		body   string
	}{
		{"not found", apperr.NotFound("Pull request not found"), 404,
			`{"error":{"code":"not_found","message":"Pull request not found"}}`},
		{"in progress", apperr.Conflict("A brief is already being generated for this pull request").WithCode("generation_in_progress"), 409,
			`{"error":{"code":"generation_in_progress","message":"A brief is already being generated for this pull request"}}`},
		{"no changed files", apperr.Validation("The pull request has no changed files").WithCode("no_changed_files"), 422,
			`{"error":{"code":"no_changed_files","message":"The pull request has no changed files"}}`},
		{"provider not configured", apperr.Validation("The openai provider is not configured").WithCode("provider_not_configured").
			WithDetails(map[string]string{"provider": "openai"}), 422,
			`{"error":{"code":"provider_not_configured","message":"The openai provider is not configured","details":{"provider":"openai"}}}`},
		{"model failed", apperr.ExternalService("The model call failed").WithCode("generation_failed"), 502,
			`{"error":{"code":"generation_failed","message":"The model call failed"}}`},
		{"timed out", apperr.ExternalService("Generation timed out").WithCode("generation_failed"), 502,
			`{"error":{"code":"generation_failed","message":"Generation timed out"}}`},
		{"plain error", io.ErrUnexpectedEOF, 500, `{"error":{"code":"internal_error","message":"Internal error"}}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			status, body := do(t, &fakeAPI{err: tc.err}, http.MethodPost, "/pulls/"+prID+"/brief", nil)
			if status != tc.status || body != tc.body {
				t.Errorf("POST = %d %s, want %d %s", status, body, tc.status, tc.body)
			}
		})
	}
}

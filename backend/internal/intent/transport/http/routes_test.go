package intenthttp_test

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

	"dev-digest-go/backend/internal/intent/app"
	"dev-digest-go/backend/internal/intent/domain"
	intenthttp "dev-digest-go/backend/internal/intent/transport/http"
	"dev-digest-go/backend/internal/platform/apperr"
	"dev-digest-go/backend/internal/platform/httpx"
	reviewer "dev-digest-go/backend/internal/reviewer/domain"
	"dev-digest-go/backend/internal/tenancy"
)

const (
	prID          = "11111111-1111-4111-8111-111111111111"
	workspaceID   = "33333333-3333-4333-8333-333333333333"
	uiOrigin      = "http://localhost:3100"
	foreignOrigin = "http://evil.example"
)

func ptr[T any](v T) *T { return &v }

// fakeAPI answers the two use cases with canned values and records its calls.
type fakeAPI struct {
	mu    sync.Mutex
	calls []string
	res   app.Response
	err   error
}

func (f *fakeAPI) record(name, ws, id string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, name+" "+ws+" "+id)
}

func (f *fakeAPI) Get(_ context.Context, ws, id string) (app.Response, error) {
	f.record("Get", ws, id)
	return f.res, f.err
}

func (f *fakeAPI) Refresh(_ context.Context, ws, id string) (app.Response, error) {
	f.record("Refresh", ws, id)
	return f.res, f.err
}

func (f *fakeAPI) got() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

type fakeTenants struct{}

func (fakeTenants) Resolve(context.Context) (tenancy.Identity, error) {
	return tenancy.Identity{WorkspaceID: workspaceID}, nil
}

func newServer(t *testing.T, api *fakeAPI, rl httpx.RateLimit) *httptest.Server {
	t.Helper()
	e := httpx.New(httpx.Options{
		Log: slog.New(slog.DiscardHandler), CORSOrigins: []string{uiOrigin}, AllowedHosts: []string{"127.0.0.1"}, RateLimit: rl,
	})
	intenthttp.AddRoutes(e, api, fakeTenants{})
	srv := httptest.NewServer(e)
	t.Cleanup(srv.Close)
	return srv
}

type response struct {
	Status int
	Body   string
}

func send(t *testing.T, srv *httptest.Server, method, path, body string, headers ...string) response {
	t.Helper()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req, err := http.NewRequestWithContext(t.Context(), method, srv.URL+path, rd)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
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
	return response{Status: resp.StatusCode, Body: strings.TrimSpace(string(b))}
}

func record() domain.Record {
	return domain.Record{
		PRID: prID, Text: "Add a token-bucket rate limiter to the public webhook endpoints.",
		InScope: []string{"Rate limiting middleware", "Limits in the config"}, OutOfScope: []string{"Authentication changes"},
		ChangeType: ptr("feature"), Confidence: ptr("medium"), DerivedFrom: "explicit",
		Sources: []reviewer.IntentSource{
			{Kind: "title", Ref: "title", Status: "used"},
			{Kind: "ticket", Ref: "7", Status: "failed", Detail: ptr("Not Found")},
		},
		HeadSHA: "0123456789abcdef0123456789abcdef01234567", InputHash: "3d30", PromptVersion: 2,
		Provider: "openrouter", Model: "deepseek/deepseek-v4-flash", TokensIn: 1200, TokensOut: 180,
		CostUSD: ptr(0.00042), DerivedAt: time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC),
	}
}

// wantRecord is the record() as Node serialises it: the Intent keys, then the stored columns.
const wantRecord = `{"intent":"Add a token-bucket rate limiter to the public webhook endpoints.",` +
	`"in_scope":["Rate limiting middleware","Limits in the config"],"out_of_scope":["Authentication changes"],` +
	`"change_type":"feature","confidence":"medium","derived_from":"explicit",` +
	`"sources":[{"kind":"title","ref":"title","status":"used","detail":null},{"kind":"ticket","ref":"7","status":"failed","detail":"Not Found"}],` +
	`"pr_id":"` + prID + `","head_sha":"0123456789abcdef0123456789abcdef01234567","input_hash":"3d30","prompt_version":2,` +
	`"provider":"openrouter","model":"deepseek/deepseek-v4-flash","tokens_in":1200,"tokens_out":180,"cost_usd":0.00042,` +
	`"derived_at":"2026-01-01T12:00:00.000Z"}`

func TestGet_Responses(t *testing.T) {
	tests := []struct {
		name string
		res  app.Response
		err  error
		want string
		code int
	}{
		{"record", app.Response{Intent: ptr(record()), Stale: true}, nil, `{"intent":` + wantRecord + `,"stale":true}`, 200},
		{"no record", app.Response{}, nil, `{"intent":null,"stale":false}`, 200},
		{"nulls and empty lists", app.Response{Intent: &domain.Record{PRID: prID, DerivedFrom: "inferred", DerivedAt: record().DerivedAt}}, nil,
			`{"intent":{"intent":"","in_scope":[],"out_of_scope":[],"change_type":null,"confidence":null,"derived_from":"inferred","sources":[],` +
				`"pr_id":"` + prID + `","head_sha":"","input_hash":"","prompt_version":0,"provider":"","model":"","tokens_in":0,"tokens_out":0,` +
				`"cost_usd":null,"derived_at":"2026-01-01T12:00:00.000Z"},"stale":false}`, 200},
		{"pull request not found", app.Response{}, apperr.NotFound("Pull request not found"),
			`{"error":{"code":"not_found","message":"Pull request not found"}}`, 404},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			api := &fakeAPI{res: tc.res, err: tc.err}
			srv := newServer(t, api, httpx.RateLimit{})

			r := send(t, srv, http.MethodGet, "/pulls/"+prID+"/intent", "")

			if r.Status != tc.code || r.Body != tc.want {
				t.Errorf("GET intent = %d %s\nwant %d %s", r.Status, r.Body, tc.code, tc.want)
			}
			if diff := cmp.Diff([]string{"Get " + workspaceID + " " + prID}, api.got()); diff != "" {
				t.Errorf("use case calls mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestRefresh_Responses(t *testing.T) {
	tests := []struct {
		name string
		res  app.Response
		err  error
		want string
		code int
	}{
		{"record", app.Response{Intent: ptr(record())}, nil, `{"intent":` + wantRecord + `,"stale":false}`, 200},
		{"repo not found", app.Response{}, apperr.NotFound("Repo not found"), `{"error":{"code":"not_found","message":"Repo not found"}}`, 404},
		{"derivation failed", app.Response{}, apperr.ExternalService("Intent derivation failed: URI malformed").WithCode("intent_unavailable"),
			`{"error":{"code":"intent_unavailable","message":"Intent derivation failed: URI malformed"}}`, 502},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			api := &fakeAPI{res: tc.res, err: tc.err}
			srv := newServer(t, api, httpx.RateLimit{})

			r := send(t, srv, http.MethodPost, "/pulls/"+prID+"/intent/refresh", "")

			if r.Status != tc.code || r.Body != tc.want {
				t.Errorf("POST refresh = %d %s\nwant %d %s", r.Status, r.Body, tc.code, tc.want)
			}
			if diff := cmp.Diff([]string{"Refresh " + workspaceID + " " + prID}, api.got()); diff != "" {
				t.Errorf("use case calls mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// A body the framework accepts (none, JSON, text/plain: platform/httpx bodyRules, as Fastify) is
// never read by the handler. Malformed JSON and other media types are refused by httpx before the
// handler runs (400, 415), like the Phase 4 cancel route.
func TestRefresh_IgnoresAnyBody(t *testing.T) {
	for _, tc := range []struct{ name, body, contentType string }{
		{"no body", "", ""},
		{"json", `{"force":false}`, "application/json"},
		{"text", "hello", "text/plain"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			api := &fakeAPI{res: app.Response{Intent: ptr(record())}}
			srv := newServer(t, api, httpx.RateLimit{})
			var headers []string
			if tc.contentType != "" {
				headers = []string{"Content-Type", tc.contentType}
			}

			r := send(t, srv, http.MethodPost, "/pulls/"+prID+"/intent/refresh", tc.body, headers...)

			if r.Status != http.StatusOK {
				t.Errorf("POST refresh with %s = %d %s, want 200", tc.name, r.Status, r.Body)
			}
		})
	}
}

func TestRoutes_NonUUIDIDIs422(t *testing.T) {
	api := &fakeAPI{}
	srv := newServer(t, api, httpx.RateLimit{})
	for _, rt := range []struct{ method, path string }{
		{http.MethodGet, "/pulls/x/intent"},
		{http.MethodPost, "/pulls/x/intent/refresh"},
	} {
		r := send(t, srv, rt.method, rt.path, "")
		if r.Status != http.StatusUnprocessableEntity || !strings.Contains(r.Body, `"code":"validation_error"`) ||
			!strings.Contains(r.Body, `"instancePath":"/id"`) {
			t.Errorf("%s %s = %d %s, want 422 validation_error at /id", rt.method, rt.path, r.Status, r.Body)
		}
	}
	if n := len(api.got()); n != 0 {
		t.Errorf("use case calls = %d, want 0", n)
	}
}

func TestRefresh_ForeignOriginIsRefused(t *testing.T) {
	api := &fakeAPI{}
	srv := newServer(t, api, httpx.RateLimit{})

	r := send(t, srv, http.MethodPost, "/pulls/"+prID+"/intent/refresh", "", "Origin", foreignOrigin)

	if r.Status != http.StatusForbidden || len(api.got()) != 0 {
		t.Errorf("POST refresh with a foreign Origin = %d %s (calls %v), want 403 and no use case call", r.Status, r.Body, api.got())
	}
	if ok := send(t, srv, http.MethodPost, "/pulls/"+prID+"/intent/refresh", "", "Origin", uiOrigin); ok.Status != http.StatusOK {
		t.Errorf("POST refresh from the UI origin = %d, want 200", ok.Status)
	}
}

func TestRefresh_HasATenPerMinuteBucket(t *testing.T) {
	api := &fakeAPI{res: app.Response{Intent: ptr(record())}}
	srv := newServer(t, api, httpx.RateLimit{
		Enabled: true,
		Buckets: []httpx.Bucket{{Method: http.MethodPost, Path: intenthttp.RefreshPath, Limit: 10}},
	})

	for i := 1; i <= 10; i++ {
		if r := send(t, srv, http.MethodPost, "/pulls/"+prID+"/intent/refresh", ""); r.Status != http.StatusOK {
			t.Fatalf("refresh %d = %d %s, want 200", i, r.Status, r.Body)
		}
	}
	r := send(t, srv, http.MethodPost, "/pulls/"+prID+"/intent/refresh", "")
	if r.Status != http.StatusTooManyRequests || !strings.Contains(r.Body, `"code":"internal_error"`) {
		t.Errorf("11th refresh = %d %s, want 429 with code internal_error", r.Status, r.Body)
	}
	if g := send(t, srv, http.MethodGet, "/pulls/"+prID+"/intent", ""); g.Status != http.StatusOK {
		t.Errorf("GET intent after the refresh bucket is used = %d, want 200 (the bucket is the route's own)", g.Status)
	}
}

func TestGet_HeadAnswersWithoutTheBodyAndWithoutDeriving(t *testing.T) {
	api := &fakeAPI{res: app.Response{}}
	srv := newServer(t, api, httpx.RateLimit{})

	r := send(t, srv, http.MethodHead, "/pulls/"+prID+"/intent", "")

	if r.Status != http.StatusOK || r.Body != "" {
		t.Errorf("HEAD intent = %d %q, want 200 with no body", r.Status, r.Body)
	}
}

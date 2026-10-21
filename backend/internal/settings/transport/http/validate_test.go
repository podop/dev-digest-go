package settingshttp_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/labstack/echo/v5"

	"dev-digest-go/backend/internal/platform/httpx"
	"dev-digest-go/backend/internal/settings/app"
	settingshttp "dev-digest-go/backend/internal/settings/transport/http"
	"dev-digest-go/backend/internal/tenancy"
)

type fakeService struct {
	gotEntries app.Bag
	gotProv    app.Provider
	gotKey     string
	updates    int
}

func (f *fakeService) Get(context.Context, string) (app.Bag, error) { return app.Bag{}, nil }

func (f *fakeService) Update(_ context.Context, _, _ string, e app.Bag) (app.Bag, error) {
	f.updates++
	f.gotEntries = e
	return e, nil
}

func (f *fakeService) SecretsStatus() app.SecretsStatus { return app.SecretsStatus{} }

func (f *fakeService) TestConnection(_ context.Context, p app.Provider, key string) app.ConnTestResult {
	f.gotProv, f.gotKey = p, key
	return app.ConnTestResult{Provider: p, OK: true, Message: "ok"}
}

type fakeIdentities struct{ err error }

func (f fakeIdentities) Resolve(context.Context) (tenancy.Identity, error) {
	return tenancy.Identity{WorkspaceID: "ws", UserID: "u"}, f.err
}

func newRouter(svc *fakeService, ids fakeIdentities) *echo.Echo {
	e := httpx.New(httpx.Options{})
	settingshttp.AddRoutes(e, settingshttp.NewHandler(svc, ids))
	return e
}

func do(h http.Handler, method, path, contentType, body string) *httptest.ResponseRecorder {
	var req *http.Request
	if body == "" {
		req = httptest.NewRequest(method, path, nil)
	} else {
		req = httptest.NewRequest(method, path, strings.NewReader(body))
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

type issueView struct {
	Keyword      string `json:"keyword"`
	InstancePath string `json:"instancePath"`
	Message      string `json:"message"`
}

func issues(t *testing.T, rec *httptest.ResponseRecorder) []issueView {
	t.Helper()
	var env struct {
		Error struct {
			Code    string      `json:"code"`
			Message string      `json:"message"`
			Details []issueView `json:"details"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("body %q is not an error envelope: %v", rec.Body.String(), err)
	}
	if env.Error.Code != "validation_error" {
		t.Errorf("error code = %q, want validation_error", env.Error.Code)
	}
	return env.Error.Details
}

func TestPutSettings_validation(t *testing.T) {
	tests := []struct {
		name string
		body string
		want []issueView // keyword and instancePath only
	}{
		{name: "polling zero", body: `{"polling_interval_min":0}`, want: []issueView{{Keyword: "too_small", InstancePath: "/polling_interval_min"}}},
		{name: "polling fraction", body: `{"polling_interval_min":1.5}`, want: []issueView{{Keyword: "invalid_type", InstancePath: "/polling_interval_min"}}},
		{name: "polling string", body: `{"polling_interval_min":"5"}`, want: []issueView{{Keyword: "invalid_type", InstancePath: "/polling_interval_min"}}},
		{name: "theme null", body: `{"theme":null}`, want: []issueView{{Keyword: "invalid_type", InstancePath: "/theme"}}},
		{name: "theme unknown", body: `{"theme":"blue"}`, want: []issueView{{Keyword: "invalid_enum_value", InstancePath: "/theme"}}},
		{name: "density unknown", body: `{"density":"tiny"}`, want: []issueView{{Keyword: "invalid_enum_value", InstancePath: "/density"}}},
		{name: "sync_to_folder string", body: `{"sync_to_folder":"yes"}`, want: []issueView{{Keyword: "invalid_type", InstancePath: "/sync_to_folder"}}},
		{name: "automatic_reviews null", body: `{"automatic_reviews":null}`, want: []issueView{{Keyword: "invalid_type", InstancePath: "/automatic_reviews"}}},
		{name: "feature_models array", body: `{"feature_models":[]}`, want: []issueView{{Keyword: "invalid_type", InstancePath: "/feature_models"}}},
		{name: "feature id unknown", body: `{"feature_models":{"nope":{"provider":"openai","model":"m"}}}`, want: []issueView{{Keyword: "invalid_enum_value", InstancePath: "/feature_models/nope"}}},
		{name: "provider unknown", body: `{"feature_models":{"risk_brief":{"provider":"x","model":"m"}}}`, want: []issueView{{Keyword: "invalid_enum_value", InstancePath: "/feature_models/risk_brief/provider"}}},
		{name: "model empty", body: `{"feature_models":{"risk_brief":{"provider":"openai","model":""}}}`, want: []issueView{{Keyword: "too_small", InstancePath: "/feature_models/risk_brief/model"}}},
		{name: "choice missing fields", body: `{"feature_models":{"risk_brief":{}}}`, want: []issueView{
			{Keyword: "invalid_type", InstancePath: "/feature_models/risk_brief/provider"},
			{Keyword: "invalid_type", InstancePath: "/feature_models/risk_brief/model"},
		}},
		{name: "array body", body: `[]`, want: []issueView{{Keyword: "invalid_type", InstancePath: "/"}}},
		{name: "string body", body: `"x"`, want: []issueView{{Keyword: "invalid_type", InstancePath: "/"}}},
		{name: "null body", body: `null`, want: []issueView{{Keyword: "invalid_type", InstancePath: "/"}}},
		{name: "all issues are reported", body: `{"theme":"x","polling_interval_min":0}`, want: []issueView{
			{Keyword: "too_small", InstancePath: "/polling_interval_min"},
			{Keyword: "invalid_enum_value", InstancePath: "/theme"},
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			svc := &fakeService{}
			rec := do(newRouter(svc, fakeIdentities{}), http.MethodPut, "/settings", "application/json", tc.body)
			if rec.Code != http.StatusUnprocessableEntity {
				t.Fatalf("PUT /settings %s status = %d, want 422 (body %s)", tc.body, rec.Code, rec.Body)
			}
			var got []issueView
			for _, is := range issues(t, rec) {
				got = append(got, issueView{Keyword: is.Keyword, InstancePath: is.InstancePath})
			}
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("PUT /settings %s issues mismatch (-want +got):\n%s", tc.body, diff)
			}
			if svc.updates != 0 {
				t.Errorf("Update called %d times on an invalid body, want 0", svc.updates)
			}
		})
	}
}

func TestPutSettings_noBodyIs422(t *testing.T) {
	rec := do(newRouter(&fakeService{}, fakeIdentities{}), http.MethodPut, "/settings", "", "")
	if rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("PUT /settings without body status = %d, want 422", rec.Code)
	}
}

func TestPutSettings_acceptedEntries(t *testing.T) {
	tests := []struct {
		name string
		body string
		want map[string]string // key to raw JSON
	}{
		{name: "empty object", body: `{}`, want: map[string]string{}},
		{name: "known keys", body: `{"polling_interval_min":10,"theme":"light","density":"compact","sync_to_folder":false,"automatic_reviews":true}`,
			want: map[string]string{"polling_interval_min": "10", "theme": `"light"`, "density": `"compact"`, "sync_to_folder": "false", "automatic_reviews": "true"}},
		{name: "integral float is an integer", body: `{"polling_interval_min":10.0}`, want: map[string]string{"polling_interval_min": "10"}},
		{name: "unknown keys keep any value including null", body: `{"x_custom":null,"y":{"a":[1,2]}}`,
			want: map[string]string{"x_custom": "null", "y": `{"a":[1,2]}`}},
		{name: "feature_models subset, extra fields dropped", body: `{"feature_models":{"risk_brief":{"provider":"openai","model":"gpt-4.1","extra":1}}}`,
			want: map[string]string{"feature_models": `{"risk_brief":{"provider":"openai","model":"gpt-4.1"}}`}},
		{name: "feature_models empty", body: `{"feature_models":{}}`, want: map[string]string{"feature_models": `{}`}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			svc := &fakeService{}
			rec := do(newRouter(svc, fakeIdentities{}), http.MethodPut, "/settings", "application/json", tc.body)
			if rec.Code != http.StatusOK {
				t.Fatalf("PUT /settings %s status = %d, want 200 (body %s)", tc.body, rec.Code, rec.Body)
			}
			got := map[string]string{}
			for k, v := range svc.gotEntries {
				got[k] = string(v)
			}
			if tc.body == `{}` && svc.updates == 0 {
				return // an empty patch may skip the write path entirely
			}
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("PUT /settings %s entries mismatch (-want +got):\n%s", tc.body, diff)
			}
		})
	}
}

func TestTestConnection_validation(t *testing.T) {
	tests := []struct {
		name string
		body string
		want []issueView
	}{
		{name: "missing provider", body: `{}`, want: []issueView{{Keyword: "invalid_type", InstancePath: "/provider"}}},
		{name: "unknown provider", body: `{"provider":"gitlab"}`, want: []issueView{{Keyword: "invalid_enum_value", InstancePath: "/provider"}}},
		{name: "provider not a string", body: `{"provider":1}`, want: []issueView{{Keyword: "invalid_type", InstancePath: "/provider"}}},
		{name: "empty key", body: `{"provider":"openai","key":""}`, want: []issueView{{Keyword: "too_small", InstancePath: "/key"}}},
		{name: "null key", body: `{"provider":"openai","key":null}`, want: []issueView{{Keyword: "invalid_type", InstancePath: "/key"}}},
		{name: "array body", body: `[]`, want: []issueView{{Keyword: "invalid_type", InstancePath: "/"}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			svc := &fakeService{}
			rec := do(newRouter(svc, fakeIdentities{}), http.MethodPost, "/settings/test-connection", "application/json", tc.body)
			if rec.Code != http.StatusUnprocessableEntity {
				t.Fatalf("POST test-connection %s status = %d, want 422", tc.body, rec.Code)
			}
			var got []issueView
			for _, is := range issues(t, rec) {
				got = append(got, issueView{Keyword: is.Keyword, InstancePath: is.InstancePath})
			}
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("issues mismatch (-want +got):\n%s", diff)
			}
			if svc.gotProv != "" {
				t.Errorf("TestConnection called with %q on an invalid body", svc.gotProv)
			}
		})
	}
}

func TestTestConnection_passesProviderAndKey(t *testing.T) {
	svc := &fakeService{}
	rec := do(newRouter(svc, fakeIdentities{}), http.MethodPost, "/settings/test-connection", "application/json",
		`{"provider":"github","key":"ghp_x","ignored":1}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body)
	}
	if svc.gotProv != app.GitHub || svc.gotKey != "ghp_x" {
		t.Errorf("TestConnection(%q, %q), want (github, ghp_x)", svc.gotProv, svc.gotKey)
	}
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(map[string]any{"provider": "github", "ok": true, "message": "ok"}, got); diff != "" {
		t.Errorf("response mismatch (-want +got):\n%s", diff)
	}
}

func TestRoutes_missingContextIs500ExceptTestConnection(t *testing.T) {
	h := newRouter(&fakeService{}, fakeIdentities{err: errors.New("not provisioned")})
	tests := []struct {
		method, path, body string
		want               int
	}{
		{http.MethodGet, "/settings", "", http.StatusInternalServerError},
		{http.MethodGet, "/settings/secrets-status", "", http.StatusInternalServerError},
		{http.MethodPut, "/settings", `{"theme":"dark"}`, http.StatusInternalServerError},
		{http.MethodPost, "/settings/test-connection", `{"provider":"github"}`, http.StatusOK},
	}
	for _, tc := range tests {
		ct := ""
		if tc.body != "" {
			ct = "application/json"
		}
		rec := do(h, tc.method, tc.path, ct, tc.body)
		if rec.Code != tc.want {
			t.Errorf("%s %s status = %d, want %d", tc.method, tc.path, rec.Code, tc.want)
		}
		if tc.want == http.StatusInternalServerError {
			want := `{"error":{"code":"internal_error","message":"Internal error"}}`
			if got := strings.TrimSpace(rec.Body.String()); got != want {
				t.Errorf("%s %s body = %s, want %s", tc.method, tc.path, got, want)
			}
		}
	}
}

func TestRateLimitBuckets(t *testing.T) {
	want := []httpx.Bucket{{Method: http.MethodPost, Path: "/settings/test-connection", Limit: 20}}
	if diff := cmp.Diff(want, settingshttp.RateLimitBuckets()); diff != "" {
		t.Errorf("RateLimitBuckets() mismatch (-want +got):\n%s", diff)
	}
}

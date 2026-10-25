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
	gotWS      string
	updates    int
	result     *app.ConnTestResult // returned by TestConnection when set
}

func (f *fakeService) Get(context.Context, string) (app.Bag, error) { return app.Bag{}, nil }

func (f *fakeService) Update(_ context.Context, _, _ string, e app.Bag) (app.Bag, error) {
	f.updates++
	f.gotEntries = e
	return e, nil
}

func (f *fakeService) SecretsStatus() app.SecretsStatus { return app.SecretsStatus{} }

func (f *fakeService) TestConnection(_ context.Context, ws string, p app.Provider, key string) app.ConnTestResult {
	f.gotWS, f.gotProv, f.gotKey = ws, p, key
	if f.result != nil {
		return *f.result
	}
	return app.ConnTestResult{Provider: p, OK: true, Message: "ok"}
}

type fakeIdentities struct{ err error }

func (f fakeIdentities) Resolve(context.Context) (tenancy.Identity, error) {
	return tenancy.Identity{WorkspaceID: "ws", UserID: "u"}, f.err
}

func newRouter(svc *fakeService, ids fakeIdentities) *echo.Echo {
	e := httpx.New(httpx.Options{CORSOrigins: []string{uiOrigin}})
	settingshttp.AddRoutes(e, settingshttp.NewHandler(svc, ids))
	return e
}

func do(h http.Handler, method, path, contentType, body string, headers ...string) *httptest.ResponseRecorder {
	var req *http.Request
	if body == "" {
		req = httptest.NewRequest(method, path, nil)
	} else {
		req = httptest.NewRequest(method, path, strings.NewReader(body))
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
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
		{name: "default_model string", body: `{"default_model":"x"}`, want: []issueView{{Keyword: "invalid_type", InstancePath: "/default_model"}}},
		{name: "default_model provider unknown", body: `{"default_model":{"provider":"foo","model":"m"}}`, want: []issueView{{Keyword: "invalid_enum_value", InstancePath: "/default_model/provider"}}},
		{name: "default_model provider only", body: `{"default_model":{"provider":"foo"}}`, want: []issueView{
			{Keyword: "invalid_enum_value", InstancePath: "/default_model/provider"},
			{Keyword: "invalid_type", InstancePath: "/default_model/model"},
		}},
		{name: "default_model model missing", body: `{"default_model":{"provider":"openai"}}`, want: []issueView{{Keyword: "invalid_type", InstancePath: "/default_model/model"}}},
		{name: "default_model model empty", body: `{"default_model":{"provider":"openai","model":""}}`, want: []issueView{{Keyword: "too_small", InstancePath: "/default_model/model"}}},
		{name: "cap negative", body: `{"monthly_spend_cap_usd":-1}`, want: []issueView{{Keyword: "too_small", InstancePath: "/monthly_spend_cap_usd"}}},
		{name: "cap above the maximum", body: `{"monthly_spend_cap_usd":1000000.01}`, want: []issueView{
			{Keyword: "too_big", InstancePath: "/monthly_spend_cap_usd"},
		}},
		{name: "cap three decimals", body: `{"monthly_spend_cap_usd":1.234}`, want: []issueView{{Keyword: "custom", InstancePath: "/monthly_spend_cap_usd"}}},
		{name: "cap negative with three decimals", body: `{"monthly_spend_cap_usd":-1.234}`, want: []issueView{
			{Keyword: "too_small", InstancePath: "/monthly_spend_cap_usd"},
			{Keyword: "custom", InstancePath: "/monthly_spend_cap_usd"},
		}},
		{name: "cap string", body: `{"monthly_spend_cap_usd":"50"}`, want: []issueView{{Keyword: "invalid_type", InstancePath: "/monthly_spend_cap_usd"}}},
		{name: "cap boolean", body: `{"monthly_spend_cap_usd":true}`, want: []issueView{{Keyword: "invalid_type", InstancePath: "/monthly_spend_cap_usd"}}},
		{name: "cap overflow", body: `{"monthly_spend_cap_usd":1e999}`, want: []issueView{{Keyword: "invalid_type", InstancePath: "/monthly_spend_cap_usd"}}},
		{name: "retention 45", body: `{"retention_days":45}`, want: []issueView{{Keyword: "invalid_union", InstancePath: "/retention_days"}}},
		{name: "retention 0", body: `{"retention_days":0}`, want: []issueView{{Keyword: "invalid_union", InstancePath: "/retention_days"}}},
		{name: "retention negative", body: `{"retention_days":-30}`, want: []issueView{{Keyword: "invalid_union", InstancePath: "/retention_days"}}},
		{name: "retention fraction", body: `{"retention_days":30.5}`, want: []issueView{{Keyword: "invalid_union", InstancePath: "/retention_days"}}},
		{name: "retention overflow", body: `{"retention_days":1e999}`, want: []issueView{{Keyword: "invalid_union", InstancePath: "/retention_days"}}},
		{name: "retention string", body: `{"retention_days":"90"}`, want: []issueView{{Keyword: "invalid_union", InstancePath: "/retention_days"}}},
		{name: "retention boolean", body: `{"retention_days":true}`, want: []issueView{{Keyword: "invalid_union", InstancePath: "/retention_days"}}},
		{name: "retention object", body: `{"retention_days":{}}`, want: []issueView{{Keyword: "invalid_union", InstancePath: "/retention_days"}}},
		{name: "workspace_name is set with PUT /workspace", body: `{"workspace_name":"acme"}`, want: []issueView{{Keyword: "custom", InstancePath: "/workspace_name"}}},
		{name: "workspace_name null", body: `{"workspace_name":null}`, want: []issueView{{Keyword: "custom", InstancePath: "/workspace_name"}}},
		{name: "workspace_name beside a valid key", body: `{"theme":"dark","workspace_name":"x"}`, want: []issueView{{Keyword: "custom", InstancePath: "/workspace_name"}}},
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
		{name: "default_model, extra fields dropped", body: `{"default_model":{"provider":"openrouter","model":"a/x","extra":1}}`,
			want: map[string]string{"default_model": `{"provider":"openrouter","model":"a/x"}`}},
		{name: "default_model null clears it", body: `{"default_model":null}`, want: map[string]string{"default_model": "null"}},
		{name: "cap null", body: `{"monthly_spend_cap_usd":null}`, want: map[string]string{"monthly_spend_cap_usd": "null"}},
		{name: "cap zero", body: `{"monthly_spend_cap_usd":0}`, want: map[string]string{"monthly_spend_cap_usd": "0"}},
		{name: "cap negative zero is zero", body: `{"monthly_spend_cap_usd":-0}`, want: map[string]string{"monthly_spend_cap_usd": "0"}},
		{name: "cap integer", body: `{"monthly_spend_cap_usd":50}`, want: map[string]string{"monthly_spend_cap_usd": "50"}},
		{name: "cap two decimals", body: `{"monthly_spend_cap_usd":0.07}`, want: map[string]string{"monthly_spend_cap_usd": "0.07"}},
		{name: "cap trailing zeros and exponent", body: `{"monthly_spend_cap_usd":5.10e1}`, want: map[string]string{"monthly_spend_cap_usd": "51"}},
		{name: "retention 30", body: `{"retention_days":30}`, want: map[string]string{"retention_days": "30"}},
		{name: "retention 60", body: `{"retention_days":60}`, want: map[string]string{"retention_days": "60"}},
		{name: "retention 90", body: `{"retention_days":90}`, want: map[string]string{"retention_days": "90"}},
		{name: "retention 180", body: `{"retention_days":180}`, want: map[string]string{"retention_days": "180"}},
		{name: "retention 365", body: `{"retention_days":365}`, want: map[string]string{"retention_days": "365"}},
		{name: "retention null keeps forever", body: `{"retention_days":null}`, want: map[string]string{"retention_days": "null"}},
		{name: "retention integral float is an integer", body: `{"retention_days":9e1}`, want: map[string]string{"retention_days": "90"}},
		{name: "cap maximum", body: `{"monthly_spend_cap_usd":1000000}`, want: map[string]string{"monthly_spend_cap_usd": "1000000"}},
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

func TestPutSettings_capIssueMessages(t *testing.T) {
	tests := []struct {
		body        string
		wantCode    string
		wantMessage string
	}{
		{`{"monthly_spend_cap_usd":-1}`, "too_small", "Number must be greater than or equal to 0"},
		{`{"monthly_spend_cap_usd":1000000.01}`, "too_big", "Number must be less than or equal to 1000000"},
		{`{"monthly_spend_cap_usd":1.234}`, "custom", "At most 2 decimals"},
		{`{"monthly_spend_cap_usd":"50"}`, "invalid_type", "Expected number, received string"},
		{`{"retention_days":45}`, "invalid_union", "Invalid input"},
		{`{"workspace_name":"x"}`, "custom", "workspace_name is set with PUT /workspace"},
		{`{"default_model":"x"}`, "invalid_type", "Expected object, received string"},
		{`{"default_model":{"provider":"foo","model":"m"}}`, "invalid_enum_value",
			"Invalid enum value. Expected 'openai' | 'anthropic' | 'openrouter', received 'foo'"},
	}
	for _, tc := range tests {
		t.Run(tc.body, func(t *testing.T) {
			rec := do(newRouter(&fakeService{}, fakeIdentities{}), http.MethodPut, "/settings", "application/json", tc.body)
			got := issues(t, rec)
			if len(got) != 1 || got[0].Keyword != tc.wantCode || got[0].Message != tc.wantMessage {
				t.Errorf("PUT /settings %s issues = %+v, want one %s %q", tc.body, got, tc.wantCode, tc.wantMessage)
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
	want := map[string]any{"provider": "github", "ok": true, "message": "ok", "latency_ms": nil, "login": nil, "scopes": nil}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("response mismatch (-want +got):\n%s", diff)
	}
}

func TestTestConnection_workspaceForLLMProvidersOnly(t *testing.T) {
	for provider, want := range map[string]string{"openrouter": "ws", "openai": "ws", "anthropic": "ws", "github": ""} {
		svc := &fakeService{}
		do(newRouter(svc, fakeIdentities{}), http.MethodPost, "/settings/test-connection", "application/json", `{"provider":"`+provider+`"}`)
		if svc.gotWS != want {
			t.Errorf("TestConnection(%s) workspace = %q, want %q", provider, svc.gotWS, want)
		}
	}
	svc := &fakeService{}
	h := newRouter(svc, fakeIdentities{err: errors.New("not provisioned")})
	if rec := do(h, http.MethodPost, "/settings/test-connection", "application/json", `{"provider":"openrouter"}`); rec.Code != http.StatusOK || svc.gotWS != "" {
		t.Errorf("unresolvable workspace: status = %d workspace = %q, want 200 and an empty workspace", rec.Code, svc.gotWS)
	}
}

func TestTestConnection_responseShapes(t *testing.T) { // FR5, FR16, A14: absent is not null
	tests := []struct {
		name   string
		result app.ConnTestResult
		want   string
	}{
		{"llm success", app.ConnTestResult{Provider: app.OpenRouter, OK: true, Message: "OK — 2 models available",
			LatencyMS: ptr(12), Model: ptr("a/x"), ModelReachable: ptr(false)},
			`{"provider":"openrouter","ok":true,"message":"OK — 2 models available","latency_ms":12,"model":"a/x","model_reachable":false}`},
		{"llm failure has null extras and no github fields", app.ConnTestResult{Provider: app.OpenAI, Message: "nope"},
			`{"provider":"openai","ok":false,"message":"nope","latency_ms":null,"model":null,"model_reachable":null}`},
		{"github success", app.ConnTestResult{Provider: app.GitHub, OK: true, Message: "Connected as @ann",
			LatencyMS: ptr(3), Login: ptr("ann"), Scopes: []string{"repo", "read:org"}},
			`{"provider":"github","ok":true,"message":"Connected as @ann","latency_ms":3,"login":"ann","scopes":["repo","read:org"]}`},
		{"github empty scopes", app.ConnTestResult{Provider: app.GitHub, OK: true, Message: "m", LatencyMS: ptr(0), Login: ptr("ann"), Scopes: []string{}},
			`{"provider":"github","ok":true,"message":"m","latency_ms":0,"login":"ann","scopes":[]}`},
		{"github failure has null extras and no model fields", app.ConnTestResult{Provider: app.GitHub, Message: "bad"},
			`{"provider":"github","ok":false,"message":"bad","latency_ms":null,"login":null,"scopes":null}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			svc := &fakeService{result: &tc.result}
			rec := do(newRouter(svc, fakeIdentities{}), http.MethodPost, "/settings/test-connection", "application/json",
				`{"provider":"`+string(tc.result.Provider)+`"}`)
			if got := strings.TrimSpace(rec.Body.String()); rec.Code != http.StatusOK || got != tc.want {
				t.Errorf("POST test-connection = %d %s, want 200 %s", rec.Code, got, tc.want)
			}
		})
	}
}

func ptr[T any](v T) *T { return &v }

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

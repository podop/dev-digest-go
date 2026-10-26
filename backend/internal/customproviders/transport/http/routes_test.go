package customprovidershttp_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/labstack/echo/v5"

	"dev-digest-go/backend/internal/customproviders/app"
	"dev-digest-go/backend/internal/customproviders/domain"
	customprovidershttp "dev-digest-go/backend/internal/customproviders/transport/http"
	"dev-digest-go/backend/internal/llm/custom"
	"dev-digest-go/backend/internal/platform/apperr"
	"dev-digest-go/backend/internal/platform/httpx"
	"dev-digest-go/backend/internal/tenancy"
)

const (
	uiOrigin      = "http://localhost:3100"
	foreignOrigin = "http://evil.example"
	workspace     = "ws-1"
)

// call is one use case call the fake saw.
type call struct {
	Method string
	WS, ID string
	In     domain.Input
}

// fakeService is a hand-written service: it records its calls and answers with view/err.
type fakeService struct {
	calls []call
	views []app.View
	err   error
}

func (f *fakeService) List(_ context.Context, ws string) ([]app.View, error) {
	f.calls = append(f.calls, call{Method: "List", WS: ws})
	return f.views, f.err
}

func (f *fakeService) Create(_ context.Context, ws string, in domain.Input) (app.View, error) {
	f.calls = append(f.calls, call{Method: "Create", WS: ws, In: in})
	return f.view(), f.err
}

func (f *fakeService) Update(_ context.Context, ws, id string, in domain.Input) (app.View, error) {
	f.calls = append(f.calls, call{Method: "Update", WS: ws, ID: id, In: in})
	return f.view(), f.err
}

func (f *fakeService) Delete(_ context.Context, ws, id string) (string, error) {
	f.calls = append(f.calls, call{Method: "Delete", WS: ws, ID: id})
	return id, f.err
}

func (f *fakeService) view() app.View {
	if len(f.views) > 0 {
		return f.views[0]
	}
	return app.View{}
}

type fakeTenants struct{}

func (fakeTenants) Resolve(context.Context) (tenancy.Identity, error) {
	return tenancy.Identity{WorkspaceID: workspace, UserID: "u-1"}, nil
}

type env struct {
	e   *echo.Echo
	svc *fakeService
}

func newEnv(t *testing.T) *env {
	t.Helper()
	svc := &fakeService{}
	e := httpx.New(httpx.Options{
		Log:          slog.New(slog.DiscardHandler),
		CORSOrigins:  []string{uiOrigin},
		AllowedHosts: []string{"example.com"},
	})
	customprovidershttp.AddRoutes(e, svc, fakeTenants{})
	return &env{e: e, svc: svc}
}

type response struct {
	status int
	body   string
}

// do sends a request as the UI would (Host example.com, JSON body when there is one).
func (v *env) do(method, path, body string, headers ...string) response {
	var req *http.Request
	if body == "" {
		req = httptest.NewRequest(method, path, nil)
	} else {
		req = httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
	}
	req.Host = "example.com"
	for i := 0; i+1 < len(headers); i += 2 {
		if headers[i] == "Host" {
			req.Host = headers[i+1]
			continue
		}
		req.Header.Set(headers[i], headers[i+1])
	}
	rec := httptest.NewRecorder()
	v.e.ServeHTTP(rec, req)
	return response{rec.Code, strings.TrimSpace(rec.Body.String())}
}

type errorBody struct {
	Error struct {
		Code    string          `json:"code"`
		Message string          `json:"message"`
		Details json.RawMessage `json:"details"`
	} `json:"error"`
}

func decodeError(t *testing.T, r response) errorBody {
	t.Helper()
	var e errorBody
	if err := json.Unmarshal([]byte(r.body), &e); err != nil {
		t.Fatalf("response body %q is not the error envelope: %v", r.body, err)
	}
	return e
}

func str(s string) *string { return &s }

var created = time.Date(2026, 10, 26, 10, 11, 12, 345_678_000, time.UTC)

// full is a provider with every field set, with a header and two models.
func full() app.View {
	return app.View{
		Provider: domain.Provider{
			ID: "custom:synapse", Name: "Synapse", Protocol: custom.ProtocolAnthropic,
			BaseURL: "http://gw.corp/v1", AuthMode: custom.AuthHeader, AuthHeader: "x-api-key",
			StructuredOutput: custom.ModeStrict, ExtraHeaderNames: []string{"X-Org"},
			Models: []domain.Model{
				{ID: "m-1", Label: str("Model one"), Pricing: &domain.Pricing{PromptPerM: 1.5, CompletionPerM: 7.25}},
				{ID: "m-2"},
			},
			CreatedAt: created, UpdatedAt: created.Add(time.Hour),
		},
		PlainHTTP: true, KeyConfigured: true,
		ExtraHeaders: []app.HeaderView{{Name: "X-Org", ValueConfigured: true}},
	}
}

// wantFull is the wire form of full(), key by key in contract order (CustomProvider in
// frontend/src/vendor/shared/contracts/platform.ts).
const wantFull = `{"id":"custom:synapse","name":"Synapse","protocol":"anthropic","base_url":"http://gw.corp/v1",` +
	`"plain_http":true,"auth_mode":"header","auth_header":"x-api-key","structured_output":"strict",` +
	`"key_configured":true,"extra_headers":[{"name":"X-Org","value_configured":true}],` +
	`"models":[{"id":"m-1","label":"Model one","pricing":{"promptPerM":1.5,"completionPerM":7.25}},` +
	`{"id":"m-2","label":null,"pricing":null}],` +
	`"created_at":"2026-10-26T10:11:12.345Z","updated_at":"2026-10-26T11:11:12.345Z"}`

// bare is a bearer provider: no auth header, no key, no headers, no models.
func bare() app.View {
	return app.View{Provider: domain.Provider{
		ID: "custom:plain", Name: "Plain", Protocol: custom.ProtocolOpenAI, BaseURL: "https://api.example.com/v1",
		AuthMode: custom.AuthBearer, StructuredOutput: custom.ModeJSONObject, CreatedAt: created, UpdatedAt: created,
	}}
}

const wantBare = `{"id":"custom:plain","name":"Plain","protocol":"openai","base_url":"https://api.example.com/v1",` +
	`"plain_http":false,"auth_mode":"bearer","auth_header":null,"structured_output":"json_object",` +
	`"key_configured":false,"extra_headers":[],"models":[],` +
	`"created_at":"2026-10-26T10:11:12.345Z","updated_at":"2026-10-26T10:11:12.345Z"}`

const minimalCreate = `{"id":"custom:synapse","name":"Synapse","protocol":"openai","base_url":"https://x/v1"}`

// AC1 AC2
func TestList_WireShape(t *testing.T) {
	v := newEnv(t)
	v.svc.views = []app.View{full(), bare()}
	res := v.do(http.MethodGet, "/settings/custom-providers", "")
	if want := "[" + wantFull + "," + wantBare + "]"; res.status != 200 || res.body != want {
		t.Errorf("GET /settings/custom-providers = %d %s, want 200 %s", res.status, res.body, want)
	}
	if diff := cmp.Diff([]call{{Method: "List", WS: workspace}}, v.svc.calls); diff != "" {
		t.Errorf("service calls mismatch (-want +got):\n%s", diff)
	}
}

func TestList_EmptyIsAnArray(t *testing.T) {
	v := newEnv(t)
	res := v.do(http.MethodGet, "/settings/custom-providers", "")
	if res.status != 200 || res.body != "[]" {
		t.Errorf("GET with no providers = %d %s, want 200 []", res.status, res.body)
	}
}

func TestCreate_Answers201WithTheProvider(t *testing.T) {
	v := newEnv(t)
	v.svc.views = []app.View{full()}
	res := v.do(http.MethodPost, "/settings/custom-providers", minimalCreate)
	if res.status != 201 || res.body != wantFull {
		t.Errorf("POST = %d %s, want 201 %s", res.status, res.body, wantFull)
	}
}

func TestCreate_MapsTheBodyToTheInput(t *testing.T) {
	v := newEnv(t)
	v.svc.views = []app.View{bare()}
	body := `{"id":"custom:synapse","name":"  Synapse ","protocol":"anthropic","base_url":"https://gw/v1",` +
		`"auth_mode":"header","auth_header":"x-key","structured_output":"prompt_only","api_key":"k-1",` +
		`"extra_headers":[{"name":"X-A","value":"v-a"},{"name":"X-B"}],` +
		`"models":[{"id":"m-1","label":"One","pricing":{"promptPerM":1,"completionPerM":2.5}},` +
		`{"id":"m-2","label":null,"pricing":null},{"id":"m-3"}]}`
	if res := v.do(http.MethodPost, "/settings/custom-providers", body); res.status != 201 {
		t.Fatalf("POST = %d %s, want 201", res.status, res.body)
	}
	want := domain.Input{
		ID: "custom:synapse", Name: "  Synapse ", Protocol: custom.ProtocolAnthropic, BaseURL: "https://gw/v1",
		AuthMode: custom.AuthHeader, AuthHeader: str("x-key"), StructuredOutput: custom.ModePromptOnly,
		APIKey:       domain.KeyChange{Op: domain.KeyReplace, Value: "k-1"},
		ExtraHeaders: []domain.HeaderInput{{Name: "X-A", Value: str("v-a")}, {Name: "X-B"}},
		Models: []domain.Model{
			{ID: "m-1", Label: str("One"), Pricing: &domain.Pricing{PromptPerM: 1, CompletionPerM: 2.5}},
			{ID: "m-2"}, {ID: "m-3"},
		},
	}
	if diff := cmp.Diff([]call{{Method: "Create", WS: workspace, In: want}}, v.svc.calls); diff != "" {
		t.Errorf("Create input mismatch (-want +got):\n%s", diff)
	}
}

func TestCreate_OmittedOptionalFieldsStayEmpty(t *testing.T) {
	v := newEnv(t)
	v.svc.views = []app.View{bare()}
	if res := v.do(http.MethodPost, "/settings/custom-providers", minimalCreate); res.status != 201 {
		t.Fatalf("POST = %d %s, want 201", res.status, res.body)
	}
	want := domain.Input{ID: "custom:synapse", Name: "Synapse", Protocol: custom.ProtocolOpenAI, BaseURL: "https://x/v1"}
	if diff := cmp.Diff([]call{{Method: "Create", WS: workspace, In: want}}, v.svc.calls); diff != "" {
		t.Errorf("Create input mismatch (-want +got):\n%s", diff)
	}
	got := v.svc.calls[0].In
	if got.StructuredOutput != "" || got.APIKey.Op != domain.KeyKeep || got.AuthHeader != nil || got.ExtraHeaders != nil {
		t.Errorf("omitted fields = %+v, want structured_output \"\", key keep, no auth header, no headers", got)
	}
}

func TestUpdate_APIKeyOmittedNullString(t *testing.T) {
	const base = `"name":"S","protocol":"openai","base_url":"https://x/v1"`
	tests := []struct {
		name string
		body string
		want domain.KeyChange
	}{
		{"omitted keeps", `{` + base + `}`, domain.KeyChange{Op: domain.KeyKeep}},
		{"null removes", `{` + base + `,"api_key":null}`, domain.KeyChange{Op: domain.KeyRemove}},
		{"string replaces", `{` + base + `,"api_key":"new-key"}`, domain.KeyChange{Op: domain.KeyReplace, Value: "new-key"}},
		{"empty string replaces (the domain refuses it)", `{` + base + `,"api_key":""}`, domain.KeyChange{Op: domain.KeyReplace}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			v := newEnv(t)
			v.svc.views = []app.View{bare()}
			if res := v.do(http.MethodPut, "/settings/custom-providers/custom:synapse", tc.body); res.status != 200 {
				t.Fatalf("PUT %s = %d %s, want 200", tc.body, res.status, res.body)
			}
			if got := v.svc.calls[0].In.APIKey; got != tc.want {
				t.Errorf("PUT %s: api_key = %+v, want %+v", tc.body, got, tc.want)
			}
		})
	}
}

// EC14
func TestPathID_RawAndEncodedAddressTheSameProvider(t *testing.T) {
	const body = `{"name":"S","protocol":"openai","base_url":"https://x/v1"}`
	tests := []struct {
		name, method, path, body, wantID string
	}{
		{"PUT raw", http.MethodPut, "/settings/custom-providers/custom:synapse", body, "custom:synapse"},
		{"PUT encoded", http.MethodPut, "/settings/custom-providers/custom%3Asynapse", body, "custom:synapse"},
		{"PUT encoded lower hex", http.MethodPut, "/settings/custom-providers/custom%3asynapse", body, "custom:synapse"},
		{"DELETE raw", http.MethodDelete, "/settings/custom-providers/custom:synapse", "", "custom:synapse"},
		{"DELETE encoded", http.MethodDelete, "/settings/custom-providers/custom%3Asynapse", "", "custom:synapse"},
		{"DELETE double encoded is a literal", http.MethodDelete, "/settings/custom-providers/custom%253Asynapse", "", "custom%3Asynapse"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			v := newEnv(t)
			v.svc.views = []app.View{bare()}
			res := v.do(tc.method, tc.path, tc.body)
			if res.status != 200 {
				t.Fatalf("%s %s = %d %s, want 200", tc.method, tc.path, res.status, res.body)
			}
			if got := v.svc.calls[0].ID; got != tc.wantID {
				t.Errorf("%s %s: service id = %q, want %q", tc.method, tc.path, got, tc.wantID)
			}
		})
	}
}

func TestUpdate_IgnoresNoIDFromTheBodyAndUsesTheRoute(t *testing.T) {
	v := newEnv(t)
	v.svc.views = []app.View{full()}
	body := `{"name":"Synapse","protocol":"anthropic","base_url":"http://gw.corp/v1"}`
	res := v.do(http.MethodPut, "/settings/custom-providers/custom:synapse", body)
	if res.status != 200 || res.body != wantFull {
		t.Errorf("PUT = %d %s, want 200 %s", res.status, res.body, wantFull)
	}
	if c := v.svc.calls[0]; c.Method != "Update" || c.WS != workspace || c.ID != "custom:synapse" || c.In.ID != "" {
		t.Errorf("Update call = %+v, want workspace %q, route id, empty input id", c, workspace)
	}
}

func TestDelete_AnswersTheDeletedID(t *testing.T) {
	v := newEnv(t)
	res := v.do(http.MethodDelete, "/settings/custom-providers/custom:synapse", "")
	if res.status != 200 || res.body != `{"deleted":"custom:synapse"}` {
		t.Errorf("DELETE = %d %s, want 200 {\"deleted\":\"custom:synapse\"}", res.status, res.body)
	}
}

func TestErrors_PassThroughWithTheirCodes(t *testing.T) {
	inUse := apperr.Conflict("Custom provider custom:synapse is used and cannot be deleted").
		WithCode(app.CodeInUse).WithDetails(app.InUseDetails{
		Agents: []app.InUseAgent{{ID: "a-1", Name: "Reviewer"}}, FeatureModels: []string{"brief"}, DefaultModel: true,
	})
	tests := []struct {
		name         string
		method, path string
		body         string
		err          error
		status       int
		code         string
		message      string
		details      string
	}{
		{"not found on PUT", http.MethodPut, "/settings/custom-providers/custom:ghost", minimalUpdate,
			apperr.NotFound("Custom provider custom:ghost not found").WithCode(app.CodeNotFound),
			404, "custom_provider_not_found", "Custom provider custom:ghost not found", ""},
		{"not found on DELETE", http.MethodDelete, "/settings/custom-providers/custom:ghost", "",
			apperr.NotFound("Custom provider custom:ghost not found").WithCode(app.CodeNotFound),
			404, "custom_provider_not_found", "Custom provider custom:ghost not found", ""},
		{"exists", http.MethodPost, "/settings/custom-providers", minimalCreate,
			apperr.Conflict("Custom provider custom:synapse already exists").WithCode(app.CodeExists),
			409, "custom_provider_exists", "Custom provider custom:synapse already exists", ""},
		{"limit", http.MethodPost, "/settings/custom-providers", minimalCreate,
			apperr.Validation("At most 20 custom providers").WithCode(app.CodeLimit),
			422, "custom_provider_limit", "At most 20 custom providers", ""},
		{"in use", http.MethodDelete, "/settings/custom-providers/custom:synapse", "", inUse,
			409, "provider_in_use", "Custom provider custom:synapse is used and cannot be deleted",
			`{"agents":[{"id":"a-1","name":"Reviewer"}],"feature_models":["brief"],"default_model":true}`},
		{"validation from the service", http.MethodPost, "/settings/custom-providers", minimalCreate,
			httpx.NewValidationError(httpx.Issue{Code: "custom", Path: []any{"base_url"}, Message: "Base URL has no host"}),
			422, "validation_error", "Request validation failed", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			v := newEnv(t)
			v.svc.err = tc.err
			res := v.do(tc.method, tc.path, tc.body)
			e := decodeError(t, res)
			if res.status != tc.status || e.Error.Code != tc.code || e.Error.Message != tc.message {
				t.Errorf("%s %s = %d %s, want %d {code %q, message %q}", tc.method, tc.path, res.status, res.body, tc.status, tc.code, tc.message)
			}
			if tc.details != "" && string(e.Error.Details) != tc.details {
				t.Errorf("%s %s: details = %s, want %s", tc.method, tc.path, e.Error.Details, tc.details)
			}
		})
	}
}

const minimalUpdate = `{"name":"S","protocol":"openai","base_url":"https://x/v1"}`

// issue is the first detail of a 422, as the UI reads it.
type issue struct {
	Keyword string `json:"keyword"`
	Message string `json:"message"`
	Params  struct {
		Issue struct {
			Path []any `json:"path"`
		} `json:"issue"`
	} `json:"params"`
}

// EC30 AC2 AC42
func TestBodies_TypeErrorsAndStrictKeys(t *testing.T) {
	tests := []struct {
		name        string
		method      string
		path        string
		body        string
		wantKeyword string
		wantPath    []any
		wantMessage string
	}{
		{"not an object", http.MethodPost, "/settings/custom-providers", `[1]`, "invalid_type", []any{}, "Expected object, received array"},
		{"null body", http.MethodPost, "/settings/custom-providers", `null`, "invalid_type", []any{}, "Expected object, received null"},
		{"unknown key on POST", http.MethodPost, "/settings/custom-providers",
			`{"id":"custom:s","name":"S","protocol":"openai","base_url":"https://x","extra":1}`, "unrecognized_keys", []any{}, "Unrecognized key(s) in object: 'extra'"},
		{"unknown key on PUT", http.MethodPut, "/settings/custom-providers/custom:s",
			`{"name":"S","protocol":"openai","base_url":"https://x","plain_http":true}`, "unrecognized_keys", []any{}, "Unrecognized key(s) in object: 'plain_http'"},
		{"id in a PUT body is unknown", http.MethodPut, "/settings/custom-providers/custom:s",
			`{"id":"custom:s","name":"S","protocol":"openai","base_url":"https://x"}`, "unrecognized_keys", []any{}, "Unrecognized key(s) in object: 'id'"},
		{"unknown key in a header", http.MethodPut, "/settings/custom-providers/custom:s",
			`{"name":"S","protocol":"openai","base_url":"https://x","extra_headers":[{"name":"X-A","secret":"v"}]}`,
			"unrecognized_keys", []any{"extra_headers", float64(0)}, "Unrecognized key(s) in object: 'secret'"},
		{"unknown key in a model", http.MethodPut, "/settings/custom-providers/custom:s",
			`{"name":"S","protocol":"openai","base_url":"https://x","models":[{"id":"m","owned_by":"x"}]}`,
			"unrecognized_keys", []any{"models", float64(0)}, "Unrecognized key(s) in object: 'owned_by'"},
		{"unknown key in pricing", http.MethodPut, "/settings/custom-providers/custom:s",
			`{"name":"S","protocol":"openai","base_url":"https://x","models":[{"id":"m","pricing":{"promptPerM":1,"completionPerM":1,"currency":"EUR"}}]}`,
			"unrecognized_keys", []any{"models", float64(0), "pricing"}, "Unrecognized key(s) in object: 'currency'"},
		{"missing id", http.MethodPost, "/settings/custom-providers", `{"name":"S","protocol":"openai","base_url":"https://x"}`,
			"invalid_type", []any{"id"}, "Required"},
		{"missing name", http.MethodPut, "/settings/custom-providers/custom:s", `{"protocol":"openai","base_url":"https://x"}`,
			"invalid_type", []any{"name"}, "Required"},
		{"name is a number", http.MethodPut, "/settings/custom-providers/custom:s", `{"name":5,"protocol":"openai","base_url":"https://x"}`,
			"invalid_type", []any{"name"}, "Expected string, received number"},
		{"protocol is null", http.MethodPut, "/settings/custom-providers/custom:s", `{"name":"S","protocol":null,"base_url":"https://x"}`,
			"invalid_type", []any{"protocol"}, "Expected string, received null"},
		{"structured_output is null", http.MethodPut, "/settings/custom-providers/custom:s",
			`{"name":"S","protocol":"openai","base_url":"https://x","structured_output":null}`,
			"invalid_type", []any{"structured_output"}, "Expected string, received null"},
		{"auth_header is a number", http.MethodPut, "/settings/custom-providers/custom:s",
			`{"name":"S","protocol":"openai","base_url":"https://x","auth_header":1}`,
			"invalid_type", []any{"auth_header"}, "Expected string, received number"},
		{"api_key is a number", http.MethodPut, "/settings/custom-providers/custom:s",
			`{"name":"S","protocol":"openai","base_url":"https://x","api_key":12345}`,
			"invalid_type", []any{"api_key"}, "Expected string, received number"},
		{"extra_headers is an object", http.MethodPut, "/settings/custom-providers/custom:s",
			`{"name":"S","protocol":"openai","base_url":"https://x","extra_headers":{}}`,
			"invalid_type", []any{"extra_headers"}, "Expected array, received object"},
		{"header value is null", http.MethodPut, "/settings/custom-providers/custom:s",
			`{"name":"S","protocol":"openai","base_url":"https://x","extra_headers":[{"name":"X-A","value":null}]}`,
			"invalid_type", []any{"extra_headers", float64(0), "value"}, "Expected string, received null"},
		{"header without a name", http.MethodPut, "/settings/custom-providers/custom:s",
			`{"name":"S","protocol":"openai","base_url":"https://x","extra_headers":[{"value":"v"}]}`,
			"invalid_type", []any{"extra_headers", float64(0), "name"}, "Required"},
		{"models is null", http.MethodPut, "/settings/custom-providers/custom:s",
			`{"name":"S","protocol":"openai","base_url":"https://x","models":null}`,
			"invalid_type", []any{"models"}, "Expected array, received null"},
		{"model is a string", http.MethodPut, "/settings/custom-providers/custom:s",
			`{"name":"S","protocol":"openai","base_url":"https://x","models":["m"]}`,
			"invalid_type", []any{"models", float64(0)}, "Expected object, received string"},
		{"price is a string", http.MethodPut, "/settings/custom-providers/custom:s",
			`{"name":"S","protocol":"openai","base_url":"https://x","models":[{"id":"m","pricing":{"promptPerM":"1","completionPerM":2}}]}`,
			"invalid_type", []any{"models", float64(0), "pricing", "promptPerM"}, "Expected number, received string"},
		{"price without completion", http.MethodPut, "/settings/custom-providers/custom:s",
			`{"name":"S","protocol":"openai","base_url":"https://x","models":[{"id":"m","pricing":{"promptPerM":1}}]}`,
			"invalid_type", []any{"models", float64(0), "pricing", "completionPerM"}, "Required"},
		{"price beyond float64", http.MethodPut, "/settings/custom-providers/custom:s",
			`{"name":"S","protocol":"openai","base_url":"https://x","models":[{"id":"m","pricing":{"promptPerM":1e999,"completionPerM":2}}]}`,
			"too_big", []any{"models", float64(0), "pricing", "promptPerM"}, "Number must be less than or equal to 10000"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			v := newEnv(t)
			res := v.do(tc.method, tc.path, tc.body)
			e := decodeError(t, res)
			if res.status != 422 || e.Error.Code != "validation_error" {
				t.Fatalf("%s %s %s = %d %s, want 422 validation_error", tc.method, tc.path, tc.body, res.status, res.body)
			}
			var details []issue
			if err := json.Unmarshal(e.Error.Details, &details); err != nil || len(details) == 0 {
				t.Fatalf("details = %s, want a list of issues (%v)", e.Error.Details, err)
			}
			got := details[0]
			if got.Keyword != tc.wantKeyword || got.Message != tc.wantMessage {
				t.Errorf("first issue = {%s %q}, want {%s %q}", got.Keyword, got.Message, tc.wantKeyword, tc.wantMessage)
			}
			if diff := cmp.Diff(tc.wantPath, got.Params.Issue.Path); diff != "" {
				t.Errorf("first issue path mismatch (-want +got):\n%s", diff)
			}
			if len(v.svc.calls) != 0 {
				t.Errorf("service was called %d time(s) for a malformed body, want 0", len(v.svc.calls))
			}
		})
	}
}

func TestBodies_EveryIssueIsReported(t *testing.T) {
	v := newEnv(t)
	res := v.do(http.MethodPost, "/settings/custom-providers", `{"name":1,"protocol":2,"extra":3}`)
	e := decodeError(t, res)
	var details []issue
	if err := json.Unmarshal(e.Error.Details, &details); err != nil {
		t.Fatalf("details = %s: %v", e.Error.Details, err)
	}
	var got []string
	for _, d := range details {
		got = append(got, d.Keyword)
	}
	// `extra` is unknown (reported first, at the object); id and base_url are required; name and
	// protocol have the wrong type.
	want := []string{"unrecognized_keys", "invalid_type", "invalid_type", "invalid_type", "invalid_type"}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("issue keywords mismatch (-want +got):\n%s", diff)
	}
}

func TestBodies_EmptyBodyIsRequired(t *testing.T) {
	v := newEnv(t)
	res := v.do(http.MethodPost, "/settings/custom-providers", "")
	e := decodeError(t, res)
	if res.status != 422 || e.Error.Code != "validation_error" || !strings.Contains(string(e.Error.Details), `"Required"`) {
		t.Errorf("POST without a body = %d %s, want 422 validation_error Required", res.status, res.body)
	}
}

// Values are never judged here: the service owns every rule on them.
func TestBodies_ValuesAreLeftToTheService(t *testing.T) {
	v := newEnv(t)
	v.svc.views = []app.View{bare()}
	body := `{"id":"synapse","name":"","protocol":"grpc","base_url":"ftp://x","auth_mode":"basic","structured_output":"loose",` +
		`"models":[{"id":"","pricing":{"promptPerM":-1,"completionPerM":0.0000001}}]}`
	if res := v.do(http.MethodPost, "/settings/custom-providers", body); res.status != 201 {
		t.Fatalf("POST = %d %s, want 201 (the fake service accepts)", res.status, res.body)
	}
	got := v.svc.calls[0].In
	if got.ID != "synapse" || got.Protocol != "grpc" || got.StructuredOutput != "loose" || got.AuthMode != "basic" || got.BaseURL != "ftp://x" {
		t.Errorf("input = %+v, want the values passed on as sent", got)
	}
	if p := got.Models[0].Pricing; p == nil || p.PromptPerM != -1 || p.CompletionPerM != 0.0000001 {
		t.Errorf("pricing = %+v, want -1 and 1e-7 passed on", p)
	}
}

// AC16 NFR3
func TestGuards_ForeignHostOriginAndMediaType(t *testing.T) {
	routes := []struct {
		name, method, path, body string
		stateChanging            bool
	}{
		{"list", http.MethodGet, "/settings/custom-providers", "", false},
		{"create", http.MethodPost, "/settings/custom-providers", minimalCreate, true},
		{"update", http.MethodPut, "/settings/custom-providers/custom:synapse", minimalUpdate, true},
		{"delete", http.MethodDelete, "/settings/custom-providers/custom:synapse", "", true},
	}
	for _, rt := range routes {
		t.Run(rt.name+"/foreign host", func(t *testing.T) {
			v := newEnv(t)
			res := v.do(rt.method, rt.path, rt.body, "Host", "rebind.evil.example")
			if e := decodeError(t, res); res.status != 403 {
				t.Errorf("%s %s with a foreign Host = %d %s, want 403 envelope", rt.method, rt.path, res.status, e.Error.Code)
			}
			if len(v.svc.calls) != 0 {
				t.Errorf("service was called for a foreign Host")
			}
		})
		if !rt.stateChanging {
			continue
		}
		t.Run(rt.name+"/foreign origin", func(t *testing.T) {
			v := newEnv(t)
			res := v.do(rt.method, rt.path, rt.body, "Origin", foreignOrigin)
			if e := decodeError(t, res); res.status != 403 {
				t.Errorf("%s %s with a foreign Origin = %d %s, want 403 envelope", rt.method, rt.path, res.status, e.Error.Code)
			}
			if len(v.svc.calls) != 0 {
				t.Errorf("service was called for a foreign Origin")
			}
		})
		t.Run(rt.name+"/ui origin", func(t *testing.T) {
			v := newEnv(t)
			v.svc.views = []app.View{bare()}
			if res := v.do(rt.method, rt.path, rt.body, "Origin", uiOrigin); res.status >= 400 {
				t.Errorf("%s %s with the UI origin = %d %s, want success", rt.method, rt.path, res.status, res.body)
			}
		})
	}
	for _, rt := range routes[1:3] {
		t.Run(rt.name+"/form media type", func(t *testing.T) {
			v := newEnv(t)
			res := v.do(rt.method, rt.path, "a=b", "Content-Type", "application/x-www-form-urlencoded")
			if e := decodeError(t, res); res.status != 415 {
				t.Errorf("%s %s as a form = %d %s, want 415 envelope", rt.method, rt.path, res.status, e.Error.Code)
			}
		})
		t.Run(rt.name+"/text/plain is never read as a command", func(t *testing.T) {
			v := newEnv(t)
			res := v.do(rt.method, rt.path, rt.body, "Content-Type", "text/plain")
			e := decodeError(t, res)
			if res.status != 422 || e.Error.Code != "validation_error" || !strings.Contains(string(e.Error.Details), "Expected object, received string") {
				t.Errorf("%s %s as text/plain = %d %s, want 422 Expected object, received string", rt.method, rt.path, res.status, res.body)
			}
			if len(v.svc.calls) != 0 {
				t.Errorf("service was called for a text/plain body")
			}
		})
	}
}

// AC16: no response holds a key or a header value, on success or on a refused request.
func TestResponses_NeverHoldASecret(t *testing.T) {
	const keySecret, headerSecret = "sk-SECRET-KEY-0123", "hv-SECRET-VALUE-4567"
	body := `{"id":"custom:synapse","name":"S","protocol":"openai","base_url":"https://x/v1","api_key":"` + keySecret + `",` +
		`"extra_headers":[{"name":"X-Org","value":"` + headerSecret + `"}]}`
	badBody := `{"id":"custom:synapse","name":"S","protocol":"openai","base_url":"https://x/v1","api_key":["` + keySecret + `"],` +
		`"extra_headers":[{"name":"X-Org","value":"` + headerSecret + `","note":1}]}`

	v := newEnv(t)
	v.svc.views = []app.View{full()}
	responses := map[string]response{
		"create":       v.do(http.MethodPost, "/settings/custom-providers", body),
		"update":       v.do(http.MethodPut, "/settings/custom-providers/custom:synapse", strings.Replace(body, `"id":"custom:synapse",`, "", 1)),
		"list":         v.do(http.MethodGet, "/settings/custom-providers", ""),
		"delete":       v.do(http.MethodDelete, "/settings/custom-providers/custom:synapse", ""),
		"malformed":    v.do(http.MethodPost, "/settings/custom-providers", badBody),
		"foreignOrig":  v.do(http.MethodPost, "/settings/custom-providers", body, "Origin", foreignOrigin),
		"textPlain":    v.do(http.MethodPost, "/settings/custom-providers", body, "Content-Type", "text/plain"),
		"unsupported":  v.do(http.MethodPost, "/settings/custom-providers", body, "Content-Type", "application/xml"),
		"foreignHost":  v.do(http.MethodPost, "/settings/custom-providers", body, "Host", "evil.example"),
		"wrongMethod":  v.do(http.MethodPatch, "/settings/custom-providers/custom:synapse", body),
		"unknownRoute": v.do(http.MethodPost, "/settings/custom-providers/custom:synapse/x", body),
	}
	for name, res := range responses {
		for _, secret := range []string{keySecret, headerSecret} {
			if strings.Contains(res.body, secret) {
				t.Errorf("%s response (%d) contains the secret %q: %s", name, res.status, secret, res.body)
			}
		}
	}
	if responses["create"].status != 201 || responses["malformed"].status != 422 {
		t.Errorf("create = %d, malformed = %d, want 201 and 422", responses["create"].status, responses["malformed"].status)
	}
	if got := v.svc.calls[0].In.APIKey.Value; got != keySecret {
		t.Errorf("service saw api_key %q, want the key from the body", got)
	}
}

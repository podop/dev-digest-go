package gitlabconnhttp_test

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

	"dev-digest-go/backend/internal/gitlabconn/app"
	"dev-digest-go/backend/internal/gitlabconn/domain"
	gitlabconnhttp "dev-digest-go/backend/internal/gitlabconn/transport/http"
	"dev-digest-go/backend/internal/platform/apperr"
	"dev-digest-go/backend/internal/platform/httpx"
)

const (
	uiOrigin      = "http://localhost:3100"
	foreignOrigin = "http://evil.example"
	connID        = "0b6f0c1e-5f55-4a2e-9a4b-8f1f6f3f2a10"
	basePath      = "/settings/gitlab-connections"
	itemPath      = basePath + "/" + connID
)

// call is one use case call the fake saw.
type call struct {
	Method string
	ID     string
	Create app.CreateInput
	Update app.UpdateInput
}

// fakeService is a hand-written service: it records its calls and answers with views/err.
type fakeService struct {
	calls        []call
	views        []app.View
	disconnected int
	err          error
}

func (f *fakeService) List(context.Context) ([]app.View, error) {
	f.calls = append(f.calls, call{Method: "List"})
	return f.views, f.err
}

func (f *fakeService) Create(_ context.Context, in app.CreateInput) (app.View, error) {
	f.calls = append(f.calls, call{Method: "Create", Create: in})
	return f.view(), f.err
}

func (f *fakeService) Update(_ context.Context, id string, in app.UpdateInput) (app.View, error) {
	f.calls = append(f.calls, call{Method: "Update", ID: id, Update: in})
	return f.view(), f.err
}

func (f *fakeService) Delete(_ context.Context, id string) (int, error) {
	f.calls = append(f.calls, call{Method: "Delete", ID: id})
	return f.disconnected, f.err
}

func (f *fakeService) view() app.View {
	if len(f.views) > 0 {
		return f.views[0]
	}
	return app.View{}
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
	gitlabconnhttp.AddRoutes(e, svc)
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

var (
	created = time.Date(2026, 10, 26, 10, 11, 12, 345_678_000, time.UTC)
	updated = time.Date(2026, 10, 27, 8, 0, 0, 0, time.UTC)
)

func sample() app.View {
	return app.View{
		Connection: domain.Connection{
			ID: connID, Name: "Corp GitLab", BaseURL: "https://gitlab.corp.example/gitlab", Host: "gitlab.corp.example",
			CreatedAt: created, UpdatedAt: updated,
		},
		TokenConfigured: true,
		RepoCount:       2,
	}
}

const sampleJSON = `{"id":"` + connID + `","name":"Corp GitLab","base_url":"https://gitlab.corp.example/gitlab",` +
	`"host":"gitlab.corp.example","token_configured":true,"repo_count":2,` +
	`"created_at":"2026-10-26T10:11:12.345Z","updated_at":"2026-10-27T08:00:00.000Z"}`

// The body is compared as text: the field order is the contract's.
func TestList_WireShapeAndFieldOrder(t *testing.T) {
	v := newEnv(t)
	v.svc.views = []app.View{sample()}
	res := v.do(http.MethodGet, basePath, "")
	if res.status != 200 || res.body != "["+sampleJSON+"]" {
		t.Errorf("GET %s = %d %s, want 200 [%s]", basePath, res.status, res.body, sampleJSON)
	}
}

func TestList_EmptyIsAnArray(t *testing.T) {
	v := newEnv(t)
	if res := v.do(http.MethodGet, basePath, ""); res.status != 200 || res.body != "[]" {
		t.Errorf("GET %s with no connection = %d %s, want 200 []", basePath, res.status, res.body)
	}
}

func TestList_KeepsTheOrderOfTheService(t *testing.T) {
	v := newEnv(t)
	a, b := sample(), sample()
	a.Connection.Name, b.Connection.Name = "A", "B"
	v.svc.views = []app.View{a, b}
	res := v.do(http.MethodGet, basePath, "")
	var got []struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal([]byte(res.body), &got); err != nil {
		t.Fatalf("GET %s body %q: %v", basePath, res.body, err)
	}
	if len(got) != 2 || got[0].Name != "A" || got[1].Name != "B" {
		t.Errorf("GET %s names = %v, want [A B]", basePath, got)
	}
}

func TestCreate_Answers201WithTheConnection(t *testing.T) {
	v := newEnv(t)
	v.svc.views = []app.View{sample()}
	res := v.do(http.MethodPost, basePath, `{"base_url":"https://GitLab.Corp.Example:443/gitlab/","token":"glpat-secret"}`)
	if res.status != 201 || res.body != sampleJSON {
		t.Errorf("POST %s = %d %s, want 201 %s", basePath, res.status, res.body, sampleJSON)
	}
}

func TestCreate_MapsTheBodyToTheInput(t *testing.T) {
	tests := []struct {
		name string
		body string
		want app.CreateInput
	}{
		{"base url only", `{"base_url":"https://gl.example"}`,
			app.CreateInput{BaseURL: "https://gl.example"}},
		{"all keys, nothing trimmed", `{"name":"  Corp ","base_url":"https://gl.example","token":"glpat-1"}`,
			app.CreateInput{Name: "  Corp ", BaseURL: "https://gl.example", Token: app.TokenInput{Op: app.TokenReplace, Value: "glpat-1"}}},
		{"empty name is left to the service", `{"name":"","base_url":"https://gl.example"}`,
			app.CreateInput{BaseURL: "https://gl.example"}},
		{"empty strings are left to the service", `{"base_url":"","token":""}`,
			app.CreateInput{Token: app.TokenInput{Op: app.TokenReplace}}},
		{"null token", `{"base_url":"https://gl.example","token":null}`,
			app.CreateInput{BaseURL: "https://gl.example", Token: app.TokenInput{Op: app.TokenRemove}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			v := newEnv(t)
			if res := v.do(http.MethodPost, basePath, tc.body); res.status != 201 {
				t.Fatalf("POST %s %s = %d %s, want 201", basePath, tc.body, res.status, res.body)
			}
			want := []call{{Method: "Create", Create: tc.want}}
			if diff := cmp.Diff(want, v.svc.calls); diff != "" {
				t.Errorf("POST %s %s service calls mismatch (-want +got):\n%s", basePath, tc.body, diff)
			}
		})
	}
}

// AC4 / FR4: PUT token omitted keeps, null removes, a string replaces.
func TestUpdate_TokenOmittedNullString(t *testing.T) {
	tests := []struct {
		name string
		body string
		want app.UpdateInput
	}{
		{"omitted keeps", `{"name":"New"}`, app.UpdateInput{Name: str("New")}},
		{"empty body keeps everything", `{}`, app.UpdateInput{}},
		{"null removes", `{"token":null}`, app.UpdateInput{Token: app.TokenInput{Op: app.TokenRemove}}},
		{"string replaces", `{"base_url":"https://gl.example/x","token":"glpat-2"}`,
			app.UpdateInput{BaseURL: str("https://gl.example/x"), Token: app.TokenInput{Op: app.TokenReplace, Value: "glpat-2"}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			v := newEnv(t)
			v.svc.views = []app.View{sample()}
			res := v.do(http.MethodPut, itemPath, tc.body)
			if res.status != 200 || res.body != sampleJSON {
				t.Fatalf("PUT %s %s = %d %s, want 200 %s", itemPath, tc.body, res.status, res.body, sampleJSON)
			}
			want := []call{{Method: "Update", ID: connID, Update: tc.want}}
			if diff := cmp.Diff(want, v.svc.calls); diff != "" {
				t.Errorf("PUT %s %s service calls mismatch (-want +got):\n%s", itemPath, tc.body, diff)
			}
		})
	}
}

func TestDelete_AnswersDeletedAndDisconnectedRepos(t *testing.T) {
	v := newEnv(t)
	v.svc.disconnected = 2
	res := v.do(http.MethodDelete, itemPath, "")
	want := `{"deleted":"` + connID + `","disconnected_repos":2}`
	if res.status != 200 || res.body != want {
		t.Errorf("DELETE %s = %d %s, want 200 %s", itemPath, res.status, res.body, want)
	}
	if diff := cmp.Diff([]call{{Method: "Delete", ID: connID}}, v.svc.calls); diff != "" {
		t.Errorf("DELETE service calls mismatch (-want +got):\n%s", diff)
	}
}

// :id is passed to the service percent-decoded once; the service decides what is a known id.
func TestPathID_IsPassedDecodedOnce(t *testing.T) {
	upper := strings.ToUpper(connID)
	tests := []struct{ name, path, want string }{
		{"upper case", basePath + "/" + upper, upper},
		{"encoded dashes", basePath + "/" + strings.ReplaceAll(connID, "-", "%2D"), connID},
		{"not a uuid", basePath + "/nope", "nope"},
		{"double encoded stays literal", basePath + "/a%252Db", "a%2Db"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			v := newEnv(t)
			if res := v.do(http.MethodDelete, tc.path, ""); res.status != 200 {
				t.Fatalf("DELETE %s = %d %s, want 200", tc.path, res.status, res.body)
			}
			if len(v.svc.calls) != 1 || v.svc.calls[0].ID != tc.want {
				t.Errorf("DELETE %s service calls = %+v, want id %q", tc.path, v.svc.calls, tc.want)
			}
		})
	}
}

func TestErrors_PassThroughWithTheirCodes(t *testing.T) {
	tests := []struct {
		name       string
		method     string
		path, body string
		err        error
		wantStatus int
		wantCode   string
		wantMsg    string
	}{
		{"exists", http.MethodPost, basePath, `{"base_url":"https://gl.example"}`,
			apperr.Conflict("A GitLab connection for gl.example already exists").WithCode("gitlab_connection_exists"),
			409, "gitlab_connection_exists", "A GitLab connection for gl.example already exists"},
		{"limit", http.MethodPost, basePath, `{"base_url":"https://gl.example"}`,
			apperr.Validation("At most 20 GitLab connections").WithCode("gitlab_connection_limit"),
			422, "gitlab_connection_limit", "At most 20 GitLab connections"},
		{"validation", http.MethodPost, basePath, `{"base_url":"http://gl.example"}`,
			httpx.NewValidationError(httpx.Issue{Code: "custom", Path: []any{"base_url"}, Message: "Base URL must start with https://"}),
			422, "validation_error", httpx.ValidationMessage},
		{"update not found", http.MethodPut, itemPath, `{}`,
			apperr.NotFound("GitLab connection " + connID + " not found").WithCode("gitlab_connection_not_found"),
			404, "gitlab_connection_not_found", "GitLab connection " + connID + " not found"},
		{"delete not found", http.MethodDelete, itemPath, "",
			apperr.NotFound("GitLab connection " + connID + " not found").WithCode("gitlab_connection_not_found"),
			404, "gitlab_connection_not_found", "GitLab connection " + connID + " not found"},
		{"internal error hides its cause", http.MethodPost, basePath, `{"base_url":"https://gl.example","token":"glpat-SECRET"}`,
			errors.New("write secrets: glpat-SECRET"), 500, "internal_error", ""},
		{"list failure", http.MethodGet, basePath, "", errors.New("db down"), 500, "internal_error", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			v := newEnv(t)
			v.svc.err = tc.err
			res := v.do(tc.method, tc.path, tc.body)
			e := decodeError(t, res)
			if res.status != tc.wantStatus || e.Error.Code != tc.wantCode {
				t.Fatalf("%s %s = %d %s, want %d %s", tc.method, tc.path, res.status, res.body, tc.wantStatus, tc.wantCode)
			}
			if tc.wantMsg != "" && e.Error.Message != tc.wantMsg {
				t.Errorf("%s %s message = %q, want %q", tc.method, tc.path, e.Error.Message, tc.wantMsg)
			}
			if strings.Contains(res.body, "glpat-SECRET") || strings.Contains(res.body, "db down") {
				t.Errorf("%s %s leaks the cause: %s", tc.method, tc.path, res.body)
			}
		})
	}
}

// issues returns the (keyword, path, message) triples of a 422 envelope.
func issues(t *testing.T, res response) []struct {
	Keyword string
	Path    []any
	Message string
} {
	t.Helper()
	if res.status != 422 {
		t.Fatalf("status = %d %s, want 422", res.status, res.body)
	}
	e := decodeError(t, res)
	if e.Error.Code != "validation_error" {
		t.Fatalf("code = %s, want validation_error", e.Error.Code)
	}
	var details []struct {
		Keyword string `json:"keyword"`
		Params  struct {
			Issue struct {
				Path    []any  `json:"path"`
				Message string `json:"message"`
			} `json:"issue"`
		} `json:"params"`
	}
	if err := json.Unmarshal(e.Error.Details, &details); err != nil {
		t.Fatalf("details %s: %v", e.Error.Details, err)
	}
	out := make([]struct {
		Keyword string
		Path    []any
		Message string
	}, len(details))
	for i, d := range details {
		out[i].Keyword, out[i].Path, out[i].Message = d.Keyword, d.Params.Issue.Path, d.Params.Issue.Message
	}
	return out
}

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
		{"not an object", http.MethodPost, basePath, `[1]`, "invalid_type", []any{}, "Expected object, received array"},
		{"null body", http.MethodPost, basePath, `null`, "invalid_type", []any{}, "Expected object, received null"},
		{"empty body on POST", http.MethodPost, basePath, ``, "invalid_type", []any{}, "Required"},
		{"base_url missing", http.MethodPost, basePath, `{"name":"x"}`, "invalid_type", []any{"base_url"}, "Required"},
		{"base_url number", http.MethodPost, basePath, `{"base_url":1}`, "invalid_type", []any{"base_url"}, "Expected string, received number"},
		{"base_url null", http.MethodPut, itemPath, `{"base_url":null}`, "invalid_type", []any{"base_url"}, "Expected string, received null"},
		{"name null", http.MethodPost, basePath, `{"name":null,"base_url":"https://x"}`, "invalid_type", []any{"name"}, "Expected string, received null"},
		{"name object", http.MethodPut, itemPath, `{"name":{}}`, "invalid_type", []any{"name"}, "Expected string, received object"},
		{"token number", http.MethodPost, basePath, `{"base_url":"https://x","token":12}`, "invalid_type", []any{"token"}, "Expected string, received number"},
		{"token bool", http.MethodPut, itemPath, `{"token":false}`, "invalid_type", []any{"token"}, "Expected string, received boolean"},
		{"token array", http.MethodPut, itemPath, `{"token":["glpat-x"]}`, "invalid_type", []any{"token"}, "Expected string, received array"},
		{"unknown key on POST", http.MethodPost, basePath, `{"base_url":"https://x","extra":1}`, "unrecognized_keys", []any{}, "Unrecognized key(s) in object: 'extra'"},
		{"unknown keys on PUT, sorted", http.MethodPut, itemPath, `{"repo_count":3,"id":"x","host":"h"}`, "unrecognized_keys", []any{}, "Unrecognized key(s) in object: 'host', 'id', 'repo_count'"},
		{"token_configured is output only", http.MethodPut, itemPath, `{"token_configured":true}`, "unrecognized_keys", []any{}, "Unrecognized key(s) in object: 'token_configured'"},
		{"case sensitive keys", http.MethodPost, basePath, `{"base_url":"https://x","Token":"glpat-1"}`, "unrecognized_keys", []any{}, "Unrecognized key(s) in object: 'Token'"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			v := newEnv(t)
			got := issues(t, v.do(tc.method, tc.path, tc.body))
			if len(got) != 1 || got[0].Keyword != tc.wantKeyword || got[0].Message != tc.wantMessage ||
				cmp.Diff(tc.wantPath, got[0].Path) != "" {
				t.Errorf("%s %s %s issues = %+v, want one %s at %v %q", tc.method, tc.path, tc.body, got, tc.wantKeyword, tc.wantPath, tc.wantMessage)
			}
			if len(v.svc.calls) != 0 {
				t.Errorf("service was called for an invalid body")
			}
		})
	}
}

// Every issue is reported, fields in the contract's order, unknown keys last.
func TestBodies_EveryIssueIsReported(t *testing.T) {
	v := newEnv(t)
	got := issues(t, v.do(http.MethodPost, basePath, `{"token":1,"extra":true,"name":2,"base_url":3}`))
	var paths []string
	for _, is := range got {
		paths = append(paths, is.Keyword+"@"+strings.Join(pathStrings(is.Path), "/"))
	}
	want := []string{"invalid_type@name", "invalid_type@base_url", "invalid_type@token", "unrecognized_keys@"}
	if diff := cmp.Diff(want, paths); diff != "" {
		t.Errorf("issue order mismatch (-want +got):\n%s", diff)
	}
}

func pathStrings(p []any) []string {
	out := make([]string, len(p))
	for i, e := range p {
		s, _ := e.(string)
		out[i] = s
	}
	return out
}

// The handler checks types and keys only; the values are the service's (domain.Check*).
func TestBodies_ValuesAreLeftToTheService(t *testing.T) {
	v := newEnv(t)
	body := `{"name":"` + strings.Repeat("n", 500) + `","base_url":"ftp://x y","token":"has space"}`
	if res := v.do(http.MethodPost, basePath, body); res.status != 201 {
		t.Fatalf("POST with out-of-range values = %d %s, want 201 (the fake service accepts)", res.status, res.body)
	}
	if len(v.svc.calls) != 1 || v.svc.calls[0].Create.BaseURL != "ftp://x y" || v.svc.calls[0].Create.Token.Value != "has space" {
		t.Errorf("service calls = %+v, want the values untouched", v.svc.calls)
	}
}

func TestGuards_ForeignHostOriginAndMediaType(t *testing.T) {
	routes := []struct {
		name, method, path, body string
		stateChanging            bool
	}{
		{"list", http.MethodGet, basePath, "", false},
		{"create", http.MethodPost, basePath, `{"base_url":"https://gl.example"}`, true},
		{"update", http.MethodPut, itemPath, `{"name":"x"}`, true},
		{"delete", http.MethodDelete, itemPath, "", true},
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
			v.svc.views = []app.View{sample()}
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
			if len(v.svc.calls) != 0 {
				t.Errorf("service was called for a form body")
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

// NFR6 / FR4: no response holds a token, on success or on a refused request.
func TestResponses_NeverHoldAToken(t *testing.T) {
	const token = "glpat-SECRET-TOKEN-0123456789"
	body := `{"base_url":"https://gl.example","token":"` + token + `"}`
	badBody := `{"base_url":"https://gl.example","token":["` + token + `"],"extra":1}`

	v := newEnv(t)
	v.svc.views = []app.View{sample()}
	responses := map[string]response{
		"create":       v.do(http.MethodPost, basePath, body),
		"update":       v.do(http.MethodPut, itemPath, body),
		"list":         v.do(http.MethodGet, basePath, ""),
		"delete":       v.do(http.MethodDelete, itemPath, ""),
		"malformed":    v.do(http.MethodPost, basePath, badBody),
		"foreignOrig":  v.do(http.MethodPost, basePath, body, "Origin", foreignOrigin),
		"textPlain":    v.do(http.MethodPost, basePath, body, "Content-Type", "text/plain"),
		"unsupported":  v.do(http.MethodPost, basePath, body, "Content-Type", "application/xml"),
		"foreignHost":  v.do(http.MethodPost, basePath, body, "Host", "evil.example"),
		"wrongMethod":  v.do(http.MethodPatch, itemPath, body),
		"badID":        v.do(http.MethodPut, basePath+"/"+token, body),
		"unknownRoute": v.do(http.MethodPost, itemPath+"/x", body),
	}
	for name, res := range responses {
		if strings.Contains(res.body, token) {
			t.Errorf("%s response (%d) contains the token: %s", name, res.status, res.body)
		}
	}
	if responses["create"].status != 201 || responses["malformed"].status != 422 {
		t.Errorf("create = %d, malformed = %d, want 201 and 422", responses["create"].status, responses["malformed"].status)
	}
	// The wire shape has no token key at all, only the flag.
	var item map[string]any
	if err := json.Unmarshal([]byte(responses["create"].body), &item); err != nil {
		t.Fatalf("create body %q: %v", responses["create"].body, err)
	}
	for _, k := range []string{"token", "Token", "access_token", "secret"} {
		if _, ok := item[k]; ok {
			t.Errorf("create response has the key %q", k)
		}
	}
	if item["token_configured"] != true {
		t.Errorf("create response token_configured = %v, want true", item["token_configured"])
	}
	if got := v.svc.calls[0].Create.Token.Value; got != token {
		t.Errorf("service saw token %q, want the token from the body", got)
	}
}

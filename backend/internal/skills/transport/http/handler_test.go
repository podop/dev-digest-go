package httpapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/labstack/echo/v5"

	"dev-digest-go/backend/internal/platform/apperr"
	"dev-digest-go/backend/internal/platform/httpx"
	"dev-digest-go/backend/internal/skills/app"
	"dev-digest-go/backend/internal/skills/domain"
	httpapi "dev-digest-go/backend/internal/skills/transport/http"
	"dev-digest-go/backend/internal/tenancy"
)

const (
	wsID     = "11111111-1111-4111-8111-111111111111"
	someUUID = "00000000-0000-4000-8000-000000000000"
)

// fakeServices implements the three service ports of the handlers. A call records its
// arguments; every method answers with the canned values below.
type fakeServices struct {
	calls []string

	skill   domain.Skill
	err     error
	preview app.Preview
	merged  app.MergeResult

	gotCreate app.CreateInput
	gotPatch  domain.Patch
	gotImport app.ImportRequest
	gotMerge  app.MergeInput
	gotFilter domain.CatalogFilter
	gotDays   int
	gotVer    int
}

func (f *fakeServices) rec(call string) { f.calls = append(f.calls, call) }

func (f *fakeServices) List(context.Context, string) ([]domain.Skill, error) {
	f.rec("List")
	return nil, f.err
}

func (f *fakeServices) Get(context.Context, string, string) (domain.Skill, error) {
	f.rec("Get")
	return f.skill, f.err
}

func (f *fakeServices) Create(_ context.Context, _ string, in app.CreateInput) (domain.Skill, error) {
	f.rec("Create")
	f.gotCreate = in
	return f.skill, f.err
}

func (f *fakeServices) Update(_ context.Context, _, _ string, p domain.Patch) (domain.Skill, error) {
	f.rec("Update")
	f.gotPatch = p
	return f.skill, f.err
}

func (f *fakeServices) Restore(_ context.Context, _, _ string, v int) (domain.Skill, error) {
	f.rec("Restore")
	f.gotVer = v
	return f.skill, f.err
}

func (f *fakeServices) Delete(context.Context, string, string) error {
	f.rec("Delete")
	return f.err
}

func (f *fakeServices) ListVersions(context.Context, string, string) ([]domain.Version, error) {
	f.rec("ListVersions")
	return nil, f.err
}

func (f *fakeServices) GetVersion(_ context.Context, _, _ string, v int) (domain.Version, error) {
	f.rec("GetVersion")
	f.gotVer = v
	return domain.Version{}, f.err
}

func (f *fakeServices) Agents(context.Context, string, string) ([]domain.AgentRef, error) {
	f.rec("Agents")
	return nil, f.err
}

func (f *fakeServices) Stats(_ context.Context, _, _ string, days int) (domain.Stats, error) {
	f.rec("Stats")
	f.gotDays = days
	return domain.Stats{}, f.err
}

func (f *fakeServices) StatsSummary(_ context.Context, _ string, days int) ([]domain.StatsSummary, error) {
	f.rec("StatsSummary")
	f.gotDays = days
	return nil, f.err
}

func (f *fakeServices) Community(fl domain.CatalogFilter) []domain.CatalogEntry {
	f.rec("Community")
	f.gotFilter = fl
	return nil
}

func (f *fakeServices) Preview(_ context.Context, r app.ImportRequest) (app.Preview, error) {
	f.rec("Preview")
	f.gotImport = r
	return f.preview, f.err
}

func (f *fakeServices) Merge(_ context.Context, _, _ string, in app.MergeInput) (app.MergeResult, error) {
	f.rec("Merge")
	f.gotMerge = in
	return f.merged, f.err
}

type fixedTenant struct{}

func (fixedTenant) Resolve(context.Context) (tenancy.Identity, error) {
	return tenancy.Identity{WorkspaceID: wsID, UserID: "u"}, nil
}

type response struct {
	status int
	body   string
}

func newEcho(f *fakeServices) *echo.Echo {
	e := httpx.New(httpx.Options{BodyLimits: httpapi.BodyLimits()})
	httpapi.AddRoutes(e, f, f, f, fixedTenant{})
	return e
}

func do(t *testing.T, e *echo.Echo, method, path, body string, headers ...string) response {
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
	e.ServeHTTP(rec, req)
	return response{rec.Code, strings.TrimSpace(rec.Body.String())}
}

type issue struct{ Path, Message, Code string }

// errorOf decodes an error envelope and returns its code, message and 422 issues.
func errorOf(t *testing.T, r response) (code, message string, issues []issue, details json.RawMessage) {
	t.Helper()
	var env struct {
		Error struct {
			Code    string          `json:"code"`
			Message string          `json:"message"`
			Details json.RawMessage `json:"details"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(r.body), &env); err != nil {
		t.Fatalf("body %q is not an error envelope: %v", r.body, err)
	}
	var ds []struct {
		InstancePath string `json:"instancePath"`
		Message      string `json:"message"`
		Keyword      string `json:"keyword"`
	}
	if r.status == http.StatusUnprocessableEntity && json.Unmarshal(env.Error.Details, &ds) == nil {
		for _, d := range ds {
			issues = append(issues, issue{d.InstancePath, d.Message, d.Keyword})
		}
	}
	return env.Error.Code, env.Error.Message, issues, env.Error.Details
}

func TestRoutesAreRegisteredAndBodyLimitsMatchThem(t *testing.T) {
	e := newEcho(&fakeServices{})
	want := []string{
		"GET /skills", "GET /skills/stats", "GET /skills/community", "POST /skills/import/preview",
		"GET /skills/:id", "POST /skills", "PUT /skills/:id", "DELETE /skills/:id",
		"GET /skills/:id/versions", "GET /skills/:id/versions/:version", "POST /skills/:id/versions/:version/restore",
		"GET /skills/:id/agents", "GET /skills/:id/stats", "POST /repos/:id/conventions/skill",
	}
	var got []string
	for _, r := range e.Router().Routes() {
		if r.Method != "echo_route_not_found" {
			got = append(got, r.Method+" "+r.Path)
		}
	}
	slices.Sort(want)
	slices.Sort(got)
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("registered routes mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff(map[string]int64{"POST /skills/import/preview": 2162688}, httpapi.BodyLimits()); diff != "" {
		t.Errorf("BodyLimits mismatch (-want +got):\n%s", diff)
	}
	for key := range httpapi.BodyLimits() {
		if !slices.Contains(got, key) {
			t.Errorf("BodyLimits key %q is not a registered route", key)
		}
	}
}

func TestStaticRoutesWinOverTheIDRoute(t *testing.T) {
	f := &fakeServices{}
	e := newEcho(f)
	for path, wantCall := range map[string]string{
		"/skills/stats":     "StatsSummary",
		"/skills/community": "Community",
	} {
		f.calls = nil
		if r := do(t, e, http.MethodGet, path, ""); r.status != 200 {
			t.Errorf("GET %s = %d %s, want 200", path, r.status, r.body)
		}
		if diff := cmp.Diff([]string{wantCall}, f.calls); diff != "" {
			t.Errorf("GET %s called (-want +got):\n%s", path, diff)
		}
	}
	f.calls = nil
	if r := do(t, e, http.MethodPost, "/skills/import/preview", `{"kind":"community","id":"x"}`); r.status != 200 {
		t.Errorf("POST /skills/import/preview = %d %s, want 200", r.status, r.body)
	}
	if diff := cmp.Diff([]string{"Preview"}, f.calls); diff != "" {
		t.Errorf("POST /skills/import/preview called (-want +got):\n%s", diff)
	}
}

func TestValidation(t *testing.T) {
	long := func(n int) string { return strings.Repeat("a", n) }
	tests := []struct {
		name   string
		method string
		path   string
		body   string
		want   []issue
	}{
		// ids and versions
		{"bad id", "GET", "/skills/nope", "", []issue{{"/id", "Invalid uuid", "invalid_string"}}},
		{"bad id on put", "PUT", "/skills/nope", `{}`, []issue{{"/id", "Invalid uuid", "invalid_string"}}},
		{"bad id on delete", "DELETE", "/skills/nope", "", []issue{{"/id", "Invalid uuid", "invalid_string"}}},
		{"bad id and version", "GET", "/skills/nope/versions/0", "", []issue{
			{"/id", "Invalid uuid", "invalid_string"}, {"/version", "Number must be greater than 0", "too_small"},
		}},
		{"version 1e2", "GET", "/skills/" + someUUID + "/versions/1e2", "", []issue{{"/version", "Expected number, received nan", "invalid_type"}}},
		{"version too big", "POST", "/skills/" + someUUID + "/versions/2147483648/restore", "", []issue{{"/version", "Number must be less than or equal to 2147483647", "too_big"}}},
		// days
		{"days 0", "GET", "/skills/stats?days=0", "", []issue{{"/days", "Number must be greater than or equal to 1", "too_small"}}},
		{"days 366", "GET", "/skills/" + someUUID + "/stats?days=366", "", []issue{{"/days", "Number must be less than or equal to 365", "too_big"}}},
		{"days abc", "GET", "/skills/stats?days=abc", "", []issue{{"/days", "Expected number, received nan", "invalid_type"}}},
		{"days 1.5", "GET", "/skills/stats?days=1.5", "", []issue{{"/days", "Expected integer, received float", "invalid_type"}}},
		{"days empty", "GET", "/skills/stats?days=", "", []issue{{"/days", "Number must be greater than or equal to 1", "too_small"}}},
		// community query
		{"q too long", "GET", "/skills/community?q=" + long(201), "", []issue{{"/q", "String must contain at most 200 character(s)", "too_big"}}},
		{"tag and lang too long", "GET", "/skills/community?tag=" + long(51) + "&lang=" + long(51), "", []issue{
			{"/tag", "String must contain at most 50 character(s)", "too_big"}, {"/lang", "String must contain at most 50 character(s)", "too_big"},
		}},
		// create
		{"create empty object", "POST", "/skills", `{}`, []issue{
			{"/name", "Required", "invalid_type"}, {"/type", "Required", "invalid_type"}, {"/body", "Required", "invalid_type"},
		}},
		{"create name not a slug", "POST", "/skills", `{"name":"Bad Name","type":"custom","body":"b"}`, []issue{
			{"/name", "Use a kebab-case slug: lowercase letters, digits and single dashes", "invalid_string"},
		}},
		{"create name empty", "POST", "/skills", `{"name":"","type":"custom","body":"b"}`, []issue{
			{"/name", "String must contain at least 1 character(s)", "too_small"},
			{"/name", "Use a kebab-case slug: lowercase letters, digits and single dashes", "invalid_string"},
		}},
		{"create bad type", "POST", "/skills", `{"name":"x","type":"nope","body":"b"}`, []issue{
			{"/type", "Invalid enum value. Expected 'rubric' | 'convention' | 'security' | 'custom', received 'nope'", "invalid_enum_value"},
		}},
		{"create type number", "POST", "/skills", `{"name":"x","type":3,"body":"b"}`, []issue{
			{"/type", "Expected 'rubric' | 'convention' | 'security' | 'custom', received number", "invalid_type"},
		}},
		{"create extracted source refused", "POST", "/skills", `{"name":"x","type":"custom","body":"b","source":"extracted"}`, []issue{
			{"/source", "Invalid enum value. Expected 'manual' | 'imported_file' | 'imported_url' | 'community', received 'extracted'", "invalid_enum_value"},
		}},
		{"create body too long", "POST", "/skills", `{"name":"x","type":"custom","body":"` + long(20001) + `"}`, []issue{
			{"/body", "String must contain at most 20000 character(s)", "too_big"},
		}},
		{"create emoji description counts UTF-16", "POST", "/skills", `{"name":"x","type":"custom","body":"b","description":"` + strings.Repeat("😀", 151) + `"}`, []issue{
			{"/description", "String must contain at most 300 character(s)", "too_big"},
		}},
		{"create source_ref too long", "POST", "/skills", `{"name":"x","type":"custom","body":"b","source_ref":"` + long(501) + `"}`, []issue{
			{"/source_ref", "String must contain at most 500 character(s)", "too_big"},
		}},
		{"create enabled string", "POST", "/skills", `{"name":"x","type":"custom","body":"b","enabled":"yes"}`, []issue{
			{"/enabled", "Expected boolean, received string", "invalid_type"},
		}},
		{"create body is an array", "POST", "/skills", `[]`, []issue{{"/", "Expected object, received array", "invalid_type"}}},
		// update
		{"update null name", "PUT", "/skills/" + someUUID, `{"name":null}`, []issue{{"/name", "Expected string, received null", "invalid_type"}}},
		{"update empty body text", "PUT", "/skills/" + someUUID, `{"body":""}`, []issue{{"/body", "String must contain at least 1 character(s)", "too_small"}}},
		{"update base_version 0", "PUT", "/skills/" + someUUID, `{"base_version":0}`, []issue{{"/base_version", "Number must be greater than 0", "too_small"}}},
		{"update base_version 1.5", "PUT", "/skills/" + someUUID, `{"base_version":1.5}`, []issue{{"/base_version", "Expected integer, received float", "invalid_type"}}},
		{"update base_version text", "PUT", "/skills/" + someUUID, `{"base_version":"2"}`, []issue{{"/base_version", "Expected number, received string", "invalid_type"}}},
		// import
		{"import no kind", "POST", "/skills/import/preview", `{}`, []issue{
			{"/kind", "Invalid discriminator value. Expected 'file' | 'url' | 'community'", "invalid_union_discriminator"},
		}},
		{"import unknown kind", "POST", "/skills/import/preview", `{"kind":"ftp"}`, []issue{
			{"/kind", "Invalid discriminator value. Expected 'file' | 'url' | 'community'", "invalid_union_discriminator"},
		}},
		{"import file without fields", "POST", "/skills/import/preview", `{"kind":"file"}`, []issue{
			{"/filename", "Required", "invalid_type"}, {"/content_base64", "Required", "invalid_type"},
		}},
		{"import file name too long", "POST", "/skills/import/preview", `{"kind":"file","filename":"` + long(256) + `","content_base64":"QQ=="}`, []issue{
			{"/filename", "String must contain at most 255 character(s)", "too_big"},
		}},
		{"import url empty", "POST", "/skills/import/preview", `{"kind":"url","url":""}`, []issue{{"/url", "String must contain at least 1 character(s)", "too_small"}}},
		{"import url too long", "POST", "/skills/import/preview", `{"kind":"url","url":"` + long(2001) + `"}`, []issue{{"/url", "String must contain at most 2000 character(s)", "too_big"}}},
		{"import community id too long", "POST", "/skills/import/preview", `{"kind":"community","id":"` + long(101) + `"}`, []issue{{"/id", "String must contain at most 100 character(s)", "too_big"}}},
		// merge
		{"merge empty object", "POST", "/repos/" + someUUID + "/conventions/skill", `{}`, []issue{
			{"/convention_ids", "Required", "invalid_type"}, {"/name", "Required", "invalid_type"}, {"/body", "Required", "invalid_type"},
		}},
		{"merge no conventions", "POST", "/repos/" + someUUID + "/conventions/skill", `{"convention_ids":[],"name":"x","body":"b"}`, []issue{
			{"/convention_ids", "Array must contain at least 1 element(s)", "too_small"},
		}},
		{"merge bad convention id", "POST", "/repos/" + someUUID + "/conventions/skill", `{"convention_ids":["x"],"name":"x","body":"b"}`, []issue{
			{"/convention_ids/0", "Invalid uuid", "invalid_string"},
		}},
		{"merge too many agents", "POST", "/repos/" + someUUID + "/conventions/skill", `{"convention_ids":["` + someUUID + `"],"name":"x","body":"b","agent_ids":[` + strings.TrimSuffix(strings.Repeat(`"`+someUUID+`",`, 51), ",") + `]}`, []issue{
			{"/agent_ids", "Array must contain at most 50 element(s)", "too_big"},
		}},
		{"merge bad repo id", "POST", "/repos/nope/conventions/skill", `{}`, []issue{{"/id", "Invalid uuid", "invalid_string"}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeServices{}
			r := do(t, newEcho(f), tc.method, tc.path, tc.body)
			if r.status != http.StatusUnprocessableEntity {
				t.Fatalf("%s %s = %d %.200s, want 422", tc.method, tc.path, r.status, r.body)
			}
			code, message, issues, _ := errorOf(t, r)
			if code != "validation_error" || message != "Request validation failed" {
				t.Errorf("error = %q %q, want validation_error / Request validation failed", code, message)
			}
			if diff := cmp.Diff(tc.want, issues); diff != "" {
				t.Errorf("%s %s issues mismatch (-want +got):\n%s", tc.method, tc.path, diff)
			}
			if len(f.calls) != 0 {
				t.Errorf("%s %s reached the use case (%v) despite the invalid request", tc.method, tc.path, f.calls)
			}
		})
	}
}

func TestPlainTextBodyIsNotReadAsJSON(t *testing.T) {
	f := &fakeServices{}
	r := do(t, newEcho(f), http.MethodPost, "/skills", `{"name":"x","type":"custom","body":"b"}`, "Content-Type", "text/plain")
	code, _, issues, _ := errorOf(t, r)
	if r.status != 422 || code != "validation_error" || len(issues) != 1 || issues[0].Message != "Expected object, received string" {
		t.Errorf("text/plain POST /skills = %d %s, want 422 Expected object, received string", r.status, r.body)
	}
	if len(f.calls) != 0 {
		t.Errorf("text/plain POST reached the use case: %v", f.calls)
	}
}

func TestForeignOriginIsRefused(t *testing.T) {
	e := httpx.New(httpx.Options{CORSOrigins: []string{"http://localhost:3100"}, BodyLimits: httpapi.BodyLimits()})
	f := &fakeServices{}
	httpapi.AddRoutes(e, f, f, f, fixedTenant{})
	for _, tc := range []struct{ method, path, body string }{
		{"POST", "/skills", `{"name":"x","type":"custom","body":"b"}`},
		{"PUT", "/skills/" + someUUID, `{}`},
		{"DELETE", "/skills/" + someUUID, ""},
		{"POST", "/skills/import/preview", `{"kind":"community","id":"x"}`},
		{"POST", "/repos/" + someUUID + "/conventions/skill", `{}`},
	} {
		if r := do(t, e, tc.method, tc.path, tc.body, "Origin", "http://evil.example"); r.status != http.StatusForbidden {
			t.Errorf("%s %s with a foreign Origin = %d, want 403", tc.method, tc.path, r.status)
		}
	}
	if len(f.calls) != 0 {
		t.Errorf("foreign-origin requests reached the use case: %v", f.calls)
	}
}

func TestBodyLimits(t *testing.T) {
	f := &fakeServices{}
	e := newEcho(f)
	// A body of 1 MiB + 1 is refused on an ordinary route ...
	big := `{"name":"x","type":"custom","body":"` + strings.Repeat("a", 1<<20) + `"}`
	if r := do(t, e, http.MethodPost, "/skills", big); r.status != http.StatusRequestEntityTooLarge {
		t.Errorf("POST /skills with a 1 MiB body = %d, want 413", r.status)
	}
	// ... while the import preview takes the base64 of a 2 MiB upload.
	upload := `{"kind":"file","filename":"a.md","content_base64":"` + strings.Repeat("A", 2*1024*1024) + `"}`
	if r := do(t, e, http.MethodPost, "/skills/import/preview", upload); r.status != http.StatusOK {
		t.Errorf("POST /skills/import/preview with a 2 MiB upload = %d %.100s, want 200", r.status, r.body)
	}
	if f.gotImport.Kind != app.KindFile || len(f.gotImport.ContentBase64) != 2*1024*1024 {
		t.Errorf("preview request = kind %q, %d base64 chars, want file and 2097152", f.gotImport.Kind, len(f.gotImport.ContentBase64))
	}
	over := `{"kind":"file","filename":"a.md","content_base64":"` + strings.Repeat("A", 2162688) + `"}`
	if r := do(t, e, http.MethodPost, "/skills/import/preview", over); r.status != http.StatusRequestEntityTooLarge {
		t.Errorf("POST /skills/import/preview over 2162688 bytes = %d, want 413", r.status)
	}
}

func TestParsedRequestsReachTheUseCases(t *testing.T) {
	f := &fakeServices{}
	e := newEcho(f)
	flag := func(b bool) *bool { return &b }
	str := func(s string) *string { return &s }

	if r := do(t, e, http.MethodPost, "/skills", `{"name":"x","type":"security","body":" b ","enabled":false,"source":"imported_url","source_ref":"https://h/x.md","extra":1}`); r.status != 201 {
		t.Fatalf("POST /skills = %d %s", r.status, r.body)
	}
	wantCreate := app.CreateInput{Name: "x", Type: domain.TypeSecurity, Body: " b ", Enabled: flag(false), Source: domain.SourceImportedURL, SourceRef: str("https://h/x.md")}
	if diff := cmp.Diff(wantCreate, f.gotCreate); diff != "" {
		t.Errorf("Create input mismatch (-want +got):\n%s", diff)
	}

	if r := do(t, e, http.MethodPut, "/skills/"+someUUID, `{"description":"","base_version":3,"enabled":true}`); r.status != 200 {
		t.Fatalf("PUT /skills/:id = %d %s", r.status, r.body)
	}
	wantPatch := domain.Patch{Description: str(""), Enabled: flag(true), BaseVersion: func() *int { n := 3; return &n }()}
	if diff := cmp.Diff(wantPatch, f.gotPatch); diff != "" {
		t.Errorf("Update patch mismatch (-want +got):\n%s", diff)
	}

	if r := do(t, e, http.MethodGet, "/skills/stats", ""); r.status != 200 || f.gotDays != 30 {
		t.Errorf("GET /skills/stats days = %d (status %d), want 30", f.gotDays, r.status)
	}
	if r := do(t, e, http.MethodGet, "/skills/"+someUUID+"/stats?days=1e2", ""); r.status != 200 || f.gotDays != 100 {
		t.Errorf("GET /skills/:id/stats?days=1e2 days = %d (status %d), want 100", f.gotDays, r.status)
	}
	if r := do(t, e, http.MethodGet, "/skills/community?q=owasp&tag=react", ""); r.status != 200 || f.gotFilter != (domain.CatalogFilter{Q: "owasp", Tag: "react"}) {
		t.Errorf("GET /skills/community filter = %+v (status %d), want q=owasp tag=react", f.gotFilter, r.status)
	}
	if r := do(t, e, http.MethodPost, "/skills/"+someUUID+"/versions/7/restore", ""); r.status != 200 || f.gotVer != 7 {
		t.Errorf("POST restore version = %d (status %d), want 7", f.gotVer, r.status)
	}

	f.merged = app.MergeResult{Skill: domain.Skill{ID: "s", Name: "house", UsedBy: 1}, LinkedAgents: []domain.AgentRef{{ID: "a", Name: "A", Enabled: true}}}
	body := `{"convention_ids":["` + someUUID + `"],"name":"house","body":"b"}`
	r := do(t, e, http.MethodPost, "/repos/"+someUUID+"/conventions/skill", body)
	if r.status != 201 {
		t.Fatalf("POST /repos/:id/conventions/skill = %d %s", r.status, r.body)
	}
	wantMerge := app.MergeInput{ConventionIDs: []string{someUUID}, Name: "house", Type: domain.TypeConvention, Body: "b", Enabled: true}
	if diff := cmp.Diff(wantMerge, f.gotMerge); diff != "" {
		t.Errorf("Merge input (defaults applied) mismatch (-want +got):\n%s", diff)
	}
	var got struct {
		Skill        map[string]any   `json:"skill"`
		LinkedAgents []map[string]any `json:"linked_agents"`
	}
	if err := json.Unmarshal([]byte(r.body), &got); err != nil || got.Skill["used_by"] != float64(1) || len(got.LinkedAgents) != 1 {
		t.Errorf("merge response = %s (%v), want skill.used_by 1 and one linked agent", r.body, err)
	}
}

func TestSkillResponseShape(t *testing.T) {
	ref := "https://h/x.md"
	f := &fakeServices{skill: domain.Skill{
		ID: "id", Name: "x", Description: "d", Type: domain.TypeRubric, Source: domain.SourceImportedURL, SourceRef: &ref,
		Body: "b", Version: 2, UsedBy: 3,
	}}
	r := do(t, newEcho(f), http.MethodGet, "/skills/"+someUUID, "")
	var got map[string]any
	if err := json.Unmarshal([]byte(r.body), &got); err != nil {
		t.Fatalf("body %q: %v", r.body, err)
	}
	want := map[string]any{
		"id": "id", "name": "x", "description": "d", "type": "rubric", "source": "imported_url", "source_ref": ref,
		"body": "b", "enabled": false, "version": float64(2), "evidence_files": nil,
		"created_at": "0001-01-01T00:00:00.000Z", "updated_at": "0001-01-01T00:00:00.000Z", "used_by": float64(3),
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("GET /skills/:id body mismatch (-want +got):\n%s", diff)
	}
}

func TestEmptyListsAreArraysNotNull(t *testing.T) {
	e := newEcho(&fakeServices{})
	for _, path := range []string{"/skills", "/skills/stats", "/skills/community", "/skills/" + someUUID + "/versions", "/skills/" + someUUID + "/agents"} {
		if r := do(t, e, http.MethodGet, path, ""); r.status != 200 || r.body != "[]" {
			t.Errorf("GET %s = %d %s, want 200 []", path, r.status, r.body)
		}
	}
}

func TestStatsResponseShape(t *testing.T) {
	r := do(t, newEcho(&fakeServices{}), http.MethodGet, "/skills/"+someUUID+"/stats?days=7", "")
	var got map[string]any
	if err := json.Unmarshal([]byte(r.body), &got); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"skill_id": "", "window_days": float64(0), "runs_attached": float64(0), "runs_cited": float64(0), "pull_rate": nil,
		"findings": float64(0), "accepted": float64(0), "dismissed": float64(0), "accept_rate": nil,
		"by_category": []any{}, "by_severity": []any{}, "used_by": []any{},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("stats body mismatch (-want +got):\n%s", diff)
	}
}

func TestUseCaseErrorsRenderAsEnvelope(t *testing.T) {
	f := &fakeServices{err: apperr.Conflict("The skill changed").WithCode("stale_version").WithDetails(map[string]int{"current_version": 3})}
	r := do(t, newEcho(f), http.MethodPut, "/skills/"+someUUID, `{"base_version":2}`)
	if r.status != 409 {
		t.Fatalf("status = %d %s, want 409", r.status, r.body)
	}
	code, message, _, details := errorOf(t, r)
	if code != "stale_version" || message != "The skill changed" || string(details) != `{"current_version":3}` {
		t.Errorf("error = %q %q %s, want stale_version with details", code, message, details)
	}
}

func TestHeadAnswersGet(t *testing.T) {
	r := do(t, newEcho(&fakeServices{}), http.MethodHead, "/skills", "")
	if r.status != http.StatusOK || r.body != "" {
		t.Errorf("HEAD /skills = %d %q, want 200 with no body", r.status, r.body)
	}
}

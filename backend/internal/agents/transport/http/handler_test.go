package httpapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/labstack/echo/v5"
	"gorm.io/gorm"

	agentsdb "dev-digest-go/backend/internal/agents/adapters/postgres"
	"dev-digest-go/backend/internal/agents/app"
	httpapi "dev-digest-go/backend/internal/agents/transport/http"
	"dev-digest-go/backend/internal/llm"
	"dev-digest-go/backend/internal/platform/httpx"
	pg "dev-digest-go/backend/internal/platform/postgres"
	"dev-digest-go/backend/internal/platform/postgres/pgtest"
	"dev-digest-go/backend/internal/tenancy"
	tenancypg "dev-digest-go/backend/internal/tenancy/adapters/postgres"
)

func TestMain(m *testing.M) { os.Exit(pgtest.Main(m)) }

const (
	uiOrigin      = "http://localhost:3100"
	foreignOrigin = "http://evil.example"
	someUUID      = "00000000-0000-4000-8000-000000000000"
)

// fakeModels is the model-listing port: a canned answer per provider, or an error.
type fakeModels struct {
	byProvider map[llm.Provider][]llm.ModelInfo
	err        error
}

func (m fakeModels) ListModels(_ context.Context, p llm.Provider) ([]llm.ModelInfo, error) {
	if m.err != nil {
		return nil, m.err
	}
	return m.byProvider[p], nil
}

type env struct {
	e      *echo.Echo
	db     *gorm.DB
	ws     string
	other  string
	models *fakeModels
}

func newEnv(t *testing.T) *env {
	t.Helper()
	d := pgtest.Migrated(t)
	v := &env{db: d.DB, models: &fakeModels{}}
	for _, q := range []struct {
		dst   *string
		query string
	}{
		{&v.ws, `INSERT INTO workspaces (name) VALUES ('default') RETURNING id`},
		{&v.other, `INSERT INTO workspaces (name) VALUES ('other') RETURNING id`},
	} {
		if err := d.DB.Raw(q.query).Scan(q.dst).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := d.DB.Exec(`INSERT INTO users (email, name) VALUES ('you@local', 'You')`).Error; err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.DiscardHandler)
	svc := app.NewService(agentsdb.NewStore(d.DB), pg.NewTx(d.DB), modelsProxy{v}, log)
	v.e = httpx.New(httpx.Options{Log: log, CORSOrigins: []string{uiOrigin}})
	httpapi.AddRoutes(v.e, svc, tenancy.NewResolver(tenancypg.NewStore(d.DB)))
	return v
}

// modelsProxy lets a test swap the canned model answers after the service was built.
type modelsProxy struct{ v *env }

func (p modelsProxy) ListModels(ctx context.Context, pr llm.Provider) ([]llm.ModelInfo, error) {
	return p.v.models.ListModels(ctx, pr)
}

type response struct {
	status int
	body   string
}

func (v *env) do(t *testing.T, method, path, body string, headers ...string) response {
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
	v.e.ServeHTTP(rec, req)
	return response{rec.Code, strings.TrimSpace(rec.Body.String())}
}

func decode[T any](t *testing.T, r response) T {
	t.Helper()
	var out T
	if err := json.Unmarshal([]byte(r.body), &out); err != nil {
		t.Fatalf("response body %q is not the expected JSON: %v", r.body, err)
	}
	return out
}

// wantError checks the status and the envelope code and message.
func wantError(t *testing.T, r response, status int, code, message string) httpx.ErrorDetail {
	t.Helper()
	got := decode[httpx.ErrorBody](t, r).Error
	if r.status != status || got.Code != code || got.Message != message {
		t.Errorf("response = %d {%s %q}, want %d {%s %q} (body %s)", r.status, got.Code, got.Message, status, code, message, r.body)
	}
	return got
}

// issues returns [instancePath: message] of a 422 body.
func issues(t *testing.T, r response) []string {
	t.Helper()
	if r.status != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d (%s), want 422", r.status, r.body)
	}
	var out []string
	details, _ := decode[httpx.ErrorBody](t, r).Error.Details.([]any)
	for _, d := range details {
		m := d.(map[string]any)
		out = append(out, m["instancePath"].(string)+": "+m["message"].(string))
	}
	return out
}

func (v *env) createAgent(t *testing.T, body string) map[string]any {
	t.Helper()
	r := v.do(t, http.MethodPost, "/agents", body)
	if r.status != http.StatusCreated {
		t.Fatalf("POST /agents %s = %d (%s), want 201", body, r.status, r.body)
	}
	return decode[map[string]any](t, r)
}

const minimal = `{"name":"A","provider":"openai","model":"gpt-4.1","system_prompt":"p"}`

func (v *env) skill(t *testing.T, ws, name string) string {
	t.Helper()
	var id string
	err := v.db.Raw(`INSERT INTO skills (workspace_id, name, description, type, source, body)
		VALUES (?, ?, 'd', 'custom', 'manual', 'b') RETURNING id`, ws, name).Scan(&id).Error
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestList_EmptyAndOrder(t *testing.T) {
	v := newEnv(t)
	if r := v.do(t, http.MethodGet, "/agents", ""); r.status != http.StatusOK || r.body != "[]" {
		t.Errorf("GET /agents (none) = %d %s, want 200 []", r.status, r.body)
	}
	v.createAgent(t, `{"name":"b","provider":"openai","model":"m","system_prompt":"p"}`)
	v.createAgent(t, `{"name":"a","provider":"openai","model":"m","system_prompt":"p"}`)
	var names []string
	for _, a := range decode[[]map[string]any](t, v.do(t, http.MethodGet, "/agents", "")) {
		names = append(names, a["name"].(string))
	}
	if diff := cmp.Diff([]string{"b", "a"}, names); diff != "" {
		t.Errorf("GET /agents order mismatch (-want +got):\n%s", diff)
	}
}

func TestCreate_DefaultsAndTwin(t *testing.T) {
	v := newEnv(t)
	got := v.createAgent(t, minimal)
	want := map[string]any{
		"id": got["id"], "name": "A", "description": "", "provider": "openai", "model": "gpt-4.1",
		"system_prompt": "p", "output_schema": nil, "enabled": true, "version": float64(1),
		"strategy": "single-pass", "ci_fail_on": "critical", "repo_intel": true,
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("POST /agents body mismatch (-want +got):\n%s", diff)
	}
	v.createAgent(t, minimal) // names are not unique

	versions := decode[[]map[string]any](t, v.do(t, http.MethodGet, "/agents/"+got["id"].(string)+"/versions", ""))
	if len(versions) != 1 {
		t.Fatalf("versions = %v, want one", versions)
	}
	wantVersion := map[string]any{
		"agent_id": got["id"], "version": float64(1), "created_at": versions[0]["created_at"],
		"config": map[string]any{
			"provider": "openai", "model": "gpt-4.1", "system_prompt": "p", "output_schema": nil,
			"strategy": "single-pass", "ci_fail_on": "critical", "repo_intel": true, "skills": []any{},
		},
	}
	if diff := cmp.Diff(wantVersion, versions[0]); diff != "" {
		t.Errorf("version 1 mismatch (-want +got):\n%s", diff)
	}
	if ts, _ := versions[0]["created_at"].(string); !strings.HasSuffix(ts, "Z") || len(ts) != len("2026-10-22T10:00:00.000Z") {
		t.Errorf("created_at = %q, want ISO-8601 UTC with milliseconds", ts)
	}
}

func TestCreate_Explicit(t *testing.T) {
	v := newEnv(t)
	got := v.createAgent(t, `{"name":"B","description":"d","provider":"anthropic","model":"m","system_prompt":"p",
		"output_schema":{"type":"object"},"strategy":"auto","ci_fail_on":"any","repo_intel":false,"enabled":false,"extra":1}`)
	if got["enabled"] != false || got["repo_intel"] != false || got["strategy"] != "auto" || got["ci_fail_on"] != "any" ||
		got["description"] != "d" || got["provider"] != "anthropic" {
		t.Errorf("POST /agents body = %v, want the sent values", got)
	}
	if diff := cmp.Diff(map[string]any{"type": "object"}, got["output_schema"]); diff != "" {
		t.Errorf("output_schema mismatch (-want +got):\n%s", diff)
	}
	if _, ok := got["extra"]; ok {
		t.Error("response carries the unknown key \"extra\"")
	}
}

func TestCreate_Validation(t *testing.T) {
	tests := []struct {
		name string
		body string
		want []string
	}{
		{"empty object", `{}`, []string{
			"/name: Required", "/provider: Required", "/model: Required", "/system_prompt: Required"}},
		{"empty strings", `{"name":"","provider":"openai","model":"","system_prompt":""}`, []string{
			"/name: String must contain at least 1 character(s)", "/model: String must contain at least 1 character(s)",
			"/system_prompt: String must contain at least 1 character(s)"}},
		{"bad provider", `{"name":"A","provider":"gemini","model":"m","system_prompt":"p"}`, []string{
			"/provider: Invalid enum value. Expected 'openai' | 'anthropic' | 'openrouter', received 'gemini'"}},
		{"wrong types", `{"name":1,"description":null,"provider":"openai","model":"m","system_prompt":"p","enabled":"yes","repo_intel":1}`, []string{
			"/name: Expected string, received number", "/description: Expected string, received null",
			"/repo_intel: Expected boolean, received number", "/enabled: Expected boolean, received string"}},
		{"bad strategy and gate", `{"name":"A","provider":"openai","model":"m","system_prompt":"p","strategy":"x","ci_fail_on":"y"}`, []string{
			"/strategy: Invalid enum value. Expected 'single-pass' | 'map-reduce' | 'auto', received 'x'",
			"/ci_fail_on: Invalid enum value. Expected 'never' | 'critical' | 'warning' | 'any', received 'y'"}},
		{"array body", `[]`, []string{"/: Expected object, received array"}},
		{"null body", `null`, []string{"/: Expected object, received null"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			v := newEnv(t)
			if diff := cmp.Diff(tc.want, issues(t, v.do(t, http.MethodPost, "/agents", tc.body))); diff != "" {
				t.Errorf("POST /agents %s issues mismatch (-want +got):\n%s", tc.body, diff)
			}
			if n := len(decode[[]any](t, v.do(t, http.MethodGet, "/agents", ""))); n != 0 {
				t.Errorf("agents after rejected create = %d, want 0", n)
			}
		})
	}
}

func TestCreate_NoBodyIsRequired(t *testing.T) {
	v := newEnv(t)
	if diff := cmp.Diff([]string{"/: Required"}, issues(t, v.do(t, http.MethodPost, "/agents", ""))); diff != "" {
		t.Errorf("POST /agents (no body) issues mismatch (-want +got):\n%s", diff)
	}
}

func TestCreate_Guards(t *testing.T) {
	v := newEnv(t)
	plain := v.do(t, http.MethodPost, "/agents", minimal, "Content-Type", "text/plain")
	if diff := cmp.Diff([]string{"/: Expected object, received string"}, issues(t, plain)); diff != "" {
		t.Errorf("text/plain POST /agents issues mismatch (-want +got):\n%s", diff)
	}
	foreign := v.do(t, http.MethodPost, "/agents", minimal, "Origin", foreignOrigin)
	if code := decode[httpx.ErrorBody](t, foreign).Error.Code; foreign.status != http.StatusForbidden || code != "forbidden" {
		t.Errorf("POST /agents from a foreign origin = %d {%s}, want 403 forbidden", foreign.status, code)
	}
	if r := v.do(t, http.MethodPost, "/agents", minimal, "Origin", uiOrigin); r.status != http.StatusCreated {
		t.Errorf("POST /agents from the UI origin = %d (%s), want 201", r.status, r.body)
	}
	if n := len(decode[[]any](t, v.do(t, http.MethodGet, "/agents", ""))); n != 1 {
		t.Errorf("agents after the guarded requests = %d, want 1", n)
	}
}

func TestUpdate_Versioning(t *testing.T) {
	v := newEnv(t)
	a := v.createAgent(t, minimal)
	path := "/agents/" + a["id"].(string)
	put := func(body string) map[string]any {
		t.Helper()
		r := v.do(t, http.MethodPut, path, body)
		if r.status != http.StatusOK {
			t.Fatalf("PUT %s = %d (%s), want 200", body, r.status, r.body)
		}
		return decode[map[string]any](t, r)
	}
	for _, st := range []struct {
		body string
		want float64
	}{
		{`{"model":"gpt-5.4"}`, 2}, {`{"enabled":false}`, 2}, {`{}`, 2}, {`{"model":"gpt-5.4"}`, 2}, {`{"output_schema":null}`, 3},
	} {
		if got := put(st.body); got["version"] != st.want {
			t.Errorf("PUT %s version = %v, want %v", st.body, got["version"], st.want)
		}
	}
	got := put(`{"name":"Renamed","strategy":"auto"}`)
	if got["name"] != "Renamed" || got["strategy"] != "auto" || got["enabled"] != false || got["model"] != "gpt-5.4" || got["version"] != float64(4) {
		t.Errorf("PUT body = %v, want the merged agent at v4", got)
	}
	versions := decode[[]map[string]any](t, v.do(t, http.MethodGet, path+"/versions", ""))
	var nums []float64
	for _, ver := range versions {
		nums = append(nums, ver["version"].(float64))
	}
	if diff := cmp.Diff([]float64{4, 3, 2, 1}, nums); diff != "" {
		t.Errorf("versions mismatch (-want +got):\n%s", diff)
	}
	one := v.do(t, http.MethodGet, path+"/versions/2", "")
	if c := decode[map[string]any](t, one)["config"].(map[string]any); one.status != 200 || c["model"] != "gpt-5.4" {
		t.Errorf("GET versions/2 = %d %s, want the model gpt-5.4", one.status, one.body)
	}
}

func TestUpdate_Errors(t *testing.T) {
	v := newEnv(t)
	a := v.createAgent(t, minimal)
	path := "/agents/" + a["id"].(string)

	for _, body := range []string{`{"name":""}`, `{"name":null}`, `{"provider":null}`, `{"enabled":null}`} {
		if r := v.do(t, http.MethodPut, path, body); r.status != http.StatusUnprocessableEntity {
			t.Errorf("PUT %s = %d (%s), want 422", body, r.status, r.body)
		}
	}
	got := issues(t, v.do(t, http.MethodPut, path, `{"name":null}`))
	if diff := cmp.Diff([]string{"/name: Expected string, received null"}, got); diff != "" {
		t.Errorf("PUT name null issues mismatch (-want +got):\n%s", diff)
	}
	wantError(t, v.do(t, http.MethodPut, "/agents/"+someUUID, `{}`), 404, "not_found", "Agent not found")
	// Validation runs before the lookup.
	if r := v.do(t, http.MethodPut, "/agents/"+someUUID, `{"name":""}`); r.status != 422 {
		t.Errorf("PUT unknown agent with a bad body = %d, want 422", r.status)
	}
	if r := v.do(t, http.MethodPut, "/agents/abc", `{}`); r.status != 422 {
		t.Errorf("PUT /agents/abc = %d, want 422", r.status)
	}
}

func TestUpdate_BodyLimit(t *testing.T) {
	v := newEnv(t)
	a := v.createAgent(t, minimal)
	path := "/agents/" + a["id"].(string)
	body := func(n int) string {
		prefix, suffix := `{"system_prompt":"`, `"}`
		return prefix + strings.Repeat("x", n-len(prefix)-len(suffix)) + suffix
	}
	if r := v.do(t, http.MethodPut, path, body(1<<20)); r.status != http.StatusOK {
		t.Errorf("PUT of 1,048,576 bytes = %d, want 200", r.status)
	}
	wantError(t, v.do(t, http.MethodPut, path, body(1<<20+1)), 413, "internal_error", "Request body is too large")
}

func TestDelete(t *testing.T) {
	v := newEnv(t)
	a := v.createAgent(t, minimal)
	path := "/agents/" + a["id"].(string)
	if r := v.do(t, http.MethodDelete, path, ""); r.status != 200 || r.body != `{"ok":true}` {
		t.Errorf("DELETE = %d %s, want 200 {\"ok\":true}", r.status, r.body)
	}
	wantError(t, v.do(t, http.MethodGet, path, ""), 404, "not_found", "Agent not found")
	wantError(t, v.do(t, http.MethodDelete, path, ""), 404, "not_found", "Agent not found")
	if r := v.do(t, http.MethodDelete, "/agents/abc", ""); r.status != 422 {
		t.Errorf("DELETE /agents/abc = %d, want 422", r.status)
	}
	foreign := v.do(t, http.MethodDelete, "/agents/"+someUUID, "", "Origin", foreignOrigin)
	if foreign.status != http.StatusForbidden {
		t.Errorf("DELETE from a foreign origin = %d, want 403", foreign.status)
	}
}

func TestPathValidation(t *testing.T) {
	v := newEnv(t)
	for _, path := range []string{
		"/agents/abc",
		"/agents/abc/versions",
		"/agents/abc/skills",
		"/agents/abc/models",
		"/agents/performance",
		"/agents/" + someUUID + "/versions/0",
		"/agents/" + someUUID + "/versions/1.5",
		"/agents/" + someUUID + "/versions/abc",
		"/agents/" + someUUID + "/versions/-1",
		"/agents/" + someUUID + "/versions/2147483648",
		"/providers/foo/models",
		"/providers/mock/models",
	} {
		if r := v.do(t, http.MethodGet, path, ""); r.status != http.StatusUnprocessableEntity {
			t.Errorf("GET %s = %d (%s), want 422", path, r.status, r.body)
		}
	}
	got := issues(t, v.do(t, http.MethodGet, "/agents/performance?period=30d", ""))
	if diff := cmp.Diff([]string{"/id: Invalid uuid"}, got); diff != "" {
		t.Errorf("GET /agents/performance issues mismatch (-want +got):\n%s", diff)
	}
	wantError(t, v.do(t, http.MethodGet, "/agents/"+someUUID+"/versions/2147483647", ""), 404, "not_found", "Agent version not found")
}

func TestVersions_NotFound(t *testing.T) {
	v := newEnv(t)
	a := v.createAgent(t, minimal)
	id := a["id"].(string)
	wantError(t, v.do(t, http.MethodGet, "/agents/"+someUUID+"/versions", ""), 404, "not_found", "Agent not found")
	wantError(t, v.do(t, http.MethodGet, "/agents/"+id+"/versions/9", ""), 404, "not_found", "Agent version not found")
	wantError(t, v.do(t, http.MethodGet, "/agents/"+someUUID+"/versions/1", ""), 404, "not_found", "Agent version not found")
}

func TestOtherWorkspaceIsNotFound(t *testing.T) {
	v := newEnv(t)
	var foreign string
	if err := v.db.Raw(`INSERT INTO agents (workspace_id, name, provider, model, system_prompt)
		VALUES (?, 'x', 'openai', 'm', 'p') RETURNING id`, v.other).Scan(&foreign).Error; err != nil {
		t.Fatal(err)
	}
	for _, req := range []struct{ method, path, body string }{
		{http.MethodGet, "/agents/" + foreign, ""},
		{http.MethodPut, "/agents/" + foreign, `{}`},
		{http.MethodDelete, "/agents/" + foreign, ""},
		{http.MethodGet, "/agents/" + foreign + "/versions", ""},
		{http.MethodGet, "/agents/" + foreign + "/skills", ""},
		{http.MethodPost, "/agents/" + foreign + "/skills", `{"skill_ids":[]}`},
		{http.MethodGet, "/agents/" + foreign + "/models", ""},
	} {
		wantError(t, v.do(t, req.method, req.path, req.body), 404, "not_found", "Agent not found")
	}
	if r := v.do(t, http.MethodGet, "/agents", ""); r.body != "[]" {
		t.Errorf("GET /agents = %s, want [] (the other workspace's agent is hidden)", r.body)
	}
}

func TestSkills_SetAndLink(t *testing.T) {
	v := newEnv(t)
	a := v.createAgent(t, minimal)
	id := a["id"].(string)
	path := "/agents/" + id + "/skills"
	sa, sb, sc := v.skill(t, v.ws, "A"), v.skill(t, v.ws, "B"), v.skill(t, v.ws, "C")
	version := func() float64 {
		return decode[map[string]any](t, v.do(t, http.MethodGet, "/agents/"+id, ""))["version"].(float64)
	}
	post := func(body string) []map[string]any {
		t.Helper()
		r := v.do(t, http.MethodPost, path, body)
		if r.status != 200 {
			t.Fatalf("POST skills %s = %d (%s), want 200", body, r.status, r.body)
		}
		return decode[[]map[string]any](t, r)
	}
	ids := func(links []map[string]any) []string {
		out := []string{}
		for i, l := range links {
			if l["agent_id"] != id || l["order"] != float64(i) {
				t.Errorf("link %d = %v, want agent %s order %d", i, l, id, i)
			}
			out = append(out, l["skill_id"].(string))
		}
		return out
	}

	post(`{"skill_ids":["` + sa + `","` + sb + `"]}`)
	if got := ids(post(`{"skill_ids":["` + sb + `","` + sa + `"]}`)); version() != 3 || got[0] != sb || got[1] != sa {
		t.Errorf("after reorder: links %v, version %v, want [B A] and 3", got, version())
	}
	post(`{"skill_ids":["` + sb + `","` + sa + `"]}`)
	if version() != 3 {
		t.Errorf("version after the same list = %v, want 3", version())
	}
	if diff := cmp.Diff([]string{sc, sb, sa}, ids(post(`{"skill_id":"`+sc+`","order":0}`))); diff != "" {
		t.Errorf("link C at 0 mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff([]string{sc, sb, sa}, ids(post(`{"skill_id":"`+sa+`","skill_ids_ignored":1}`))); diff != "" || version() != 4 {
		t.Errorf("relink A mismatch (-want +got):\n%s\nversion %v, want 4", diff, version())
	}
	// skill_id and order are ignored when skill_ids is present.
	if diff := cmp.Diff([]string{sa}, ids(post(`{"skill_ids":["`+sa+`"],"skill_id":"`+sc+`","order":0}`))); diff != "" {
		t.Errorf("set mode mismatch (-want +got):\n%s", diff)
	}
	if got := ids(post(`{"skill_ids":[]}`)); len(got) != 0 || version() != 6 {
		t.Errorf("clear: links %v, version %v, want [] and 6", got, version())
	}
	if r := v.do(t, http.MethodGet, path, ""); r.body != "[]" || r.status != 200 {
		t.Errorf("GET skills (cleared) = %d %s, want 200 []", r.status, r.body)
	}
	post(`{"skill_ids":["` + sb + `","` + sa + `"]}`)
	got := decode[[]map[string]any](t, v.do(t, http.MethodGet, path, ""))
	if diff := cmp.Diff([]string{sb, sa}, ids(got)); diff != "" {
		t.Errorf("GET skills mismatch (-want +got):\n%s", diff)
	}
}

func TestSkills_Errors(t *testing.T) {
	v := newEnv(t)
	a := v.createAgent(t, minimal)
	id := a["id"].(string)
	path := "/agents/" + id + "/skills"
	sa := v.skill(t, v.ws, "A")
	foreign := v.skill(t, v.other, "S")
	random := "00000000-0000-4000-8000-000000000001"
	v.do(t, http.MethodPost, path, `{"skill_ids":["`+sa+`"]}`)

	r := v.do(t, http.MethodPost, path, `{"skill_ids":["`+sa+`","`+foreign+`","`+random+`"]}`)
	got := wantError(t, r, 422, "unknown_skill", "Unknown skill id(s) for this workspace: "+foreign+", "+random)
	if diff := cmp.Diff(map[string]any{"skill_ids": []any{foreign, random}}, got.Details); diff != "" {
		t.Errorf("unknown_skill details mismatch (-want +got):\n%s", diff)
	}
	if links := decode[[]any](t, v.do(t, http.MethodGet, path, "")); len(links) != 1 {
		t.Errorf("links after the rejected request = %v, want the original one", links)
	}
	if ver := decode[map[string]any](t, v.do(t, http.MethodGet, "/agents/"+id, ""))["version"]; ver != float64(2) {
		t.Errorf("version after the rejected request = %v, want 2", ver)
	}
	wantError(t, v.do(t, http.MethodPost, path, `{"skill_id":"`+random+`"}`), 422, "unknown_skill", "Unknown skill id(s) for this workspace: "+random)

	tests := []struct {
		name string
		body string
		want []string
	}{
		{"empty object", `{}`, []string{"/: Provide skill_ids (set/reorder) or skill_id (link one)"}},
		{"duplicates", `{"skill_ids":["` + sa + `","` + sa + `"]}`, []string{"/: skill_ids must not contain duplicates"}},
		{"bad uuid", `{"skill_ids":["x"]}`, []string{"/skill_ids/0: Invalid uuid"}},
		{"not array", `{"skill_ids":"x"}`, []string{"/skill_ids: Expected array, received string"}},
		{"item type", `{"skill_ids":[1]}`, []string{"/skill_ids/0: Expected string, received number"}},
		{"bad skill_id", `{"skill_id":"x"}`, []string{"/skill_id: Invalid uuid"}},
		{"negative order", `{"skill_id":"` + sa + `","order":-1}`, []string{"/order: Number must be greater than or equal to 0"}},
		{"float order", `{"skill_id":"` + sa + `","order":1.5}`, []string{"/order: Expected integer, received float"}},
		{"string order", `{"skill_id":"` + sa + `","order":"1"}`, []string{"/order: Expected number, received string"}},
		{"null skill_ids", `{"skill_ids":null}`, []string{"/skill_ids: Expected array, received null"}},
		{"string body", `"x"`, []string{"/: Expected object, received string"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if diff := cmp.Diff(tc.want, issues(t, v.do(t, http.MethodPost, path, tc.body))); diff != "" {
				t.Errorf("POST skills %s issues mismatch (-want +got):\n%s", tc.body, diff)
			}
		})
	}
	wantError(t, v.do(t, http.MethodPost, "/agents/"+someUUID+"/skills", `{"skill_ids":["`+random+`"]}`), 404, "not_found", "Agent not found")
}

func TestModels(t *testing.T) {
	v := newEnv(t)
	created := int64(1700000000)
	label := "Claude X"
	v.models.byProvider = map[llm.Provider][]llm.ModelInfo{
		llm.OpenAI:    {{ID: "gpt-4.1", Provider: llm.OpenAI, Created: &created}},
		llm.Anthropic: {{ID: "claude-x", Provider: llm.Anthropic, Label: &label}},
	}
	anth := v.createAgent(t, `{"name":"A","provider":"anthropic","model":"m","system_prompt":"p"}`)

	r := v.do(t, http.MethodGet, "/agents/"+anth["id"].(string)+"/models", "")
	want := []any{map[string]any{"id": "claude-x", "provider": "anthropic", "label": "Claude X"}}
	if diff := cmp.Diff(want, decode[[]any](t, r)); r.status != 200 || diff != "" {
		t.Errorf("GET /agents/:id/models = %d, mismatch (-want +got):\n%s", r.status, diff)
	}
	r = v.do(t, http.MethodGet, "/providers/openai/models", "")
	want = []any{map[string]any{"id": "gpt-4.1", "provider": "openai", "created": float64(created)}}
	if diff := cmp.Diff(want, decode[[]any](t, r)); r.status != 200 || diff != "" {
		t.Errorf("GET /providers/openai/models = %d, mismatch (-want +got):\n%s", r.status, diff)
	}
	if r := v.do(t, http.MethodGet, "/providers/openrouter/models", ""); r.status != 200 || r.body != "[]" {
		t.Errorf("GET /providers/openrouter/models (no models) = %d %s, want 200 []", r.status, r.body)
	}

	v.models.err = errors.New("OpenAI model list failed: 500")
	for _, path := range []string{"/providers/openai/models", "/agents/" + anth["id"].(string) + "/models"} {
		if r := v.do(t, http.MethodGet, path, ""); r.status != 200 || r.body != "[]" {
			t.Errorf("GET %s (provider error) = %d %s, want 200 []", path, r.status, r.body)
		}
	}
	wantError(t, v.do(t, http.MethodGet, "/agents/"+someUUID+"/models", ""), 404, "not_found", "Agent not found")
}

func TestRoutesAreRegistered(t *testing.T) {
	v := newEnv(t)
	want := []string{
		"GET /agents", "POST /agents", "GET /agents/:id", "PUT /agents/:id", "DELETE /agents/:id",
		"GET /agents/:id/versions", "GET /agents/:id/versions/:version", "GET /agents/:id/skills",
		"POST /agents/:id/skills", "GET /agents/:id/models", "GET /providers/:id/models",
	}
	var got []string
	for _, r := range v.e.Router().Routes() {
		if r.Method != "echo_route_not_found" {
			got = append(got, r.Method+" "+r.Path)
		}
	}
	for _, w := range want {
		found := false
		for _, g := range got {
			found = found || g == w
		}
		if !found {
			t.Errorf("route %q is not registered (have %v)", w, got)
		}
	}
	if len(got) != len(want) {
		t.Errorf("registered routes = %d, want %d: %v", len(got), len(want), got)
	}
}

package pluginshttp_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/labstack/echo/v5"

	"dev-digest-go/backend/internal/platform/apperr"
	"dev-digest-go/backend/internal/platform/httpx"
	"dev-digest-go/backend/internal/plugins/app"
	"dev-digest-go/backend/internal/plugins/domain"
	pluginshttp "dev-digest-go/backend/internal/plugins/transport/http"
	"dev-digest-go/backend/internal/tenancy"
)

const (
	workspaceID = "11111111-2222-4333-8444-555555555555"
	userID      = "99999999-2222-4333-8444-555555555555"
	pluginID    = "7e1d0000-0000-4000-8000-000000000001"
	agentID     = "7e1d0000-0000-4000-8000-0000000000a1"
	skillID     = "7e1d0000-0000-4000-8000-0000000000b1"
	hostName    = "example.com"
	uiOrigin    = "http://localhost:3100"
)

var installedAt = time.Date(2026, 10, 26, 12, 34, 56, 789_000_000, time.UTC)

// fake is the plugins use cases: it answers with the configured values, records the last call
// and counts the calls.
type fake struct {
	mu    sync.Mutex
	calls int
	call  string
	args  []any
	err   error

	listing   app.Listing
	exported  app.ExportResult
	preview   domain.PreviewDoc
	installed domain.Installed
	plugin    domain.Plugin
	removed   domain.Removed
}

var _ app.API = (*fake)(nil)

func (f *fake) rec(name string, args ...any) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.call, f.args = name, args
	return f.err
}

func (f *fake) List(_ context.Context, ws string) (app.Listing, error) {
	return f.listing, f.rec("List", ws)
}

func (f *fake) Export(_ context.Context, ws, user string, in app.ExportInput) (app.ExportResult, error) {
	return f.exported, f.rec("Export", ws, user, in)
}

func (f *fake) Preview(_ context.Context, ws string, in app.ImportInput) (domain.PreviewDoc, error) {
	return f.preview, f.rec("Preview", ws, in)
}

func (f *fake) Install(_ context.Context, ws string, in app.ImportInput) (domain.Installed, error) {
	return f.installed, f.rec("Install", ws, in)
}

func (f *fake) SetEnabled(_ context.Context, ws, id string, enabled bool) (domain.Plugin, error) {
	return f.plugin, f.rec("SetEnabled", ws, id, enabled)
}

func (f *fake) Remove(_ context.Context, ws, id, confirm string) (domain.Removed, error) {
	return f.removed, f.rec("Remove", ws, id, confirm)
}

type fakeTenants struct{ err error }

func (t fakeTenants) Resolve(context.Context) (tenancy.Identity, error) {
	if t.err != nil {
		return tenancy.Identity{}, t.err
	}
	return tenancy.Identity{WorkspaceID: workspaceID, UserID: userID}, nil
}

// newEcho builds the real httpx stack (guards, body rules, error envelope) with the plugin routes
// and the raw-body entry of the import route.
func newEcho(t *testing.T, f *fake, rl httpx.RateLimit, ids fakeTenants) *echo.Echo {
	t.Helper()
	e := httpx.New(httpx.Options{
		Log:          slog.New(slog.DiscardHandler),
		CORSOrigins:  []string{uiOrigin},
		AllowedHosts: []string{hostName},
		RawBodies:    pluginshttp.RawBodies(),
		RateLimit:    rl,
	})
	pluginshttp.AddRoutes(e, f, ids)
	return e
}

type request struct {
	method      string
	path        string
	contentType string
	body        []byte
	headers     map[string]string
}

func send(e *echo.Echo, r request) *httptest.ResponseRecorder {
	var body *bytes.Reader
	if r.body != nil {
		body = bytes.NewReader(r.body)
	} else {
		body = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(r.method, r.path, body)
	req.Host = hostName
	if r.contentType != "" {
		req.Header.Set("Content-Type", r.contentType)
	}
	for k, v := range r.headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	return rec
}

func do(t *testing.T, f *fake, r request) *httptest.ResponseRecorder {
	t.Helper()
	return send(newEcho(t, f, httpx.RateLimit{}, fakeTenants{}), r)
}

func jsonReq(method, path, body string) request {
	return request{method: method, path: path, contentType: "application/json", body: []byte(body)}
}

func upload(path, contentType string, body []byte) request {
	return request{method: http.MethodPost, path: path, contentType: contentType, body: body}
}

// errBody decodes the error envelope.
func errBody(t *testing.T, rec *httptest.ResponseRecorder) httpx.ErrorDetail {
	t.Helper()
	var env struct {
		Error httpx.ErrorDetail `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("response is not an error envelope: %v\n%s", err, rec.Body.String())
	}
	return env.Error
}

func ptr[T any](v T) *T { return &v }

func samplePlugin() domain.Plugin {
	return domain.Plugin{
		ID: pluginID, Name: "acme", Version: ptr("1.2.0"), Source: ptr("acme.zip"), InstalledAt: installedAt,
		Enabled: false, Description: ptr("d"),
		Counts: domain.PluginCounts{Agents: 1, Skills: 2, EvalCases: 3, Learnings: 4},
	}
}

const samplePluginJSON = `{"id":"` + pluginID + `","name":"acme","version":"1.2.0","source":"acme.zip",` +
	`"installed_at":"2026-10-26T12:34:56.789Z","enabled":false,"description":"d",` +
	`"counts":{"agents":1,"skills":2,"eval_cases":3,"learnings":4}}`

func wantBody(t *testing.T, rec *httptest.ResponseRecorder, want string) {
	t.Helper()
	if got := strings.TrimSpace(rec.Body.String()); got != want {
		t.Errorf("body mismatch:\n got  %s\n want %s", got, want)
	}
}

func wantStatus(t *testing.T, rec *httptest.ResponseRecorder, want int) {
	t.Helper()
	if rec.Code != want {
		t.Fatalf("status = %d, want %d; body %s", rec.Code, want, rec.Body.String())
	}
}

// ---- GET /plugins ----------------------------------------------------------------------------

func TestList_WireShapeAndKeyOrder(t *testing.T) {
	f := &fake{listing: app.Listing{
		Plugins: []domain.Plugin{samplePlugin()},
		LastExport: &domain.LastExport{
			FileName: "x.zip", ExportedAt: "2026-10-25T10:00:00.000Z",
			Counts: domain.LastExportCounts{Agents: 1, Skills: 2, EvalCases: 3, Learnings: 0},
		},
	}}
	rec := do(t, f, request{method: http.MethodGet, path: "/plugins"})
	wantStatus(t, rec, http.StatusOK)
	wantBody(t, rec, `{"plugins":[`+samplePluginJSON+`],"last_export":{"file_name":"x.zip",`+
		`"exported_at":"2026-10-25T10:00:00.000Z","counts":{"agents":1,"skills":2,"eval_cases":3,"learnings":0}}}`)
	if f.call != "List" || f.args[0] != workspaceID {
		t.Errorf("call = %s %v, want List for the workspace", f.call, f.args)
	}
}

func TestList_EmptyHasEmptyArrayAndNullLastExport(t *testing.T) {
	rec := do(t, &fake{}, request{method: http.MethodGet, path: "/plugins"})
	wantStatus(t, rec, http.StatusOK)
	wantBody(t, rec, `{"plugins":[],"last_export":null}`)
}

func TestList_NullableRowFields(t *testing.T) {
	p := domain.Plugin{ID: pluginID, Name: "n", InstalledAt: installedAt}
	rec := do(t, &fake{listing: app.Listing{Plugins: []domain.Plugin{p}}}, request{method: http.MethodGet, path: "/plugins"})
	wantStatus(t, rec, http.StatusOK)
	wantBody(t, rec, `{"plugins":[{"id":"`+pluginID+`","name":"n","version":null,"source":null,`+
		`"installed_at":"2026-10-26T12:34:56.789Z","enabled":false,"description":null,`+
		`"counts":{"agents":0,"skills":0,"eval_cases":0,"learnings":0}}],"last_export":null}`)
}

// ---- POST /plugins/export --------------------------------------------------------------------

func TestExport_DownloadHeadersAndBody(t *testing.T) {
	zipBytes := []byte("PK\x03\x04zip")
	f := &fake{exported: app.ExportResult{FileName: "devdigest-acme-2026-10-26.zip", Archive: zipBytes}}
	rec := do(t, f, jsonReq(http.MethodPost, "/plugins/export", `{}`))
	wantStatus(t, rec, http.StatusOK)
	for header, want := range map[string]string{
		"Content-Type":                  "application/zip",
		"Content-Disposition":           `attachment; filename="devdigest-acme-2026-10-26.zip"`,
		"Access-Control-Expose-Headers": "Content-Disposition",
		"Cache-Control":                 "no-store",
	} {
		if got := rec.Header().Get(header); got != want {
			t.Errorf("%s = %q, want %q", header, got, want)
		}
	}
	if !bytes.Equal(rec.Body.Bytes(), zipBytes) {
		t.Errorf("body = %q, want the archive bytes", rec.Body.Bytes())
	}
}

func TestExport_ContentDispositionCannotBeInjected(t *testing.T) {
	f := &fake{exported: app.ExportResult{FileName: "a\"b\r\nX-Evil: 1/../c.zip", Archive: []byte("z")}}
	rec := do(t, f, jsonReq(http.MethodPost, "/plugins/export", `{}`))
	wantStatus(t, rec, http.StatusOK)
	if got, want := rec.Header().Get("Content-Disposition"), `attachment; filename="a_b__X-Evil__1_.._c.zip"`; got != want {
		t.Errorf("Content-Disposition = %q, want %q", got, want)
	}
	if rec.Header().Get("X-Evil") != "" {
		t.Error("a header was injected through the file name")
	}
}

func TestExport_InputAndDefaults(t *testing.T) {
	tests := []struct {
		name string
		body string
		want app.ExportInput
	}{
		{"empty object takes the defaults", `{}`, app.ExportInput{EvalCases: true, Learnings: false}},
		{
			"every key",
			`{"name":"my-plugin","description":"about","agent_ids":["` + agentID + `"],"skill_ids":["` + skillID + `"],` +
				`"include_eval_cases":false,"include_learnings":true}`,
			app.ExportInput{
				Name: "my-plugin", Description: "about", AgentIDs: []string{agentID}, SkillIDs: []string{skillID},
				EvalCases: false, Learnings: true,
			},
		},
		{
			"ids are lower-cased and de-duplicated in order",
			`{"agent_ids":["7E1D0000-0000-4000-8000-0000000000A1","` + agentID + `","7e1d0000-0000-4000-8000-0000000000a2"]}`,
			app.ExportInput{
				AgentIDs:  []string{agentID, "7e1d0000-0000-4000-8000-0000000000a2"},
				EvalCases: true,
			},
		},
		{"flags only", `{"include_learnings":true}`, app.ExportInput{EvalCases: true, Learnings: true}},
		{"description at the bound", `{"description":"` + strings.Repeat("x", 300) + `"}`,
			app.ExportInput{Description: strings.Repeat("x", 300), EvalCases: true}},
		{"name at the bound", `{"name":"` + strings.Repeat("a", 64) + `"}`,
			app.ExportInput{Name: strings.Repeat("a", 64), EvalCases: true}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := &fake{exported: app.ExportResult{FileName: "f.zip", Archive: []byte("z")}}
			rec := do(t, f, jsonReq(http.MethodPost, "/plugins/export", tc.body))
			wantStatus(t, rec, http.StatusOK)
			if f.call != "Export" || f.args[0] != workspaceID || f.args[1] != userID {
				t.Fatalf("call = %s %v, want Export for the workspace and user", f.call, f.args)
			}
			if diff := cmp.Diff(tc.want, f.args[2]); diff != "" {
				t.Errorf("ExportInput mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestExport_RefusesBadBodies(t *testing.T) {
	tooManyAgents := `{"agent_ids":[` + strings.TrimSuffix(strings.Repeat(`"`+agentID+`",`, 201), ",") + `]}`
	tooManySkills := `{"skill_ids":[` + strings.TrimSuffix(strings.Repeat(`"`+skillID+`",`, 501), ",") + `]}`
	tests := []struct {
		name        string
		contentType string
		body        string
		wantMessage string // a message that must appear among the issues
	}{
		{"unknown key", "application/json", `{"nope":1}`, "Unrecognized key(s) in object: 'nope'"},
		{"two unknown keys sorted", "application/json", `{"zeta":1,"alpha":2}`, "Unrecognized key(s) in object: 'alpha', 'zeta'"},
		{"known key next to an unknown one", "application/json", `{"name":"ok","mode":"merge"}`, "Unrecognized key(s) in object: 'mode'"},
		{"name not kebab-case", "application/json", `{"name":"My Plugin"}`, "Invalid"},
		{"name empty", "application/json", `{"name":""}`, "String must contain at least 1 character(s)"},
		{"name too long", "application/json", `{"name":"` + strings.Repeat("a", 65) + `"}`, "String must contain at most 64 character(s)"},
		{"name wrong type", "application/json", `{"name":5}`, "Expected string, received number"},
		{"name null", "application/json", `{"name":null}`, "Expected string, received null"},
		{"description too long", "application/json", `{"description":"` + strings.Repeat("x", 301) + `"}`, "String must contain at most 300 character(s)"},
		{"agent id not a uuid", "application/json", `{"agent_ids":["nope"]}`, "Invalid uuid"},
		{"agent id wrong type", "application/json", `{"agent_ids":[1]}`, "Expected string, received number"},
		{"agent ids not an array", "application/json", `{"agent_ids":"` + agentID + `"}`, "Expected array, received string"},
		{"agent ids empty", "application/json", `{"agent_ids":[]}`, "Array must contain at least 1 element(s)"},
		{"skill ids empty", "application/json", `{"skill_ids":[]}`, "Array must contain at least 1 element(s)"},
		{"too many agent ids", "application/json", tooManyAgents, "Array must contain at most 200 element(s)"},
		{"too many skill ids", "application/json", tooManySkills, "Array must contain at most 500 element(s)"},
		{"flag wrong type", "application/json", `{"include_eval_cases":"yes"}`, "Expected boolean, received string"},
		{"learnings flag null", "application/json", `{"include_learnings":null}`, "Expected boolean, received null"},
		{"array body", "application/json", `[]`, "Expected object, received array"},
		{"null body", "application/json", `null`, "Expected object, received null"},
		{"text/plain body is a string", "text/plain", `{}`, "Expected object, received string"},
		{"no body", "", ``, "Required"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := &fake{}
			r := request{method: http.MethodPost, path: "/plugins/export", contentType: tc.contentType}
			if tc.body != "" {
				r.body = []byte(tc.body)
			}
			rec := do(t, f, r)
			wantStatus(t, rec, http.StatusUnprocessableEntity)
			eb := errBody(t, rec)
			if eb.Code != "validation_error" || eb.Message != httpx.ValidationMessage {
				t.Errorf("error = %s %q, want validation_error %q", eb.Code, eb.Message, httpx.ValidationMessage)
			}
			if !strings.Contains(rec.Body.String(), tc.wantMessage) {
				t.Errorf("issues do not contain %q:\n%s", tc.wantMessage, rec.Body.String())
			}
			if f.calls != 0 {
				t.Errorf("the service was called %d times for an invalid body", f.calls)
			}
		})
	}
}

func TestExport_UnknownKeyIssueShape(t *testing.T) {
	rec := do(t, &fake{}, jsonReq(http.MethodPost, "/plugins/export", `{"nope":1}`))
	wantStatus(t, rec, http.StatusUnprocessableEntity)
	wantBody(t, rec, `{"error":{"code":"validation_error","message":"Request validation failed","details":[{`+
		`"keyword":"unrecognized_keys","instancePath":"/","schemaPath":"#//unrecognized_keys",`+
		`"message":"Unrecognized key(s) in object: 'nope'","params":{"issue":{"code":"unrecognized_keys","path":[],`+
		`"message":"Unrecognized key(s) in object: 'nope'"}}}]}}`)
}

// ---- POST /plugins/import --------------------------------------------------------------------

func samplePreview() domain.PreviewDoc {
	return domain.PreviewDoc{
		Manifest: domain.Manifest{
			Name: "acme", Version: "1.0.0", Format: domain.BundleFormat, Kind: domain.KindPlugin,
			ExportedAt: "2026-10-20T00:00:00.000Z",
			Counts:     domain.ManifestCounts{Agents: 1, Skills: 1},
		},
		Kind:      domain.KindPlugin,
		Counts:    domain.ManifestCounts{Agents: 1, Skills: 1},
		Agents:    []domain.PreviewAgent{{Name: "a", Provider: "openai", Model: "gpt", Enabled: true, Skills: []string{"s"}}},
		Skills:    []domain.PreviewSkill{{Name: "s", Type: "custom", Description: "sd", Enabled: true}},
		Conflicts: domain.Conflicts{Plugin: true, Skills: []string{"s"}},
		Ignored:   []domain.Ignored{{PathOrSection: "README.md", Reason: domain.ReasonUnknownKey}},
		Warnings:  []string{"w"},
	}
}

func TestImport_DryRunPreviewWireShape(t *testing.T) {
	f := &fake{preview: samplePreview()}
	rec := do(t, f, upload("/plugins/import?dry_run=true&filename=acme.zip", "application/zip", []byte("PKzip")))
	wantStatus(t, rec, http.StatusOK)
	wantBody(t, rec, `{"manifest":{"name":"acme","version":"1.0.0","format":"devdigest-plugin/v1","kind":"plugin",`+
		`"exported_at":"2026-10-20T00:00:00.000Z","description":null,`+
		`"counts":{"agents":1,"skills":1,"eval_cases":0,"conventions":0,"learnings":0}},`+
		`"kind":"plugin","counts":{"agents":1,"skills":1,"eval_cases":0,"conventions":0,"learnings":0},`+
		`"agents":[{"name":"a","provider":"openai","model":"gpt","enabled":true,"skills":["s"]}],`+
		`"skills":[{"name":"s","type":"custom","description":"sd","enabled":true}],`+
		`"conflicts":{"plugin":true,"skills":["s"]},`+
		`"ignored":[{"path_or_section":"README.md","reason":"`+domain.ReasonUnknownKey+`"}],"warnings":["w"]}`)
	if f.call != "Preview" {
		t.Fatalf("call = %s, want Preview", f.call)
	}
	want := app.ImportInput{Media: "application/zip", Body: []byte("PKzip"), FileName: "acme.zip"}
	if diff := cmp.Diff(want, f.args[1]); diff != "" {
		t.Errorf("ImportInput mismatch (-want +got):\n%s", diff)
	}
}

func TestImport_PreviewEmptyListsStayArrays(t *testing.T) {
	f := &fake{preview: domain.PreviewDoc{Manifest: domain.Manifest{Name: "n"}, Kind: domain.KindPlugin}}
	rec := do(t, f, upload("/plugins/import?dry_run=true", "application/json", []byte(`{}`)))
	wantStatus(t, rec, http.StatusOK)
	body := rec.Body.String()
	for _, want := range []string{`"agents":[]`, `"skills":[]`, `"conflicts":{"plugin":false,"skills":[]}`, `"ignored":[]`, `"warnings":[]`} {
		if !strings.Contains(body, want) {
			t.Errorf("body lacks %s:\n%s", want, body)
		}
	}
}

func TestImport_InstallWireShape(t *testing.T) {
	f := &fake{installed: domain.Installed{
		Plugin:  samplePlugin(),
		Created: domain.Created{Agents: 1, Skills: 2, EvalCases: 3, Learnings: 4},
	}}
	rec := do(t, f, upload("/plugins/import?dry_run=false&filename=acme.zip", "application/zip", []byte("PKzip")))
	wantStatus(t, rec, http.StatusCreated)
	wantBody(t, rec, `{"installed":`+samplePluginJSON+`,"created":{"agents":1,"skills":2,"eval_cases":3,"conventions":0,"learnings":4}}`)
	if f.call != "Install" {
		t.Fatalf("call = %s, want Install", f.call)
	}
}

func TestImport_QueryAndMedia(t *testing.T) {
	tests := []struct {
		name         string
		query        string
		contentType  string
		wantCall     string
		wantFileName string
		wantMedia    string
	}{
		{"no dry_run installs", "", "application/zip", "Install", "", "application/zip"},
		{"dry_run=false installs", "?dry_run=false&filename=a.zip", "application/zip", "Install", "a.zip", "application/zip"},
		{"dry_run=true previews", "?dry_run=true", "application/zip", "Preview", "", "application/zip"},
		{"json bundle with charset", "?dry_run=true&filename=b.json", "Application/JSON; charset=utf-8", "Preview", "b.json", "application/json"},
		{"unicode file name", "?dry_run=true&filename=%D0%BF%D0%BB%D0%B0%D0%B3%D0%B8%D0%BD.zip", "application/zip", "Preview", "плагин.zip", "application/zip"},
		{"file name of 255", "?dry_run=true&filename=" + strings.Repeat("a", 255), "application/zip", "Preview", strings.Repeat("a", 255), "application/zip"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := &fake{}
			rec := do(t, f, upload("/plugins/import"+tc.query, tc.contentType, []byte("body")))
			if rec.Code != http.StatusOK && rec.Code != http.StatusCreated {
				t.Fatalf("status = %d, want 200 or 201; body %s", rec.Code, rec.Body.String())
			}
			if f.call != tc.wantCall {
				t.Fatalf("call = %s, want %s", f.call, tc.wantCall)
			}
			want := app.ImportInput{Media: tc.wantMedia, Body: []byte("body"), FileName: tc.wantFileName}
			if diff := cmp.Diff(want, f.args[1]); diff != "" {
				t.Errorf("ImportInput mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestImport_MalformedDryRunIs400(t *testing.T) {
	for _, q := range []string{"?dry_run=1", "?dry_run=TRUE", "?dry_run=", "?dry_run=yes", "?dry_run=true&dry_run=false", "?dry_run=true&dry_run=true"} {
		t.Run(q, func(t *testing.T) {
			f := &fake{}
			rec := do(t, f, upload("/plugins/import"+q, "application/zip", []byte("z")))
			wantStatus(t, rec, http.StatusBadRequest)
			if eb := errBody(t, rec); eb.Code != "invalid_input" || eb.Message != "dry_run must be true or false" {
				t.Errorf("error = %s %q, want invalid_input %q", eb.Code, eb.Message, "dry_run must be true or false")
			}
			if f.calls != 0 {
				t.Errorf("the service was called %d times; a malformed query must not install anything", f.calls)
			}
		})
	}
}

func TestImport_BadFileNameIs422(t *testing.T) {
	tests := []struct {
		name  string
		query string
		want  string
	}{
		{"empty", "?filename=", "String must contain at least 1 character(s)"},
		{"256 characters", "?filename=" + strings.Repeat("a", 256), "String must contain at most 255 character(s)"},
		{"control character", "?filename=a%00b.zip", "Invalid file name"},
		{"newline", "?dry_run=true&filename=a%0Ab.zip", "Invalid file name"},
		{"invalid UTF-8", "?filename=%FF.zip", "Invalid file name"},
		{"given twice", "?filename=a.zip&filename=b.zip", "filename must be given once"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := &fake{}
			rec := do(t, f, upload("/plugins/import"+tc.query, "application/zip", []byte("z")))
			wantStatus(t, rec, http.StatusUnprocessableEntity)
			if eb := errBody(t, rec); eb.Code != "validation_error" {
				t.Errorf("code = %s, want validation_error", eb.Code)
			}
			if !strings.Contains(rec.Body.String(), tc.want) || !strings.Contains(rec.Body.String(), `"instancePath":"/filename"`) {
				t.Errorf("issue not at /filename with %q:\n%s", tc.want, rec.Body.String())
			}
			if f.calls != 0 {
				t.Errorf("the service was called %d times for a bad file name", f.calls)
			}
		})
	}
}

func TestImport_QueryIsCheckedBeforeBody(t *testing.T) {
	// A bad dry_run next to a bad file name answers the 400 (the query is read in order).
	rec := do(t, &fake{}, upload("/plugins/import?dry_run=x&filename=", "application/zip", []byte("z")))
	wantStatus(t, rec, http.StatusBadRequest)
}

func TestImport_UnsupportedMediaIs415(t *testing.T) {
	tests := []struct {
		name        string
		contentType string
		body        []byte
	}{
		{"text/plain", "text/plain", []byte("hello")},
		{"form", "application/x-www-form-urlencoded", []byte("a=b")},
		{"octet-stream", "application/octet-stream", []byte("PK")},
		{"no content type", "", []byte("PK")},
		{"zip lookalike", "application/zip-evil", []byte("PK")},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := &fake{}
			rec := do(t, f, request{method: http.MethodPost, path: "/plugins/import?dry_run=true", contentType: tc.contentType, body: tc.body})
			wantStatus(t, rec, http.StatusUnsupportedMediaType)
			if eb := errBody(t, rec); eb.Code != "unsupported_media_type" {
				t.Errorf("code = %s, want unsupported_media_type", eb.Code)
			}
			if f.calls != 0 {
				t.Errorf("the service was called %d times for an unsupported media type", f.calls)
			}
		})
	}
}

func TestImport_SizeBoundaries(t *testing.T) {
	const (
		zipMax  = 5 << 20
		jsonMax = 20 << 20
	)
	if zipMax != domain.ArchiveMaxZipBytes || jsonMax != domain.MaxInflatedBytes {
		t.Fatalf("test limits differ from the domain: zip %d json %d", domain.ArchiveMaxZipBytes, domain.MaxInflatedBytes)
	}
	tests := []struct {
		name        string
		contentType string
		size        int
		wantStatus  int
	}{
		{"zip at the limit", "application/zip", zipMax, http.StatusOK},
		{"zip one byte over", "application/zip", zipMax + 1, http.StatusRequestEntityTooLarge},
		{"json over the zip limit is fine", "application/json", zipMax + 1, http.StatusOK},
		{"json at the limit", "application/json", jsonMax, http.StatusOK},
		{"json one byte over", "application/json", jsonMax + 1, http.StatusRequestEntityTooLarge},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := &fake{}
			rec := do(t, f, upload("/plugins/import?dry_run=true", tc.contentType, bytes.Repeat([]byte("x"), tc.size)))
			wantStatus(t, rec, tc.wantStatus)
			if tc.wantStatus == http.StatusRequestEntityTooLarge {
				if eb := errBody(t, rec); eb.Code != "payload_too_large" {
					t.Errorf("code = %s, want payload_too_large", eb.Code)
				}
				if f.calls != 0 {
					t.Errorf("the service was called %d times for an oversized body", f.calls)
				}
				return
			}
			if got := len(f.args[1].(app.ImportInput).Body); got != tc.size {
				t.Errorf("service got %d bytes, want %d", got, tc.size)
			}
		})
	}
}

func TestImport_UnsupportedMediaFromTheServiceIs415(t *testing.T) {
	// The service reports it as a 400 invalid_input whose cause is domain.ErrUnsupportedMedia
	// (apperr has no 415 kind); the transport answers 415 before the generic mapping.
	errs := map[string]error{
		"bare domain error": domain.ErrUnsupportedMedia,
		"service error": apperr.InvalidInput("Unsupported Media Type").WithCode(app.CodeUnsupportedMediaType).
			WithCause(domain.ErrUnsupportedMedia),
	}
	for name, err := range errs {
		for _, dry := range []string{"true", "false"} {
			t.Run(name+"/dry_run="+dry, func(t *testing.T) {
				rec := do(t, &fake{err: err}, upload("/plugins/import?dry_run="+dry, "application/zip", []byte("z")))
				wantStatus(t, rec, http.StatusUnsupportedMediaType)
				wantBody(t, rec, `{"error":{"code":"unsupported_media_type","message":"Unsupported Media Type"}}`)
			})
		}
	}
}

func TestImport_KeepsTheForeignOriginAndHostGuards(t *testing.T) {
	t.Run("foreign origin", func(t *testing.T) {
		f := &fake{}
		r := upload("/plugins/import?dry_run=true", "application/zip", []byte("z"))
		r.headers = map[string]string{"Origin": "http://evil.example"}
		rec := do(t, f, r)
		wantStatus(t, rec, http.StatusForbidden)
		if f.calls != 0 {
			t.Errorf("the service was called %d times for a foreign origin", f.calls)
		}
	})
	t.Run("allowed origin", func(t *testing.T) {
		r := upload("/plugins/import?dry_run=true", "application/zip", []byte("z"))
		r.headers = map[string]string{"Origin": uiOrigin}
		wantStatus(t, do(t, &fake{}, r), http.StatusOK)
	})
	t.Run("foreign host", func(t *testing.T) {
		e := newEcho(t, &fake{}, httpx.RateLimit{}, fakeTenants{})
		req := httptest.NewRequest(http.MethodPost, "/plugins/import?dry_run=true", strings.NewReader("z"))
		req.Host = "rebind.example"
		req.Header.Set("Content-Type", "application/zip")
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		wantStatus(t, rec, http.StatusForbidden)
	})
}

// ---- PATCH /plugins/:id ----------------------------------------------------------------------

func TestSetEnabled_Happy(t *testing.T) {
	for _, enabled := range []bool{true, false} {
		t.Run(strconv.FormatBool(enabled), func(t *testing.T) {
			p := samplePlugin()
			p.Enabled = enabled
			f := &fake{plugin: p}
			rec := do(t, f, jsonReq(http.MethodPatch, "/plugins/"+pluginID, `{"enabled":`+strconv.FormatBool(enabled)+`}`))
			wantStatus(t, rec, http.StatusOK)
			want := strings.Replace(samplePluginJSON, `"enabled":false`, `"enabled":`+strconv.FormatBool(enabled), 1)
			wantBody(t, rec, want)
			if diff := cmp.Diff([]any{workspaceID, pluginID, enabled}, f.args); diff != "" {
				t.Errorf("args mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestSetEnabled_RefusesBadRequests(t *testing.T) {
	tests := []struct {
		name string
		path string
		ct   string
		body string
		want string
	}{
		{"missing enabled", "/plugins/" + pluginID, "application/json", `{}`, "Required"},
		{"not a boolean", "/plugins/" + pluginID, "application/json", `{"enabled":"true"}`, "Expected boolean, received string"},
		{"null", "/plugins/" + pluginID, "application/json", `{"enabled":null}`, "Expected boolean, received null"},
		{"unknown key", "/plugins/" + pluginID, "application/json", `{"enabled":true,"name":"x"}`, "Unrecognized key(s) in object: 'name'"},
		{"text/plain", "/plugins/" + pluginID, "text/plain", `{"enabled":true}`, "Expected object, received string"},
		{"id not a uuid", "/plugins/abc", "application/json", `{"enabled":true}`, "Invalid uuid"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := &fake{}
			rec := do(t, f, request{method: http.MethodPatch, path: tc.path, contentType: tc.ct, body: []byte(tc.body)})
			wantStatus(t, rec, http.StatusUnprocessableEntity)
			if eb := errBody(t, rec); eb.Code != "validation_error" {
				t.Errorf("code = %s, want validation_error", eb.Code)
			}
			if !strings.Contains(rec.Body.String(), tc.want) {
				t.Errorf("issues do not contain %q:\n%s", tc.want, rec.Body.String())
			}
			if f.calls != 0 {
				t.Errorf("the service was called %d times", f.calls)
			}
		})
	}
}

// ---- DELETE /plugins/:id ---------------------------------------------------------------------

func TestRemove_Happy(t *testing.T) {
	f := &fake{removed: domain.Removed{Agents: 1, Skills: 2, EvalCases: 3, Learnings: 4, CIInstallations: 5}}
	rec := do(t, f, jsonReq(http.MethodDelete, "/plugins/"+pluginID, `{"confirm":" Acme "}`))
	wantStatus(t, rec, http.StatusOK)
	wantBody(t, rec, `{"ok":true,"removed":{"agents":1,"skills":2,"eval_cases":3,"learnings":4,"ci_installations":5}}`)
	// The confirmation reaches the service exactly as typed.
	if diff := cmp.Diff([]any{workspaceID, pluginID, " Acme "}, f.args); diff != "" {
		t.Errorf("args mismatch (-want +got):\n%s", diff)
	}
}

func TestRemove_RefusesBadRequests(t *testing.T) {
	tests := []struct {
		name string
		path string
		ct   string
		body string
		want string
	}{
		{"missing confirm", "/plugins/" + pluginID, "application/json", `{}`, "Required"},
		{"not a string", "/plugins/" + pluginID, "application/json", `{"confirm":5}`, "Expected string, received number"},
		{"unknown key", "/plugins/" + pluginID, "application/json", `{"confirm":"a","force":true}`, "Unrecognized key(s) in object: 'force'"},
		{"text/plain", "/plugins/" + pluginID, "text/plain", `{"confirm":"a"}`, "Expected object, received string"},
		{"id not a uuid", "/plugins/abc", "application/json", `{"confirm":"a"}`, "Invalid uuid"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := &fake{}
			rec := do(t, f, request{method: http.MethodDelete, path: tc.path, contentType: tc.ct, body: []byte(tc.body)})
			wantStatus(t, rec, http.StatusUnprocessableEntity)
			if !strings.Contains(rec.Body.String(), tc.want) {
				t.Errorf("issues do not contain %q:\n%s", tc.want, rec.Body.String())
			}
			if f.calls != 0 {
				t.Errorf("the service was called %d times", f.calls)
			}
		})
	}
}

func TestRemove_NoBodyIsRequired(t *testing.T) {
	f := &fake{}
	rec := do(t, f, request{method: http.MethodDelete, path: "/plugins/" + pluginID})
	wantStatus(t, rec, http.StatusUnprocessableEntity)
	if f.calls != 0 {
		t.Errorf("the service was called %d times", f.calls)
	}
}

// ---- error mapping ---------------------------------------------------------------------------

func TestErrors_ApplicationErrorsRenderedAsTheEnvelope(t *testing.T) {
	routes := map[string]request{
		"list":    {method: http.MethodGet, path: "/plugins"},
		"export":  jsonReq(http.MethodPost, "/plugins/export", `{}`),
		"preview": upload("/plugins/import?dry_run=true", "application/zip", []byte("z")),
		"install": upload("/plugins/import", "application/zip", []byte("z")),
		"patch":   jsonReq(http.MethodPatch, "/plugins/"+pluginID, `{"enabled":true}`),
		"delete":  jsonReq(http.MethodDelete, "/plugins/"+pluginID, `{"confirm":"x"}`),
	}
	errs := []struct {
		name    string
		err     error
		status  int
		code    string
		details string // JSON of details, "" = none
	}{
		{
			"invalid_plugin",
			apperr.Validation("name is required").WithCode(app.CodeInvalidPlugin).WithDetails(map[string]any{"issues": []string{"name is required", "second"}}),
			http.StatusUnprocessableEntity, "invalid_plugin", `{"issues":["name is required","second"]}`,
		},
		{
			"plugin_conflict",
			apperr.Conflict("exists").WithCode(app.CodePluginConflict).WithDetails(map[string]any{"plugin": true, "skills": []string{"s1"}}),
			http.StatusConflict, "plugin_conflict", `{"plugin":true,"skills":["s1"]}`,
		},
		{
			"not_found with ids",
			apperr.NotFound("Agent or skill not found").WithDetails(map[string]any{"ids": []string{agentID}}),
			http.StatusNotFound, "not_found", `{"ids":["` + agentID + `"]}`,
		},
		{"confirmation_mismatch", apperr.Validation("no").WithCode(app.CodeConfirmationMismatch), http.StatusUnprocessableEntity, "confirmation_mismatch", ""},
		{"nothing_to_export", apperr.Validation("nothing").WithCode(app.CodeNothingToExport), http.StatusUnprocessableEntity, "nothing_to_export", ""},
		{"export_too_large", apperr.Validation("big").WithCode(app.CodeExportTooLarge), http.StatusUnprocessableEntity, "export_too_large", ""},
		{"validation_error", apperr.Validation("bad name"), http.StatusUnprocessableEntity, "validation_error", ""},
		{"internal", apperr.Internal("Internal error").WithCause(errors.New("db down")), http.StatusInternalServerError, "internal_error", ""},
		{"plain error", errors.New("boom"), http.StatusInternalServerError, "internal_error", ""},
	}
	for route, req := range routes {
		for _, tc := range errs {
			t.Run(route+"/"+tc.name, func(t *testing.T) {
				rec := do(t, &fake{err: tc.err}, req)
				wantStatus(t, rec, tc.status)
				eb := errBody(t, rec)
				if eb.Code != tc.code {
					t.Errorf("code = %s, want %s", eb.Code, tc.code)
				}
				gotDetails := ""
				if eb.Details != nil {
					b, err := json.Marshal(eb.Details)
					if err != nil {
						t.Fatal(err)
					}
					gotDetails = string(b)
				}
				if gotDetails != tc.details {
					t.Errorf("details = %s, want %s", gotDetails, tc.details)
				}
				if strings.Contains(rec.Body.String(), "db down") || strings.Contains(rec.Body.String(), "boom") {
					t.Errorf("a cause leaked to the client: %s", rec.Body.String())
				}
			})
		}
	}
}

func TestErrors_DomainErrorsGetTheSameWire(t *testing.T) {
	tests := []struct {
		name    string
		err     error
		status  int
		code    string
		details string
	}{
		{
			"invalid upload", &domain.InvalidError{Issues: []string{"first", "second"}},
			http.StatusUnprocessableEntity, "invalid_plugin", `{"issues":["first","second"]}`,
		},
		{
			"conflict", &domain.ConflictError{Conflicts: domain.Conflicts{Plugin: true, Skills: []string{"a", "b"}}},
			http.StatusConflict, "plugin_conflict", `{"plugin":true,"skills":["a","b"]}`,
		},
		{
			"conflict without skills has an empty list", &domain.ConflictError{Conflicts: domain.Conflicts{Plugin: true}},
			http.StatusConflict, "plugin_conflict", `{"plugin":true,"skills":[]}`,
		},
		{"wrapped not found", errors.Join(errors.New("ctx"), domain.ErrNotFound), http.StatusNotFound, "not_found", ""},
		{"confirmation mismatch", domain.ErrConfirmationMismatch, http.StatusUnprocessableEntity, "confirmation_mismatch", ""},
		{"export too large", domain.ErrExportTooLarge, http.StatusUnprocessableEntity, "export_too_large", ""},
		{"invalid export name", domain.ErrInvalidExportName, http.StatusUnprocessableEntity, "validation_error", ""},
		{"invalid file name", domain.ErrInvalidFileName, http.StatusUnprocessableEntity, "validation_error", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := do(t, &fake{err: tc.err}, upload("/plugins/import", "application/zip", []byte("z")))
			wantStatus(t, rec, tc.status)
			eb := errBody(t, rec)
			if eb.Code != tc.code {
				t.Errorf("code = %s, want %s", eb.Code, tc.code)
			}
			gotDetails := ""
			if eb.Details != nil {
				b, err := json.Marshal(eb.Details)
				if err != nil {
					t.Fatal(err)
				}
				gotDetails = string(b)
			}
			if gotDetails != tc.details {
				t.Errorf("details = %s, want %s", gotDetails, tc.details)
			}
		})
	}
}

func TestErrors_UnresolvedIdentityIs500(t *testing.T) {
	e := newEcho(t, &fake{}, httpx.RateLimit{}, fakeTenants{err: errors.New("not provisioned")})
	rec := send(e, request{method: http.MethodGet, path: "/plugins"})
	wantStatus(t, rec, http.StatusInternalServerError)
	if strings.Contains(rec.Body.String(), "provisioned") {
		t.Errorf("the cause leaked: %s", rec.Body.String())
	}
}

// ---- rate limits -----------------------------------------------------------------------------

func TestRateLimitBuckets_Declared(t *testing.T) {
	want := []httpx.Bucket{
		{Method: http.MethodPost, Path: "/plugins/import", Limit: 10},
		{Method: http.MethodPost, Path: "/plugins/export", Limit: 10},
	}
	got := pluginshttp.RateLimitBuckets()
	if diff := cmp.Diff(want, got, cmpopts.IgnoreFields(httpx.Bucket{}, "Key")); diff != "" {
		t.Errorf("RateLimitBuckets() mismatch (-want +got):\n%s", diff)
	}
	for _, b := range got {
		if b.Key != nil || b.Name != "" {
			t.Errorf("bucket %s %s must count per client on its own counter", b.Method, b.Path)
		}
	}
}

func TestRateLimit_TenPerMinutePerRouteAndSeparate(t *testing.T) {
	rl := httpx.RateLimit{Enabled: true, Limit: 1000, Buckets: pluginshttp.RateLimitBuckets()}
	f := &fake{exported: app.ExportResult{FileName: "f.zip", Archive: []byte("z")}}
	e := newEcho(t, f, rl, fakeTenants{})
	imp := upload("/plugins/import?dry_run=true", "application/zip", []byte("z"))
	exp := jsonReq(http.MethodPost, "/plugins/export", `{}`)

	for i := 1; i <= 10; i++ {
		if rec := send(e, imp); rec.Code != http.StatusOK {
			t.Fatalf("import %d: status = %d, want 200", i, rec.Code)
		}
	}
	rec := send(e, imp)
	wantStatus(t, rec, http.StatusTooManyRequests)
	if rec.Header().Get("Retry-After") == "" {
		t.Error("the 429 has no Retry-After")
	}
	// The export bucket is its own counter: spent imports do not touch it.
	for i := 1; i <= 10; i++ {
		if rec := send(e, exp); rec.Code != http.StatusOK {
			t.Fatalf("export %d: status = %d, want 200", i, rec.Code)
		}
	}
	wantStatus(t, send(e, exp), http.StatusTooManyRequests)
	// Other routes keep the global budget.
	wantStatus(t, send(e, request{method: http.MethodGet, path: "/plugins"}), http.StatusOK)
}

func TestRateLimit_RunsBeforeTheBodyIsRead(t *testing.T) {
	rl := httpx.RateLimit{Enabled: true, Limit: 1000, Buckets: []httpx.Bucket{{Method: http.MethodPost, Path: pluginshttp.ImportPath, Limit: 1}}}
	f := &fake{}
	e := newEcho(t, f, rl, fakeTenants{})
	wantStatus(t, send(e, upload("/plugins/import?dry_run=true", "application/zip", []byte("z"))), http.StatusOK)
	big := upload("/plugins/import?dry_run=true", "application/zip", bytes.Repeat([]byte("x"), 5<<20+1))
	wantStatus(t, send(e, big), http.StatusTooManyRequests)
	if f.calls != 1 {
		t.Errorf("service calls = %d, want 1", f.calls)
	}
}

func TestRawBodies_KeyMatchesTheRegisteredRoute(t *testing.T) {
	got := pluginshttp.RawBodies()
	want := map[string]map[string]int64{"POST /plugins/import": {"application/zip": 5 << 20, "application/json": 20 << 20}}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("RawBodies() mismatch (-want +got):\n%s", diff)
	}
	e := newEcho(t, &fake{}, httpx.RateLimit{}, fakeTenants{})
	found := false
	for _, r := range e.Router().Routes() {
		if r.Method == http.MethodPost && r.Path == "/plugins/import" {
			found = true
		}
	}
	if !found {
		t.Error("POST /plugins/import is not a registered route")
	}
}

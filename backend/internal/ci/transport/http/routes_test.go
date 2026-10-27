package cihttp_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/labstack/echo/v5"

	"dev-digest-go/backend/internal/ci/app"
	"dev-digest-go/backend/internal/ci/domain"
	cihttp "dev-digest-go/backend/internal/ci/transport/http"
	"dev-digest-go/backend/internal/platform/apperr"
	"dev-digest-go/backend/internal/platform/httpx"
	"dev-digest-go/backend/internal/tenancy"
)

const (
	workspaceID = "11111111-2222-4333-8444-555555555555"
	agentID     = "7e1d0000-0000-4000-8000-000000000001"
	instID      = "7e1d0000-0000-4000-8000-0000000000a1"
	uiOrigin    = "http://localhost:3100"
)

func ptr[T any](v T) *T { return &v }

// fakeCI implements app.Exporter and app.Ingest, records the calls and answers canned values.
type fakeCI struct {
	mu    sync.Mutex
	calls []string

	exportReq   app.ExportRequest
	runFilter   app.RunFilter
	refreshFor  string
	refreshWs   string
	exportAgent string

	err     error
	export  domain.Export
	insts   []domain.Installation
	runs    []domain.Run
	groups  []domain.RepoGroup
	refresh domain.RefreshResult
}

var (
	_ app.Exporter = (*fakeCI)(nil)
	_ app.Ingest   = (*fakeCI)(nil)

	_ app.RepoInstallations = (*fakeCI)(nil)
)

func (f *fakeCI) note(s string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, s)
}

func (f *fakeCI) callList() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

func (f *fakeCI) Export(_ context.Context, ws, id string, req app.ExportRequest) (domain.Export, error) {
	f.note("Export")
	f.exportReq, f.exportAgent = req, id
	return f.export, f.err
}

func (f *fakeCI) ListInstallations(_ context.Context, ws, id string) ([]domain.Installation, error) {
	f.note("ListInstallations")
	f.exportAgent = id
	return f.insts, f.err
}

func (f *fakeCI) ListRuns(_ context.Context, ws string, flt app.RunFilter) ([]domain.Run, error) {
	f.note("ListRuns")
	f.runFilter = flt
	return f.runs, f.err
}

func (f *fakeCI) ListByRepo(_ context.Context, ws string) ([]domain.RepoGroup, error) {
	f.note("ListByRepo")
	f.refreshWs = ws
	return f.groups, f.err
}

func (f *fakeCI) Refresh(_ context.Context, ws, id string) (domain.RefreshResult, error) {
	f.note("Refresh")
	f.refreshWs, f.refreshFor = ws, id
	return f.refresh, f.err
}

type fakeTenants struct{}

func (fakeTenants) Resolve(context.Context) (tenancy.Identity, error) {
	return tenancy.Identity{WorkspaceID: workspaceID}, nil
}

func newServer(t *testing.T, f *fakeCI) *httptest.Server {
	t.Helper()
	e := httpx.New(httpx.Options{
		Log:          slog.New(slog.DiscardHandler),
		CORSOrigins:  []string{uiOrigin},
		AllowedHosts: []string{"127.0.0.1"},
	})
	cihttp.AddRoutes(e, f, f, fakeTenants{})
	cihttp.AddInstallationsRoute(e, f, fakeTenants{})
	srv := httptest.NewServer(e)
	t.Cleanup(srv.Close)
	return srv
}

type response struct {
	Status int
	Body   string
}

// send performs a request; contentType "" sends none.
func send(t *testing.T, srv *httptest.Server, method, path, contentType, body string, headers ...string) response {
	t.Helper()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req, err := http.NewRequestWithContext(t.Context(), method, srv.URL+path, rd)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
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
		t.Fatalf("read body of %s %s: %v", method, path, err)
	}
	return response{Status: resp.StatusCode, Body: string(b)}
}

func postJSON(t *testing.T, srv *httptest.Server, path, body string) response {
	t.Helper()
	return send(t, srv, http.MethodPost, path, "application/json", body)
}

func decode(t *testing.T, r response) any {
	t.Helper()
	var v any
	if err := json.Unmarshal([]byte(r.Body), &v); err != nil {
		t.Fatalf("response %d %q is not JSON: %v", r.Status, r.Body, err)
	}
	return v
}

type issueDetail struct{ Keyword, InstancePath, Message string }

// validationIssues asserts a 422 validation_error and returns its details.
func validationIssues(t *testing.T, r response) []issueDetail {
	t.Helper()
	var body struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
			Details []struct {
				Keyword      string `json:"keyword"`
				InstancePath string `json:"instancePath"`
				Message      string `json:"message"`
			} `json:"details"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(r.Body), &body); err != nil {
		t.Fatalf("decode %q: %v", r.Body, err)
	}
	if r.Status != http.StatusUnprocessableEntity || body.Error.Code != "validation_error" || body.Error.Message != "Request validation failed" {
		t.Fatalf("response = %d %s, want 422 validation_error", r.Status, r.Body)
	}
	var out []issueDetail
	for _, d := range body.Error.Details {
		out = append(out, issueDetail{d.Keyword, d.InstancePath, d.Message})
	}
	return out
}

func assertError(t *testing.T, r response, status int, code, message string) map[string]any {
	t.Helper()
	var body struct {
		Error map[string]any `json:"error"`
	}
	if err := json.Unmarshal([]byte(r.Body), &body); err != nil {
		t.Fatalf("decode %q: %v", r.Body, err)
	}
	if r.Status != status || body.Error["code"] != code || body.Error["message"] != message {
		t.Fatalf("response = %d %s, want %d %s %q", r.Status, r.Body, status, code, message)
	}
	return body.Error
}

var ranAt = time.Date(2026, 10, 20, 12, 30, 15, 123_000_000, time.UTC)

func sampleRun() domain.Run {
	return domain.Run{
		ID: "run-1", InstallationID: ptr(instID), PRNumber: ptr(7), RanAt: ptr(ranAt), Status: ptr("succeeded"),
		FindingsCount: ptr(3), CostUSD: ptr(0.0123), GithubURL: ptr("https://github.com/acme/widgets/actions/runs/9"),
		Source: ptr("gha"), AgentName: ptr("Reviewer"), DurationMS: ptr(4321), AgentID: ptr(agentID), Repo: ptr("acme/widgets"),
		Blockers: ptr(1), HeadSHA: ptr("abc123"), ManifestVersion: ptr(2),
	}
}

func wantRunJSON() map[string]any {
	return map[string]any{
		"id": "run-1", "ci_installation_id": instID, "pr_number": 7.0, "ran_at": "2026-10-20T12:30:15.123Z", "status": "succeeded",
		"findings_count": 3.0, "cost_usd": 0.0123, "github_url": "https://github.com/acme/widgets/actions/runs/9", "source": "gha",
		"agent": "Reviewer", "duration_s": 4.321, "agent_id": agentID, "repo": "acme/widgets", "verdict": "blocked", "blockers": 1.0,
		"head_sha": "abc123", "manifest_version": 2.0, "provider": "github", "host": "github.com", "pr_url": nil,
	}
}

func TestExport_Statuses(t *testing.T) {
	t.Parallel()
	installed := time.Date(2026, 10, 19, 8, 0, 0, 0, time.UTC)
	f := &fakeCI{export: domain.Export{
		Installation: &domain.Installation{ID: instID, AgentID: agentID, Repo: "acme/widgets", TargetType: "gha", InstalledAt: installed, Slug: ptr("reviewer"), ManifestVersion: ptr(2)},
		Files:        []domain.File{{Path: ".github/workflows/devdigest-review-reviewer.yml", Contents: "on: x\n", Editable: true}, {Path: "a", Contents: "b"}},
		PRURL:        ptr("https://github.com/acme/widgets/pull/1"),

		RequiredSecrets:   []string{"DEVDIGEST_MY_GW_API_KEY", "DEVDIGEST_MY_GW_HEADER_1"},
		RequiredVariables: []string{"DEVDIGEST_MY_GW_BASE_URL"},
	}}
	srv := newServer(t, f)

	r := postJSON(t, srv, "/agents/"+agentID+"/export-ci", `{"repo":"acme/widgets","action":"open_pr"}`)
	if r.Status != http.StatusCreated {
		t.Fatalf("open_pr status = %d, want 201 (%s)", r.Status, r.Body)
	}
	want := map[string]any{
		"installation": map[string]any{
			"id": instID, "agent_id": agentID, "repo": "acme/widgets", "target_type": "gha", "installed_at": "2026-10-19T08:00:00.000Z",
			"slug": "reviewer", "workflow_path": ".github/workflows/devdigest-review-reviewer.yml", "manifest_version": 2.0, "last_run": nil,
			"provider": "github", "host": "github.com",
		},
		"files": []any{
			map[string]any{"path": ".github/workflows/devdigest-review-reviewer.yml", "contents": "on: x\n", "editable": true},
			map[string]any{"path": "a", "contents": "b", "editable": false},
		},
		"pr_url":             "https://github.com/acme/widgets/pull/1",
		"required_secrets":   []any{"DEVDIGEST_MY_GW_API_KEY", "DEVDIGEST_MY_GW_HEADER_1"},
		"required_variables": []any{"DEVDIGEST_MY_GW_BASE_URL"},
		"gitlab_ci":          nil,
	}
	if diff := cmp.Diff(want, decode(t, r)); diff != "" {
		t.Errorf("open_pr body mismatch (-want +got):\n%s", diff)
	}
	if !strings.HasPrefix(r.Body, `{"installation":{"id":`) || !strings.Contains(r.Body, `"files":[`) || !strings.HasSuffix(strings.TrimSpace(r.Body), `"pr_url":"https://github.com/acme/widgets/pull/1","required_secrets":["DEVDIGEST_MY_GW_API_KEY","DEVDIGEST_MY_GW_HEADER_1"],"required_variables":["DEVDIGEST_MY_GW_BASE_URL"],"gitlab_ci":null}`) {
		t.Errorf("keys are not in contract order: %s", r.Body)
	}

	f.export = domain.Export{Files: []domain.File{{Path: "w", Contents: "c", Editable: true}}, RequiredSecrets: []string{"OPENAI_API_KEY"}}
	r = postJSON(t, srv, "/agents/"+agentID+"/export-ci", `{"repo":"acme/widgets","action":"files"}`)
	if r.Status != http.StatusOK {
		t.Fatalf("files status = %d, want 200 (%s)", r.Status, r.Body)
	}
	if diff := cmp.Diff(map[string]any{"installation": nil, "files": []any{map[string]any{"path": "w", "contents": "c", "editable": true}}, "pr_url": nil, "required_secrets": []any{"OPENAI_API_KEY"}, "required_variables": []any{}, "gitlab_ci": nil}, decode(t, r)); diff != "" {
		t.Errorf("files body mismatch (-want +got):\n%s", diff)
	}

	f.export = domain.Export{}
	r = postJSON(t, srv, "/agents/"+agentID+"/export-ci", `{"repo":"acme/widgets","action":"files"}`)
	if !strings.Contains(r.Body, `"files":[]`) {
		t.Errorf("empty files must be [] not null: %s", r.Body)
	}
	if !strings.Contains(r.Body, `"required_secrets":[]`) || !strings.Contains(r.Body, `"required_variables":[]`) {
		t.Errorf("empty required lists must be [] not null: %s", r.Body)
	}
}

func TestExport_RequestMapping(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		body string
		want app.ExportRequest
	}{
		{"defaults", `{"repo":"acme/widgets"}`, app.ExportRequest{
			Repo: "acme/widgets", Action: domain.ActionOpenPR, PostAs: domain.PostAsGitHubReview, Runner: domain.RunnerGitHubHosted,
			Triggers: []string{"opened", "synchronize", "reopened"}}},
		{"all fields", `{"repo":"acme/widgets","target":"gha","action":"files","post_as":"none","runner":"self_hosted","triggers":["reopened","opened"],"base":"x","workflow":"on: y\n","extra":1}`, app.ExportRequest{
			Repo: "acme/widgets", Target: "gha", Action: domain.ActionFiles, PostAs: domain.PostAsNone, Runner: domain.RunnerSelfHosted,
			Triggers: []string{"reopened", "opened"}, Workflow: ptr("on: y\n")}},
		{"runner github_hosted", `{"repo":"r","runner":"github_hosted"}`, app.ExportRequest{
			Repo: "r", Action: domain.ActionOpenPR, PostAs: domain.PostAsGitHubReview, Runner: domain.RunnerGitHubHosted,
			Triggers: []string{"opened", "synchronize", "reopened"}}},
		{"null workflow", `{"repo":"r","workflow":null}`, app.ExportRequest{
			Repo: "r", Action: domain.ActionOpenPR, PostAs: domain.PostAsGitHubReview, Runner: domain.RunnerGitHubHosted,
			Triggers: []string{"opened", "synchronize", "reopened"}}},
		{"empty workflow stays empty", `{"repo":"r","workflow":""}`, app.ExportRequest{
			Repo: "r", Action: domain.ActionOpenPR, PostAs: domain.PostAsGitHubReview, Runner: domain.RunnerGitHubHosted,
			Triggers: []string{"opened", "synchronize", "reopened"}, Workflow: ptr("")}},
		{"unsupported target reaches the use case", `{"repo":"r","target":"circle"}`, app.ExportRequest{
			Repo: "r", Target: "circle", Action: domain.ActionOpenPR, PostAs: domain.PostAsGitHubReview, Runner: domain.RunnerGitHubHosted,
			Triggers: []string{"opened", "synchronize", "reopened"}}},
		{"empty triggers reach the use case", `{"repo":" a/b/c ","triggers":[]}`, app.ExportRequest{
			Repo: " a/b/c ", Action: domain.ActionOpenPR, PostAs: domain.PostAsGitHubReview, Runner: domain.RunnerGitHubHosted,
			Triggers: []string{}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := &fakeCI{}
			r := postJSON(t, newServer(t, f), "/agents/"+agentID+"/export-ci", tc.body)
			if r.Status != http.StatusOK && r.Status != http.StatusCreated {
				t.Fatalf("status = %d (%s), want 200 or 201", r.Status, r.Body)
			}
			if diff := cmp.Diff(tc.want, f.exportReq); diff != "" {
				t.Errorf("ExportRequest mismatch (-want +got):\n%s", diff)
			}
			if f.exportAgent != agentID {
				t.Errorf("agent id = %q, want %q", f.exportAgent, agentID)
			}
		})
	}
}

func TestExport_ValidationIssues(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		body string
		want []issueDetail
	}{
		{"missing repo", `{}`, []issueDetail{{"invalid_type", "/repo", "Required"}}},
		{"empty repo", `{"repo":""}`, []issueDetail{{"too_small", "/repo", "String must contain at least 1 character(s)"}}},
		{"repo number", `{"repo":5}`, []issueDetail{{"invalid_type", "/repo", "Expected string, received number"}}},
		{"repo null", `{"repo":null}`, []issueDetail{{"invalid_type", "/repo", "Expected string, received null"}}},
		{"bad enum", `{"repo":"a/b","action":"merge"}`, []issueDetail{{"invalid_enum_value", "/action", "Invalid enum value. Expected 'open_pr' | 'files', received 'merge'"}}},
		{"enum wrong type", `{"repo":"a/b","post_as":1}`, []issueDetail{{"invalid_type", "/post_as", "Expected 'github_review' | 'pr_comment' | 'none', received number"}}},
		{"runner enum", `{"repo":"a/b","runner":"windows"}`, []issueDetail{{"invalid_enum_value", "/runner", "Invalid enum value. Expected 'github_hosted' | 'self_hosted', received 'windows'"}}},
		{"runner wrong type", `{"repo":"a/b","runner":1}`, []issueDetail{{"invalid_type", "/runner", "Expected 'github_hosted' | 'self_hosted', received number"}}},
		{"target null", `{"repo":"a/b","target":null}`, []issueDetail{{"invalid_type", "/target", "Expected 'gha' | 'circle' | 'jenkins' | 'cli' | 'gitlab_ci', received null"}}},
		{"triggers not array", `{"repo":"a/b","triggers":"opened"}`, []issueDetail{{"invalid_type", "/triggers", "Expected array, received string"}}},
		{"trigger element", `{"repo":"a/b","triggers":["opened",1]}`, []issueDetail{{"invalid_type", "/triggers/1", "Expected string, received number"}}},
		{"base type", `{"repo":"a/b","base":false}`, []issueDetail{{"invalid_type", "/base", "Expected string, received boolean"}}},
		{"workflow type", `{"repo":"a/b","workflow":3}`, []issueDetail{{"invalid_type", "/workflow", "Expected string, received number"}}},
		{"several issues in schema order", `{"repo":1,"action":"x","triggers":3}`, []issueDetail{
			{"invalid_type", "/repo", "Expected string, received number"},
			{"invalid_enum_value", "/action", "Invalid enum value. Expected 'open_pr' | 'files', received 'x'"},
			{"invalid_type", "/triggers", "Expected array, received number"},
		}},
		{"array body", `[]`, []issueDetail{{"invalid_type", "/", "Expected object, received array"}}},
		{"null body", `null`, []issueDetail{{"invalid_type", "/", "Expected object, received null"}}},
		{"string body", `"x"`, []issueDetail{{"invalid_type", "/", "Expected object, received string"}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := &fakeCI{}
			r := postJSON(t, newServer(t, f), "/agents/"+agentID+"/export-ci", tc.body)
			if diff := cmp.Diff(tc.want, validationIssues(t, r)); diff != "" {
				t.Errorf("issues mismatch (-want +got):\n%s", diff)
			}
			if calls := f.callList(); len(calls) != 0 {
				t.Errorf("use case called on invalid input: %v", calls)
			}
		})
	}
}

// The workflow cap of the schema (1,048,576 units) is above what the 1 MiB body limit lets
// through, so the biggest body that fits must reach the use case, which owns the 64 KiB rule.
func TestExport_LargestBodyReachesTheUseCase(t *testing.T) {
	t.Parallel()
	f := &fakeCI{}
	srv := newServer(t, f)
	ok := postJSON(t, srv, "/agents/"+agentID+"/export-ci", `{"repo":"a/b","workflow":"`+strings.Repeat("a", 1_048_576-64)+`"}`)
	if ok.Status != http.StatusCreated {
		t.Fatalf("a workflow under the cap answered %d: %.200s", ok.Status, ok.Body)
	}
	if f.exportReq.Workflow == nil || len(*f.exportReq.Workflow) != 1_048_576-64 {
		t.Errorf("workflow was not passed to the use case whole")
	}
}

func TestExport_PathIsCheckedBeforeBody(t *testing.T) {
	t.Parallel()
	f := &fakeCI{}
	r := postJSON(t, newServer(t, f), "/agents/not-a-uuid/export-ci", `{}`)
	want := []issueDetail{{"invalid_string", "/id", "Invalid uuid"}}
	if diff := cmp.Diff(want, validationIssues(t, r)); diff != "" {
		t.Errorf("issues mismatch (-want +got):\n%s", diff)
	}
}

func TestExport_BodyRules(t *testing.T) {
	t.Parallel()
	f := &fakeCI{}
	srv := newServer(t, f)
	path := "/agents/" + agentID + "/export-ci"

	// text/plain is never read as a command (CSRF-safe content type): it is a string, not an object.
	r := send(t, srv, http.MethodPost, path, "text/plain", `{"repo":"a/b"}`)
	if diff := cmp.Diff([]issueDetail{{"invalid_type", "/", "Expected object, received string"}}, validationIssues(t, r)); diff != "" {
		t.Errorf("text/plain issues mismatch (-want +got):\n%s", diff)
	}
	r = send(t, srv, http.MethodPost, path, "application/x-www-form-urlencoded", `repo=a/b`)
	if r.Status != http.StatusUnsupportedMediaType {
		t.Errorf("form body status = %d, want 415", r.Status)
	}
	r = send(t, srv, http.MethodPost, path, "", "")
	if diff := cmp.Diff([]issueDetail{{"invalid_type", "/", "Required"}}, validationIssues(t, r)); diff != "" {
		t.Errorf("no body issues mismatch (-want +got):\n%s", diff)
	}
	r = send(t, srv, http.MethodPost, path, "application/json", `{"repo":"a/b"}`, "Origin", "https://evil.example")
	if r.Status != http.StatusForbidden {
		t.Errorf("foreign origin status = %d (%s), want 403", r.Status, r.Body)
	}
	if calls := f.callList(); len(calls) != 0 {
		t.Errorf("use case called for rejected requests: %v", calls)
	}
}

func TestExport_UseCaseErrorsKeepTheirEnvelope(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		err     error
		status  int
		code    string
		message string
		details any
	}{
		{"agent", apperr.NotFound("Agent not found").WithCode("agent_not_found"), 404, "agent_not_found", "Agent not found", nil},
		{"slug", apperr.Conflict(`Another agent already uses the CI name "x" in this repository`).WithCode("ci_slug_conflict"), 409, "ci_slug_conflict", `Another agent already uses the CI name "x" in this repository`, nil},
		{"insecure", apperr.Validation("The workflow breaks a security rule").WithCode("workflow_insecure").WithDetails(map[string]any{"rule": "fork_guard"}), 422, "workflow_insecure", "The workflow breaks a security rule", map[string]any{"rule": "fork_guard"}},
		{"permission", apperr.Validation(domain.PermissionDeniedMessage).WithCode("github_permission_denied"), 422, "github_permission_denied", domain.PermissionDeniedMessage, nil},
		{"unavailable", apperr.ExternalService(domain.UnavailableMessage).WithCode("github_unavailable"), 502, "github_unavailable", domain.UnavailableMessage, nil},
		{"config", apperr.Config("GITHUB_TOKEN is not configured"), 500, "config_error", "GITHUB_TOKEN is not configured", nil},
		{"unexpected", io.ErrUnexpectedEOF, 500, "internal_error", "Internal error", nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			srv := newServer(t, &fakeCI{err: tc.err})
			r := postJSON(t, srv, "/agents/"+agentID+"/export-ci", `{"repo":"a/b"}`)
			e := assertError(t, r, tc.status, tc.code, tc.message)
			if diff := cmp.Diff(tc.details, e["details"]); diff != "" {
				t.Errorf("details mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestInstallations(t *testing.T) {
	t.Parallel()
	installed := time.Date(2026, 10, 19, 8, 0, 0, 0, time.UTC)
	f := &fakeCI{insts: []domain.Installation{
		{ID: instID, AgentID: agentID, Repo: "acme/widgets", TargetType: "gha", InstalledAt: installed, Slug: ptr("reviewer"), ManifestVersion: ptr(2), LastRun: ptr(sampleRun())},
		{ID: "old", AgentID: agentID, Repo: "acme/old", TargetType: "gha", InstalledAt: installed},
	}}
	srv := newServer(t, f)
	r := send(t, srv, http.MethodGet, "/agents/"+agentID+"/ci-installations", "", "")
	if r.Status != http.StatusOK {
		t.Fatalf("status = %d (%s), want 200", r.Status, r.Body)
	}
	want := map[string]any{"installations": []any{
		map[string]any{
			"id": instID, "agent_id": agentID, "repo": "acme/widgets", "target_type": "gha", "installed_at": "2026-10-19T08:00:00.000Z",
			"slug": "reviewer", "workflow_path": ".github/workflows/devdigest-review-reviewer.yml", "manifest_version": 2.0, "last_run": wantRunJSON(),
			"provider": "github", "host": "github.com",
		},
		map[string]any{
			"id": "old", "agent_id": agentID, "repo": "acme/old", "target_type": "gha", "installed_at": "2026-10-19T08:00:00.000Z",
			"slug": nil, "workflow_path": nil, "manifest_version": nil, "last_run": nil,
			"provider": "github", "host": "github.com",
		},
	}}
	if diff := cmp.Diff(want, decode(t, r)); diff != "" {
		t.Errorf("body mismatch (-want +got):\n%s", diff)
	}
	if f.exportAgent != agentID {
		t.Errorf("agent id = %q, want %q", f.exportAgent, agentID)
	}

	f.insts = nil
	if r := send(t, srv, http.MethodGet, "/agents/"+agentID+"/ci-installations", "", ""); strings.TrimSpace(r.Body) != `{"installations":[]}` {
		t.Errorf("empty body = %s, want {\"installations\":[]}", r.Body)
	}
	r = send(t, srv, http.MethodGet, "/agents/x/ci-installations", "", "")
	if diff := cmp.Diff([]issueDetail{{"invalid_string", "/id", "Invalid uuid"}}, validationIssues(t, r)); diff != "" {
		t.Errorf("issues mismatch (-want +got):\n%s", diff)
	}
	f.err = apperr.NotFound("Agent not found").WithCode("agent_not_found")
	assertError(t, send(t, srv, http.MethodGet, "/agents/"+agentID+"/ci-installations", "", ""), 404, "agent_not_found", "Agent not found")
}

func TestRuns_QueryAndShape(t *testing.T) {
	t.Parallel()
	failed := domain.Run{ID: "run-2", Status: ptr("failed"), GithubURL: ptr("https://github.com/acme/gone/actions/runs/1")}
	f := &fakeCI{runs: []domain.Run{sampleRun(), failed}}
	srv := newServer(t, f)

	r := send(t, srv, http.MethodGet, "/ci/runs", "", "")
	if r.Status != http.StatusOK {
		t.Fatalf("status = %d (%s), want 200", r.Status, r.Body)
	}
	wantFailed := map[string]any{
		"id": "run-2", "ci_installation_id": nil, "pr_number": nil, "ran_at": nil, "status": "failed", "findings_count": nil, "cost_usd": nil,
		"github_url": "https://github.com/acme/gone/actions/runs/1", "source": nil, "agent": nil, "duration_s": nil, "agent_id": nil,
		"repo": "acme/gone", "verdict": nil, "blockers": nil, "head_sha": nil, "manifest_version": nil,
		"provider": "github", "host": "github.com", "pr_url": nil,
	}
	if diff := cmp.Diff(map[string]any{"runs": []any{wantRunJSON(), wantFailed}}, decode(t, r)); diff != "" {
		t.Errorf("body mismatch (-want +got):\n%s", diff)
	}
	if f.runFilter != (app.RunFilter{Limit: 100}) {
		t.Errorf("default filter = %+v, want limit 100 and no agent", f.runFilter)
	}

	r = send(t, srv, http.MethodGet, "/ci/runs?agent_id="+agentID+"&limit=20&other=1", "", "")
	if r.Status != http.StatusOK || f.runFilter != (app.RunFilter{AgentID: agentID, Limit: 20}) {
		t.Errorf("filter = %+v (status %d), want agent and limit 20", f.runFilter, r.Status)
	}

	f.runs = nil
	if r := send(t, srv, http.MethodGet, "/ci/runs", "", ""); strings.TrimSpace(r.Body) != `{"runs":[]}` {
		t.Errorf("empty body = %s, want {\"runs\":[]}", r.Body)
	}
}

func TestRuns_LimitAndAgentValidation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		query     string
		wantLimit int
		want      []issueDetail
	}{
		{"1", "limit=1", 1, nil},
		{"200", "limit=200", 200, nil},
		{"exponent", "limit=1e2", 100, nil},
		{"hex", "limit=0x10", 16, nil},
		{"padded", "limit=%2050%20", 50, nil},
		{"zero", "limit=0", 0, []issueDetail{{"too_small", "/limit", "Number must be greater than or equal to 1"}}},
		{"empty is zero", "limit=", 0, []issueDetail{{"too_small", "/limit", "Number must be greater than or equal to 1"}}},
		{"201", "limit=201", 0, []issueDetail{{"too_big", "/limit", "Number must be less than or equal to 200"}}},
		{"negative", "limit=-5", 0, []issueDetail{{"too_small", "/limit", "Number must be greater than or equal to 1"}}},
		{"float", "limit=1.5", 0, []issueDetail{{"invalid_type", "/limit", "Expected integer, received float"}}},
		{"small float", "limit=0.5", 0, []issueDetail{
			{"invalid_type", "/limit", "Expected integer, received float"},
			{"too_small", "/limit", "Number must be greater than or equal to 1"}}},
		{"nan", "limit=abc", 0, []issueDetail{{"invalid_type", "/limit", "Expected number, received nan"}}},
		{"repeated", "limit=1&limit=2", 0, []issueDetail{{"invalid_type", "/limit", "Expected number, received nan"}}},
		{"infinity", "limit=Infinity", 0, []issueDetail{
			{"invalid_type", "/limit", "Expected integer, received float"},
			{"too_big", "/limit", "Number must be less than or equal to 200"}}},
		{"agent not uuid", "agent_id=x", 0, []issueDetail{{"invalid_string", "/agent_id", "Invalid uuid"}}},
		{"agent empty", "agent_id=", 0, []issueDetail{{"invalid_string", "/agent_id", "Invalid uuid"}}},
		{"agent repeated", "agent_id=a&agent_id=b", 0, []issueDetail{{"invalid_type", "/agent_id", "Expected string, received array"}}},
		{"both", "agent_id=x&limit=0", 0, []issueDetail{
			{"invalid_string", "/agent_id", "Invalid uuid"},
			{"too_small", "/limit", "Number must be greater than or equal to 1"}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := &fakeCI{}
			r := send(t, newServer(t, f), http.MethodGet, "/ci/runs?"+tc.query, "", "")
			if tc.want == nil {
				if r.Status != http.StatusOK || f.runFilter.Limit != tc.wantLimit {
					t.Errorf("GET /ci/runs?%s = %d (%s), limit %d; want 200, limit %d", tc.query, r.Status, r.Body, f.runFilter.Limit, tc.wantLimit)
				}
				return
			}
			if diff := cmp.Diff(tc.want, validationIssues(t, r)); diff != "" {
				t.Errorf("GET /ci/runs?%s issues mismatch (-want +got):\n%s", tc.query, diff)
			}
			if calls := f.callList(); len(calls) != 0 {
				t.Errorf("use case called on invalid query: %v", calls)
			}
		})
	}
}

func TestRefresh_Bodies(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		contentType string
		body        string
		wantAgent   string
	}{
		{"no body and no content type", "", "", ""},
		{"json null", "application/json", "null", ""},
		{"empty object", "application/json", "{}", ""},
		{"unknown keys", "application/json", `{"x":1}`, ""},
		{"agent", "application/json", `{"agent_id":"` + agentID + `"}`, agentID},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := &fakeCI{refreshFor: "unset"}
			r := send(t, newServer(t, f), http.MethodPost, "/ci/refresh", tc.contentType, tc.body)
			if r.Status != http.StatusOK {
				t.Fatalf("status = %d (%s), want 200", r.Status, r.Body)
			}
			if f.refreshFor != tc.wantAgent || f.refreshWs != workspaceID {
				t.Errorf("Refresh(ws %q, agent %q), want (%q, %q)", f.refreshWs, f.refreshFor, workspaceID, tc.wantAgent)
			}
		})
	}
}

func TestRefresh_ValidationIssues(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		contentType string
		body        string
		want        []issueDetail
	}{
		{"not uuid", "application/json", `{"agent_id":"x"}`, []issueDetail{{"invalid_string", "/agent_id", "Invalid uuid"}}},
		{"empty id", "application/json", `{"agent_id":""}`, []issueDetail{{"invalid_string", "/agent_id", "Invalid uuid"}}},
		{"number", "application/json", `{"agent_id":5}`, []issueDetail{{"invalid_type", "/agent_id", "Expected string, received number"}}},
		{"null id", "application/json", `{"agent_id":null}`, []issueDetail{{"invalid_type", "/agent_id", "Expected string, received null"}}},
		{"array body", "application/json", `[]`, []issueDetail{{"invalid_type", "/", "Expected object, received array"}}},
		{"text/plain body", "text/plain", `{}`, []issueDetail{{"invalid_type", "/", "Expected object, received string"}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := &fakeCI{}
			r := send(t, newServer(t, f), http.MethodPost, "/ci/refresh", tc.contentType, tc.body)
			if diff := cmp.Diff(tc.want, validationIssues(t, r)); diff != "" {
				t.Errorf("issues mismatch (-want +got):\n%s", diff)
			}
			if calls := f.callList(); len(calls) != 0 {
				t.Errorf("use case called on invalid body: %v", calls)
			}
		})
	}
}

func TestRefresh_ResultShape(t *testing.T) {
	t.Parallel()
	f := &fakeCI{refresh: domain.RefreshResult{
		Ingested: 2, Degraded: true,
		Installations: []domain.InstallationResult{
			{InstallationID: "i1", Repo: "acme/a", State: domain.StateOK, Ingested: 2, Skipped: []domain.SkippedRun{{GithubRunID: 9, Reason: domain.SkipExpired}}},
			{InstallationID: "i2", Repo: "acme/b", State: domain.StateNotInstalled},
			{InstallationID: "i3", Repo: "acme/c", State: domain.StateError, ErrorCode: domain.CodeRateLimited},
			{InstallationID: "i4", Repo: "acme/d", State: domain.StateNotChecked},
		},
	}}
	r := send(t, newServer(t, f), http.MethodPost, "/ci/refresh", "", "")
	want := map[string]any{"ingested": 2.0, "degraded": true, "installations": []any{
		map[string]any{"installation_id": "i1", "repo": "acme/a", "state": "ok", "ingested": 2.0, "skipped": []any{map[string]any{"github_run_id": 9.0, "reason": "artifact_expired"}}},
		map[string]any{"installation_id": "i2", "repo": "acme/b", "state": "not_installed", "ingested": 0.0, "skipped": []any{}},
		map[string]any{"installation_id": "i3", "repo": "acme/c", "state": "error", "error_code": "github_rate_limited", "ingested": 0.0, "skipped": []any{}},
		map[string]any{"installation_id": "i4", "repo": "acme/d", "state": "not_checked", "ingested": 0.0, "skipped": []any{}},
	}}
	if diff := cmp.Diff(want, decode(t, r)); diff != "" {
		t.Errorf("body mismatch (-want +got):\n%s", diff)
	}
	if strings.Contains(r.Body, `"error_code":""`) {
		t.Errorf("error_code must be absent unless state is error: %s", r.Body)
	}

	f.refresh = domain.RefreshResult{}
	if r := send(t, newServer(t, f), http.MethodPost, "/ci/refresh", "", ""); strings.TrimSpace(r.Body) != `{"ingested":0,"degraded":false,"installations":[]}` {
		t.Errorf("empty result = %s", r.Body)
	}
}

func TestRefresh_UseCaseErrors(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		err     error
		status  int
		code    string
		message string
	}{
		{"agent", apperr.NotFound("Agent not found").WithCode("agent_not_found"), 404, "agent_not_found", "Agent not found"},
		{"config", apperr.Config("GITHUB_TOKEN is not configured"), 500, "config_error", "GITHUB_TOKEN is not configured"},
		{"database", apperr.Internal("Internal error"), 500, "internal_error", "Internal error"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := send(t, newServer(t, &fakeCI{err: tc.err}), http.MethodPost, "/ci/refresh", "", "")
			assertError(t, r, tc.status, tc.code, tc.message)
		})
	}
}

func TestRefresh_ForeignOriginIs403(t *testing.T) {
	t.Parallel()
	f := &fakeCI{}
	r := send(t, newServer(t, f), http.MethodPost, "/ci/refresh", "", "", "Origin", "https://evil.example")
	if r.Status != http.StatusForbidden {
		t.Errorf("status = %d (%s), want 403", r.Status, r.Body)
	}
	if calls := f.callList(); len(calls) != 0 {
		t.Errorf("use case called for a foreign origin: %v", calls)
	}
}

func TestAddRoutes_RegistersFourRoutes(t *testing.T) {
	t.Parallel()
	e := echo.New()
	cihttp.AddRoutes(e, &fakeCI{}, &fakeCI{}, fakeTenants{})
	var got []string
	for _, r := range e.Router().Routes() {
		got = append(got, r.Method+" "+r.Path)
	}
	want := map[string]bool{
		"POST /agents/:id/export-ci": true, "GET /agents/:id/ci-installations": true, "GET /ci/runs": true, "POST /ci/refresh": true,
	}
	for _, g := range got {
		delete(want, g)
	}
	if len(want) != 0 {
		t.Errorf("routes %v are not registered; have %v", want, got)
	}
}

func TestRepoInstallations_Shape(t *testing.T) {
	t.Parallel()
	installed := time.Date(2026, 10, 19, 8, 0, 0, 0, time.UTC)
	ranAt := time.Date(2026, 10, 20, 9, 30, 0, 0, time.UTC)
	f := &fakeCI{groups: []domain.RepoGroup{
		{
			Repo: "acme/api",
			Installations: []domain.RepoInstallation{
				{ID: instID, AgentID: agentID, AgentName: "alpha", Repo: "acme/api", Slug: ptr("alpha"), InstalledAt: installed},
				{ID: "i2", AgentID: "a2", AgentName: "beta", Repo: "acme/api", InstalledAt: installed},
			},
			LastRun: &domain.Run{ID: "r1", Status: ptr("succeeded"), RanAt: &ranAt, GithubURL: ptr("https://github.com/acme/api/actions/runs/1"), AgentID: ptr(agentID), PRNumber: ptr(7)},
		},
		{
			Repo:          "acme/web",
			Installations: []domain.RepoInstallation{{ID: "i3", AgentID: agentID, AgentName: "alpha", Repo: "acme/web", InstalledAt: installed}},
		},
		{
			Repo:          "acme/bare",
			Installations: []domain.RepoInstallation{{ID: "i4", AgentID: agentID, AgentName: "alpha", Repo: "acme/bare", InstalledAt: installed}},
			LastRun:       &domain.Run{ID: "r2"},
		},
	}}
	srv := newServer(t, f)
	r := send(t, srv, http.MethodGet, "/ci/installations", "", "")
	if r.Status != http.StatusOK {
		t.Fatalf("status = %d (%s), want 200", r.Status, r.Body)
	}
	want := map[string]any{"repos": []any{
		map[string]any{
			"repo": "acme/api", "provider": "github", "host": "github.com", "target_type": "gha",
			"installations": []any{
				map[string]any{"id": instID, "agent_id": agentID, "agent_name": "alpha", "slug": "alpha", "installed_at": "2026-10-19T08:00:00.000Z"},
				map[string]any{"id": "i2", "agent_id": "a2", "agent_name": "beta", "slug": nil, "installed_at": "2026-10-19T08:00:00.000Z"},
			},
			"last_run": map[string]any{
				"status": "succeeded", "ran_at": "2026-10-20T09:30:00.000Z",
				"github_url": "https://github.com/acme/api/actions/runs/1", "agent_id": agentID,
				"pr_number": 7.0, "pr_url": nil,
			},
		},
		map[string]any{
			"repo": "acme/web", "provider": "github", "host": "github.com", "target_type": "gha",
			"installations": []any{
				map[string]any{"id": "i3", "agent_id": agentID, "agent_name": "alpha", "slug": nil, "installed_at": "2026-10-19T08:00:00.000Z"},
			},
			"last_run": nil,
		},
		map[string]any{
			"repo": "acme/bare", "provider": "github", "host": "github.com", "target_type": "gha",
			"installations": []any{
				map[string]any{"id": "i4", "agent_id": agentID, "agent_name": "alpha", "slug": nil, "installed_at": "2026-10-19T08:00:00.000Z"},
			},
			"last_run": map[string]any{"status": nil, "ran_at": nil, "github_url": nil, "agent_id": nil, "pr_number": nil, "pr_url": nil},
		},
	}}
	if diff := cmp.Diff(want, decode(t, r)); diff != "" {
		t.Errorf("body mismatch (-want +got):\n%s", diff)
	}
	if f.refreshWs != workspaceID {
		t.Errorf("workspace = %q, want %q", f.refreshWs, workspaceID)
	}
}

func TestRepoInstallations_EmptyIsAnEmptyList(t *testing.T) {
	t.Parallel()
	r := send(t, newServer(t, &fakeCI{}), http.MethodGet, "/ci/installations", "", "")
	if r.Status != http.StatusOK || strings.TrimSpace(r.Body) != `{"repos":[]}` {
		t.Errorf("empty answer = %d %s, want 200 {\"repos\":[]}", r.Status, r.Body)
	}
}

func TestRepoInstallations_DatabaseErrorIs500(t *testing.T) {
	t.Parallel()
	f := &fakeCI{err: errors.New("db down")}
	assertError(t, send(t, newServer(t, f), http.MethodGet, "/ci/installations", "", ""), 500, "internal_error", "Internal error")
}

func TestAddInstallationsRoute_DoesNotShadowTheAgentRoute(t *testing.T) {
	t.Parallel()
	e := echo.New()
	cihttp.AddRoutes(e, &fakeCI{}, &fakeCI{}, fakeTenants{})
	cihttp.AddInstallationsRoute(e, &fakeCI{}, fakeTenants{})
	have := map[string]bool{}
	for _, r := range e.Router().Routes() {
		have[r.Method+" "+r.Path] = true
	}
	for _, w := range []string{"GET /ci/installations", "GET /agents/:id/ci-installations"} {
		if !have[w] {
			t.Errorf("route %q is not registered; have %v", w, have)
		}
	}
}

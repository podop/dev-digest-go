package evalshttp_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/evals/app"
	"dev-digest-go/backend/internal/evals/domain"
	evalshttp "dev-digest-go/backend/internal/evals/transport/http"
	"dev-digest-go/backend/internal/platform/apperr"
	"dev-digest-go/backend/internal/platform/httpx"
	"dev-digest-go/backend/internal/tenancy"
)

const (
	workspaceID = "11111111-2222-4333-8444-555555555555"
	id1         = "7e1d0000-0000-4000-8000-000000000001"
	id2         = "7e1d0000-0000-4000-8000-000000000002"
)

// fake serves both use-case interfaces: it answers with the configured values and records the
// last call.
type fake struct {
	mu   sync.Mutex
	call string
	args []any
	err  error

	caseView  domain.CaseView
	caseViews []domain.CaseView
	run       domain.Run
	runs      []domain.Run
	detail    domain.RunDetail
	compare   domain.Compare
	runAll    domain.RunAllResult
	overview  domain.Overview
	dash      domain.AgentDashboard
}

var (
	_ app.Cases = (*fake)(nil)
	_ app.Runs  = (*fake)(nil)
)

func (f *fake) rec(name string, args ...any) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.call, f.args = name, args
	return f.err
}

func (f *fake) FromFinding(_ context.Context, ws, id string) (domain.CaseView, error) {
	return f.caseView, f.rec("FromFinding", ws, id)
}

func (f *fake) List(_ context.Context, ws string, k domain.OwnerKind, id string) ([]domain.CaseView, error) {
	return f.caseViews, f.rec("List", ws, k, id)
}

func (f *fake) Create(_ context.Context, ws string, k domain.OwnerKind, id string, in domain.CreateInput) (domain.CaseView, error) {
	return f.caseView, f.rec("Create", ws, k, id, in)
}

func (f *fake) Update(_ context.Context, ws, id string, in domain.UpdateInput) (domain.CaseView, error) {
	return f.caseView, f.rec("Update", ws, id, in)
}

func (f *fake) Delete(_ context.Context, ws, id string) error { return f.rec("Delete", ws, id) }

func (f *fake) StartAgent(_ context.Context, ws, id string) (domain.Run, error) {
	return f.run, f.rec("StartAgent", ws, id)
}

func (f *fake) StartSkill(_ context.Context, ws, id, host string) (domain.Run, error) {
	return f.run, f.rec("StartSkill", ws, id, host)
}

func (f *fake) RunAll(_ context.Context, ws string) (domain.RunAllResult, error) {
	return f.runAll, f.rec("RunAll", ws)
}

func (f *fake) ListAgentRuns(_ context.Context, ws, id string, limit int, p domain.Period) ([]domain.Run, error) {
	return f.runs, f.rec("ListAgentRuns", ws, id, limit, p)
}

func (f *fake) ListSkillRuns(_ context.Context, ws, id string, limit int) ([]domain.Run, error) {
	return f.runs, f.rec("ListSkillRuns", ws, id, limit)
}

func (f *fake) Detail(_ context.Context, ws, id string) (domain.RunDetail, error) {
	return f.detail, f.rec("Detail", ws, id)
}

func (f *fake) Compare(_ context.Context, ws, a, b string) (domain.Compare, error) {
	return f.compare, f.rec("Compare", ws, a, b)
}

func (f *fake) Overview(_ context.Context, ws string) (domain.Overview, error) {
	return f.overview, f.rec("Overview", ws)
}

func (f *fake) AgentDashboard(_ context.Context, ws, id string, p domain.Period) (domain.AgentDashboard, error) {
	return f.dash, f.rec("AgentDashboard", ws, id, p)
}

func (f *fake) FailInterrupted(context.Context) (int, error) { return 0, nil }

type fakeTenants struct{}

func (fakeTenants) Resolve(context.Context) (tenancy.Identity, error) {
	return tenancy.Identity{WorkspaceID: workspaceID}, nil
}

type reply struct {
	status int
	body   string
}

type request struct {
	method, path, contentType, body string
	header                          http.Header
}

// server starts the routes behind the API-wide middleware.
func server(t *testing.T, f *fake, rl httpx.RateLimit) *httptest.Server {
	t.Helper()
	e := httpx.New(httpx.Options{
		Log: slog.New(slog.DiscardHandler), AllowedHosts: []string{"127.0.0.1"}, RateLimit: rl,
	})
	evalshttp.AddRoutes(e, f, f, fakeTenants{})
	srv := httptest.NewServer(e)
	t.Cleanup(srv.Close)
	return srv
}

func send(t *testing.T, srv *httptest.Server, r request) reply {
	t.Helper()
	var body io.Reader
	if r.body != "" {
		body = strings.NewReader(r.body)
	}
	req, err := http.NewRequestWithContext(t.Context(), r.method, srv.URL+r.path, body)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if r.contentType != "" {
		req.Header.Set("Content-Type", r.contentType)
	}
	for k, v := range r.header {
		req.Header[k] = v
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", r.method, r.path, err)
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
	return reply{resp.StatusCode, strings.TrimSpace(string(b))}
}

// do sends one request to a fresh server.
func do(t *testing.T, f *fake, r request) reply {
	t.Helper()
	return send(t, server(t, f, httpx.RateLimit{}), r)
}

func get(path string) request  { return request{method: http.MethodGet, path: path} }
func post(path string) request { return request{method: http.MethodPost, path: path} }
func postJSON(path, body string) request {
	return request{method: http.MethodPost, path: path, contentType: "application/json", body: body}
}

func ptr[T any](v T) *T { return &v }

func decode(t *testing.T, s string) any {
	t.Helper()
	var v any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, s)
	}
	return v
}

// wantJSON compares the body with a JSON text as values (not as bytes).
func wantJSON(t *testing.T, got reply, status int, want string) {
	t.Helper()
	if got.status != status {
		t.Fatalf("status = %d (%s), want %d", got.status, got.body, status)
	}
	if diff := cmp.Diff(decode(t, want), decode(t, got.body)); diff != "" {
		t.Errorf("body mismatch (-want +got):\n%s", diff)
	}
}

var (
	t0 = time.Date(2026, 3, 4, 5, 6, 7, 8_000_000, time.UTC)
	t1 = t0.Add(90 * time.Second)
)

func sampleCase() domain.CaseView {
	return domain.CaseView{
		Case: domain.Case{
			ID: id1, WorkspaceID: workspaceID, OwnerKind: domain.OwnerAgent, OwnerID: id2, Name: "Case A",
			Expectation: domain.Expectation{
				Kind: domain.MustFind, File: "src/a.ts", StartLine: 9, EndLine: 10, Severity: ptr("WARNING"), Title: ptr("T"),
			},
			InputDiff:       "diff --git a/src/a.ts b/src/a.ts\n",
			InputMeta:       domain.InputMeta{PRTitle: "PR", Repo: ptr("o/r"), PRNumber: ptr(7), HeadSHA: ptr("abc")},
			SourceFindingID: ptr("f1"), SourcePRID: ptr("p1"),
			ContentEditedAt: &t1, CreatedAt: t0,
		},
		LastResult: &domain.LastResult{RunID: id2, Status: domain.CasePassed, RanAt: t1},
	}
}

const sampleCaseJSON = `{"id":"` + id1 + `","owner_kind":"agent","owner_id":"` + id2 + `","agent_id":"` + id2 + `","name":"Case A","notes":null,` +
	`"expectation":{"kind":"must_find","file":"src/a.ts","start_line":9,"end_line":10,"severity":"WARNING","category":null,"title":"T"},` +
	`"input_diff":"diff --git a/src/a.ts b/src/a.ts\n",` +
	`"input_meta":{"pr_title":"PR","pr_description":null,"repo":"o/r","pr_number":7,"head_sha":"abc"},` +
	`"source_finding_id":"f1","source_pr_id":"p1","origin":"finding","content_edited_at":"2026-03-04T05:07:37.008Z",` +
	`"created_at":"2026-03-04T05:06:07.008Z","last_result":{"run_id":"` + id2 + `","status":"passed","ran_at":"2026-03-04T05:07:37.008Z"}}`

func sampleRun() domain.Run {
	return domain.Run{
		ID: id1, OwnerKind: domain.OwnerSkill, SkillID: ptr(id2), SkillVersion: ptr(3), AgentID: id2, AgentVersion: 4,
		AgentName: "Agent", Config: domain.Config{Provider: "openai", Model: "gpt-x"}, Status: domain.RunCompleted,
		StartedAt: t0, FinishedAt: &t1, CasesTotal: 3, CasesFinished: 3, CasesPassed: 2, CasesErrored: 1,
		Metrics:    domain.Metrics{Recall: ptr(0.5), Precision: ptr(1.0), UnlabeledFindings: 2, CostUSD: ptr(0.000123)},
		DurationMS: ptr(90000), Error: ptr("boom"),
	}
}

const sampleRunJSON = `{"id":"` + id1 + `","owner_kind":"skill","skill_id":"` + id2 + `","skill_version":3,"agent_id":"` + id2 + `",` +
	`"agent_version":4,"agent_name":"Agent","model":"gpt-x","status":"completed","started_at":"2026-03-04T05:06:07.008Z",` +
	`"finished_at":"2026-03-04T05:07:37.008Z","cases_total":3,"cases_finished":3,"cases_passed":2,"cases_errored":1,` +
	`"recall":0.5,"precision":1,"citation_accuracy":null,"unlabeled_findings":2,"cost_usd":0.000123,"duration_ms":90000,"error":"boom"}`

func runningRun() domain.Run {
	return domain.Run{
		ID: id1, OwnerKind: domain.OwnerAgent, AgentID: id2, AgentVersion: 1, AgentName: "A", Config: domain.Config{Model: "m"},
		Status: domain.RunRunning, StartedAt: t0, CasesTotal: 2,
	}
}

const runningRunJSON = `{"id":"` + id1 + `","owner_kind":"agent","skill_id":null,"skill_version":null,"agent_id":"` + id2 + `",` +
	`"agent_version":1,"agent_name":"A","model":"m","status":"running","started_at":"2026-03-04T05:06:07.008Z","finished_at":null,` +
	`"cases_total":2,"cases_finished":0,"cases_passed":0,"cases_errored":0,"recall":null,"precision":null,"citation_accuracy":null,` +
	`"unlabeled_findings":0,"cost_usd":null,"duration_ms":null,"error":null}`

const validCreateBody = `{"name":"Case A","expectation":{"kind":"must_find","file":"src/a.ts","start_line":9,"end_line":10},"input_diff":"@@ -1 +1 @@"}`

func TestRoutes_StatusAndWireShape(t *testing.T) {
	exp := domain.Expectation{Kind: domain.MustNotFlag, File: "f.go", StartLine: 1, EndLine: 2, Category: ptr("bug")}
	detail := domain.RunDetail{Run: sampleRun(), Results: []domain.CaseResult{
		{CaseID: id1, CaseName: "C", Expectation: exp, Status: domain.CaseFailed, DurationMS: 12,
			Findings: []domain.ResultFinding{{File: "f.go", StartLine: 1, EndLine: 1, Severity: ptr("WARNING"), Match: domain.MatchViolation}}},
		{CaseID: id2, CaseName: "D", Expectation: exp, Status: domain.CaseError, Error: ptr("x"), CostUSD: ptr(0.5)},
	}}
	cmpr := domain.Compare{
		A: sampleRun(), B: runningRun(),
		Delta: domain.Delta{Recall: ptr(-0.5)},
		Cases: []domain.CompareCase{{CaseID: id1, CaseName: "C", A: domain.CasePassed, B: domain.CaseAbsent, Flipped: true}},
		ConfigDiff: domain.ConfigDiff{
			SystemPrompt: []domain.DiffLine{{Op: domain.OpSame, Line: "x"}, {Op: domain.OpAdd, Line: "y"}},
			Model:        &domain.ValueChange{A: "m1", B: "m2"}, SkillsAdded: []string{"s"},
		},
	}
	overview := domain.Overview{
		Agents: []domain.AgentOverview{
			{AgentRef: domain.AgentRef{ID: id2, Name: "Agent", Model: "gpt-x"}, CasesTotal: 3, Latest: ptr(sampleRun()),
				Sparkline: []domain.SparkPoint{{RunID: id1, Recall: ptr(1.0)}}},
			{AgentRef: domain.AgentRef{ID: id1, Name: "Empty", Model: "m"}},
		},
		RecentRuns: []domain.RecentRun{{Run: runningRun(), AgentName: "Renamed"}},
	}
	dash := domain.AgentDashboard{
		AgentID: id2, CasesTotal: 3, Latest: ptr(sampleRun()), Trend: []domain.Run{sampleRun()},
		Alert: []domain.MetricDelta{{Metric: domain.MetricRecall, Delta: -0.1}},
	}
	runAll := domain.RunAllResult{
		Started: []domain.Run{runningRun()},
		Skipped: []domain.Skip{{AgentID: id1, Reason: domain.SkipNoCases}, {AgentID: id2, Reason: domain.SkipRunActive}},
	}

	tests := []struct {
		name   string
		req    request
		status int
		want   string
		call   string
	}{
		{"case from finding", post("/findings/" + id1 + "/eval-case"), 201, sampleCaseJSON, "FromFinding"},
		{"agent cases", get("/agents/" + id1 + "/eval-cases"), 200, `{"cases":[` + sampleCaseJSON + `]}`, "List"},
		{"create agent case", postJSON("/agents/"+id1+"/eval-cases", validCreateBody), 201, sampleCaseJSON, "Create"},
		{"skill cases", get("/skills/" + id1 + "/eval-cases"), 200, `{"cases":[` + sampleCaseJSON + `]}`, "List"},
		{"create skill case", postJSON("/skills/"+id1+"/eval-cases", validCreateBody), 201, sampleCaseJSON, "Create"},
		{"update case", request{method: http.MethodPatch, path: "/eval-cases/" + id1, contentType: "application/json", body: `{}`}, 200, sampleCaseJSON, "Update"},
		{"start agent run", post("/agents/" + id1 + "/eval-runs"), 202, runningRunJSON, "StartAgent"},
		{"agent runs", get("/agents/" + id1 + "/eval-runs"), 200, `{"runs":[` + runningRunJSON + `]}`, "ListAgentRuns"},
		{"start skill run", postJSON("/skills/"+id1+"/eval-runs", `{"agent_id":"`+id2+`"}`), 202, runningRunJSON, "StartSkill"},
		{"skill runs", get("/skills/" + id1 + "/eval-runs"), 200, `{"runs":[` + runningRunJSON + `]}`, "ListSkillRuns"},
		{"compare", get("/eval-runs/compare?a=" + id1 + "&b=" + id2), 200,
			`{"a":` + sampleRunJSON + `,"b":` + runningRunJSON + `,"delta":{"recall":-0.5,"precision":null,"citation_accuracy":null,"cost_usd":null},` +
				`"cases":[{"case_id":"` + id1 + `","case_name":"C","a":"passed","b":"absent","flipped":true}],` +
				`"config_diff":{"system_prompt":[{"op":"same","line":"x"},{"op":"add","line":"y"}],"model":{"a":"m1","b":"m2"},"strategy":null,` +
				`"skills_added":["s"],"skills_removed":[]}}`, "Compare"},
		{"run detail", get("/eval-runs/" + id1), 200,
			`{"run":` + sampleRunJSON + `,"results":[` +
				`{"case_id":"` + id1 + `","case_name":"C","expectation":{"kind":"must_not_flag","file":"f.go","start_line":1,"end_line":2,"severity":null,"category":"bug","title":null},` +
				`"status":"failed","findings":[{"file":"f.go","start_line":1,"end_line":1,"severity":"WARNING","category":null,"title":null,"match":"violation"}],` +
				`"dropped_count":0,"duration_ms":12,"cost_usd":null,"error":null},` +
				`{"case_id":"` + id2 + `","case_name":"D","expectation":{"kind":"must_not_flag","file":"f.go","start_line":1,"end_line":2,"severity":null,"category":"bug","title":null},` +
				`"status":"error","findings":[],"dropped_count":0,"duration_ms":0,"cost_usd":0.5,"error":"x"}]}`, "Detail"},
		{"run-all", post("/eval-runs/run-all"), 202,
			`{"started":[` + runningRunJSON + `],"skipped":[{"agent_id":"` + id1 + `","reason":"no_cases"},{"agent_id":"` + id2 + `","reason":"run_active"}]}`, "RunAll"},
		{"overview", get("/eval/dashboard"), 200,
			`{"agents":[{"agent_id":"` + id2 + `","name":"Agent","model":"gpt-x","cases_total":3,"latest":` + sampleRunJSON +
				`,"sparkline":[{"run_id":"` + id1 + `","recall":1,"precision":null,"citation_accuracy":null}]},` +
				`{"agent_id":"` + id1 + `","name":"Empty","model":"m","cases_total":0,"latest":null,"sparkline":[]}],` +
				`"recent_runs":[` + strings.Replace(runningRunJSON, `"agent_name":"A"`, `"agent_name":"Renamed"`, 1) + `]}`, "Overview"},
		{"agent dashboard", get("/agents/" + id2 + "/eval/dashboard"), 200,
			`{"agent_id":"` + id2 + `","cases_total":3,"latest":` + sampleRunJSON + `,"previous":null,"trend":[` + sampleRunJSON +
				`],"alert":{"metrics":[{"metric":"recall","delta":-0.1}]}}`, "AgentDashboard"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := &fake{
				caseView: sampleCase(), caseViews: []domain.CaseView{sampleCase()}, run: runningRun(), runs: []domain.Run{runningRun()},
				detail: detail, compare: cmpr, runAll: runAll, overview: overview, dash: dash,
			}
			got := do(t, f, tc.req)
			wantJSON(t, got, tc.status, tc.want)
			if f.call != tc.call {
				t.Errorf("use case called = %q, want %q", f.call, tc.call)
			}
			if len(f.args) == 0 || f.args[0] != workspaceID {
				t.Errorf("%s args = %v, want the workspace first", f.call, f.args)
			}
		})
	}
}

func TestDelete_NoContent(t *testing.T) {
	f := &fake{}
	got := do(t, f, request{method: http.MethodDelete, path: "/eval-cases/" + id1})
	if got.status != http.StatusNoContent || got.body != "" {
		t.Errorf("DELETE = %d %q, want 204 with an empty body", got.status, got.body)
	}
	if f.call != "Delete" || f.args[1] != id1 {
		t.Errorf("call = %s %v", f.call, f.args)
	}
}

func TestEmptyListsAreArrays(t *testing.T) {
	f := &fake{}
	wantJSON(t, do(t, f, get("/agents/"+id1+"/eval-cases")), 200, `{"cases":[]}`)
	wantJSON(t, do(t, f, get("/skills/"+id1+"/eval-runs")), 200, `{"runs":[]}`)
	wantJSON(t, do(t, f, get("/eval/dashboard")), 200, `{"agents":[],"recent_runs":[]}`)
	wantJSON(t, do(t, f, post("/eval-runs/run-all")), 202, `{"started":[],"skipped":[]}`)
}

func TestPathIDs_MustBeUUIDs(t *testing.T) {
	tests := []request{
		post("/findings/x/eval-case"),
		get("/agents/x/eval-cases"),
		postJSON("/agents/x/eval-cases", validCreateBody),
		get("/skills/x/eval-cases"),
		postJSON("/skills/x/eval-cases", validCreateBody),
		{method: http.MethodPatch, path: "/eval-cases/x", contentType: "application/json", body: `{}`},
		{method: http.MethodDelete, path: "/eval-cases/x"},
		post("/agents/x/eval-runs"),
		get("/agents/x/eval-runs"),
		postJSON("/skills/x/eval-runs", `{"agent_id":"`+id2+`"}`),
		get("/skills/x/eval-runs"),
		get("/eval-runs/x"),
		get("/agents/x/eval/dashboard"),
	}
	for _, req := range tests {
		t.Run(req.method+" "+req.path, func(t *testing.T) {
			f := &fake{}
			got := do(t, f, req)
			want := `{"error":{"code":"validation_error","message":"Request validation failed","details":[{"keyword":"invalid_string",` +
				`"instancePath":"/id","schemaPath":"#/id/invalid_string","message":"Invalid uuid",` +
				`"params":{"issue":{"code":"invalid_string","path":["id"],"message":"Invalid uuid"}}}]}}`
			wantJSON(t, got, 422, want)
			if f.call != "" {
				t.Errorf("use case %s called for an invalid id", f.call)
			}
		})
	}
}

func TestErrors_Envelope(t *testing.T) {
	tests := []struct {
		name   string
		err    error
		status int
		want   string
	}{
		{"not found", apperr.NotFound("Agent not found"), 404, `{"error":{"code":"not_found","message":"Agent not found"}}`},
		{"case exists", apperr.Conflict("An eval case already exists for this finding").WithCode("eval_case_exists").WithDetails(map[string]any{"case_id": id2}), 409,
			`{"error":{"code":"eval_case_exists","message":"An eval case already exists for this finding","details":{"case_id":"` + id2 + `"}}}`},
		{"unprocessable rule", &domain.RuleError{Kind: domain.RuleUnprocessable, Code: domain.CodeDiffInvalid, Message: "The diff has no file with a hunk (@@ header)"}, 422,
			`{"error":{"code":"diff_invalid","message":"The diff has no file with a hunk (@@ header)"}}`},
		{"bad request rule", &domain.RuleError{Kind: domain.RuleBadRequest, Code: domain.CodeValidationError, Message: "end_line must be >= start_line"}, 400,
			`{"error":{"code":"validation_error","message":"end_line must be >= start_line"}}`},
		{"files detail", &domain.RuleError{Kind: domain.RuleUnprocessable, Code: domain.CodeFileNotInDiff, Message: "m", Details: map[string]any{"files": []string{"a", "b"}}}, 422,
			`{"error":{"code":"expectation_file_not_in_diff","message":"m","details":{"files":["a","b"]}}}`},
		{"ranges detail from the rule", &domain.RuleError{Kind: domain.RuleUnprocessable, Code: domain.CodeLinesNotInDiff, Message: "m",
			Details: map[string]any{"ranges": []domain.LineRange{{Start: 1, End: 3}, {Start: 9, End: 9}}}}, 422,
			`{"error":{"code":"expectation_lines_not_in_diff","message":"m","details":{"ranges":[{"start":1,"end":3},{"start":9,"end":9}]}}}`},
		{"ranges detail from an application error", apperr.Validation("m").WithCode(domain.CodeLinesNotInDiff).
			WithDetails(map[string]any{"ranges": []domain.LineRange{{Start: 4, End: 5}}}), 422,
			`{"error":{"code":"expectation_lines_not_in_diff","message":"m","details":{"ranges":[{"start":4,"end":5}]}}}`},
		{"unexpected error", io.ErrUnexpectedEOF, 500, `{"error":{"code":"internal_error","message":"Internal error"}}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := do(t, &fake{err: tc.err}, postJSON("/agents/"+id1+"/eval-cases", validCreateBody))
			wantJSON(t, got, tc.status, tc.want)
		})
	}
}

func TestErrors_ReachEveryRoute(t *testing.T) {
	reqs := []request{
		post("/findings/" + id1 + "/eval-case"), get("/agents/" + id1 + "/eval-cases"), get("/skills/" + id1 + "/eval-cases"),
		{method: http.MethodPatch, path: "/eval-cases/" + id1, contentType: "application/json", body: `{}`},
		{method: http.MethodDelete, path: "/eval-cases/" + id1},
		post("/agents/" + id1 + "/eval-runs"), get("/agents/" + id1 + "/eval-runs"),
		postJSON("/skills/"+id1+"/eval-runs", `{"agent_id":"`+id2+`"}`), get("/skills/" + id1 + "/eval-runs"),
		get("/eval-runs/compare?a=x&b=y"), get("/eval-runs/" + id1), post("/eval-runs/run-all"),
		get("/eval/dashboard"), get("/agents/" + id1 + "/eval/dashboard"),
	}
	for _, req := range reqs {
		t.Run(req.method+" "+req.path, func(t *testing.T) {
			got := do(t, &fake{err: apperr.NotFound("Nope")}, req)
			wantJSON(t, got, 404, `{"error":{"code":"not_found","message":"Nope"}}`)
		})
	}
}

func TestQueries_ReachTheUseCases(t *testing.T) {
	tests := []struct {
		name string
		path string
		call string
		args []any
	}{
		{"agent runs defaults", "/agents/" + id1 + "/eval-runs", "ListAgentRuns", []any{workspaceID, id1, 20, domain.PeriodNone}},
		{"agent runs limit and period", "/agents/" + id1 + "/eval-runs?limit=100&period=30d", "ListAgentRuns", []any{workspaceID, id1, 100, domain.Period30d}},
		{"agent runs JS number", "/agents/" + id1 + "/eval-runs?limit=%201e1%20&period=all", "ListAgentRuns", []any{workspaceID, id1, 10, domain.PeriodAll}},
		{"skill runs ignore the period", "/skills/" + id1 + "/eval-runs?limit=3&period=7d", "ListSkillRuns", []any{workspaceID, id1, 3}},
		{"compare passes any text", "/eval-runs/compare?a=not-a-uuid&b=%20", "Compare", []any{workspaceID, "not-a-uuid", " "}},
		{"dashboard period", "/agents/" + id1 + "/eval/dashboard?period=90d", "AgentDashboard", []any{workspaceID, id1, domain.Period90d}},
		{"dashboard no period", "/agents/" + id1 + "/eval/dashboard", "AgentDashboard", []any{workspaceID, id1, domain.PeriodNone}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := &fake{}
			if got := do(t, f, get(tc.path)); got.status != 200 {
				t.Fatalf("GET %s = %d %s, want 200", tc.path, got.status, got.body)
			}
			if f.call != tc.call {
				t.Errorf("call = %s, want %s", f.call, tc.call)
			}
			if diff := cmp.Diff(tc.args, f.args); diff != "" {
				t.Errorf("%s args mismatch (-want +got):\n%s", tc.call, diff)
			}
		})
	}
}

func TestCompare_IsNotTheRunDetail(t *testing.T) {
	f := &fake{}
	do(t, f, get("/eval-runs/compare?a=x&b=y"))
	if f.call != "Compare" {
		t.Errorf("GET /eval-runs/compare called %q, want Compare", f.call)
	}
	// "run-all" is a POST: a GET on it is the detail route and its id is not a UUID.
	if got := do(t, &fake{}, get("/eval-runs/run-all")); got.status != 422 {
		t.Errorf("GET /eval-runs/run-all = %d, want 422 (detail route, invalid id)", got.status)
	}
}

func TestBodies_Passed(t *testing.T) {
	f := &fake{}
	body := `{"name":"  Case A  ","notes":"why","expectation":{"kind":"must_not_flag","file":"src/a.ts","start_line":9,"end_line":12,` +
		`"severity":"CRITICAL","category":"security","title":"Hard-coded key"},"input_diff":"@@ -1 +1 @@","input_meta":{"pr_title":"T","pr_description":null}}`
	if got := do(t, f, postJSON("/skills/"+id1+"/eval-cases", body)); got.status != 201 {
		t.Fatalf("POST = %d %s", got.status, got.body)
	}
	want := domain.CreateInput{
		Name: "Case A", Notes: ptr("why"),
		Expectation: domain.Expectation{Kind: domain.MustNotFlag, File: "src/a.ts", StartLine: 9, EndLine: 12,
			Severity: ptr("CRITICAL"), Category: ptr("security"), Title: ptr("Hard-coded key")},
		InputDiff: "@@ -1 +1 @@", PRTitle: ptr("T"),
	}
	wantArgs := []any{workspaceID, domain.OwnerSkill, id1, want}
	if diff := cmp.Diff(wantArgs, f.args); diff != "" {
		t.Errorf("Create args mismatch (-want +got):\n%s", diff)
	}

	patch := `{"name":" N ","notes":null,"kind":"must_find","file":"b.go","start_line":2,"end_line":3,"severity":null,"category":"bug",` +
		`"title":"x","input_diff":"d","pr_title":null,"pr_description":"p"}`
	f = &fake{}
	req := request{method: http.MethodPatch, path: "/eval-cases/" + id1, contentType: "application/json", body: patch}
	if got := do(t, f, req); got.status != 200 {
		t.Fatalf("PATCH = %d %s", got.status, got.body)
	}
	kind := domain.MustFind
	wantUpdate := domain.UpdateInput{
		Name: ptr("N"), Notes: domain.Field[string]{Set: true}, Kind: &kind, File: ptr("b.go"), StartLine: ptr(2), EndLine: ptr(3),
		Severity: domain.Field[string]{Set: true}, Category: domain.Field[string]{Set: true, Value: ptr("bug")},
		Title: domain.Field[string]{Set: true, Value: ptr("x")}, InputDiff: ptr("d"),
		PRTitle: domain.Field[string]{Set: true}, PRDescription: domain.Field[string]{Set: true, Value: ptr("p")},
	}
	if diff := cmp.Diff([]any{workspaceID, id1, wantUpdate}, f.args); diff != "" {
		t.Errorf("Update args mismatch (-want +got):\n%s", diff)
	}

	f = &fake{}
	do(t, f, postJSON("/skills/"+id1+"/eval-runs", `{"agent_id":"`+strings.ToUpper(id2)+`","extra":1}`))
	if diff := cmp.Diff([]any{workspaceID, id1, strings.ToUpper(id2)}, f.args); diff != "" {
		t.Errorf("StartSkill args mismatch (-want +got):\n%s", diff)
	}
}

func issuesOf(t *testing.T, r reply) []string {
	t.Helper()
	var env struct {
		Error struct {
			Code    string `json:"code"`
			Details []struct {
				Params struct {
					Issue struct {
						Code    string `json:"code"`
						Path    []any  `json:"path"`
						Message string `json:"message"`
					} `json:"issue"`
				} `json:"params"`
			} `json:"details"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(r.body), &env); err != nil {
		t.Fatalf("not an envelope: %v\n%s", err, r.body)
	}
	if r.status != 422 || env.Error.Code != "validation_error" {
		t.Fatalf("answer = %d %s, want 422 validation_error", r.status, r.body)
	}
	var out []string
	for _, d := range env.Error.Details {
		i := d.Params.Issue
		p, _ := json.Marshal(i.Path)
		out = append(out, i.Code+" "+string(p)+" "+i.Message)
	}
	return out
}

func TestBodies_Rejected(t *testing.T) {
	tests := []struct {
		name string
		req  request
		want []string
	}{
		{"no body", request{method: http.MethodPost, path: "/agents/" + id1 + "/eval-cases"}, []string{"invalid_type [] Required"}},
		{"text body", request{method: http.MethodPost, path: "/agents/" + id1 + "/eval-cases", contentType: "text/plain", body: "hello"},
			[]string{"invalid_type [] Expected object, received string"}},
		{"patch without body", request{method: http.MethodPatch, path: "/eval-cases/" + id1}, []string{"invalid_type [] Required"}},
		{"skill run without body", post("/skills/" + id1 + "/eval-runs"), []string{"invalid_type [] Required"}},
		{"skill run text body", request{method: http.MethodPost, path: "/skills/" + id1 + "/eval-runs", contentType: "text/plain", body: "x"},
			[]string{"invalid_type [] Expected object, received string"}},
		{"refine is skipped on an aborted object", postJSON("/agents/"+id1+"/eval-cases",
			`{"name":"n","expectation":{"kind":"must_find","file":"f","start_line":5,"end_line":1,"severity":"HIGH"},"input_diff":"d"}`),
			[]string{"invalid_enum_value [\"expectation\",\"severity\"] Invalid enum value. Expected 'CRITICAL' | 'WARNING' | 'SUGGESTION', received 'HIGH'"}},
		{"refine runs on a dirty object", postJSON("/agents/"+id1+"/eval-cases",
			`{"name":"n","expectation":{"kind":"must_find","file":"f","start_line":0,"end_line":-1},"input_diff":"d"}`),
			[]string{
				"too_small [\"expectation\",\"start_line\"] Number must be greater than or equal to 1",
				"too_small [\"expectation\",\"end_line\"] Number must be greater than or equal to 1",
				"custom [\"expectation\",\"end_line\"] end_line must be >= start_line",
			}},
		{"fraction below one is two issues", postJSON("/agents/"+id1+"/eval-cases",
			`{"name":"n","expectation":{"kind":"must_find","file":"f","start_line":0.5,"end_line":1},"input_diff":"d"}`),
			[]string{
				"invalid_type [\"expectation\",\"start_line\"] Expected integer, received float",
				"too_small [\"expectation\",\"start_line\"] Number must be greater than or equal to 1",
			}},
		{"pr_description limit", postJSON("/agents/"+id1+"/eval-cases",
			`{"name":"n","expectation":{"kind":"must_find","file":"f","start_line":1,"end_line":1},"input_diff":"d","input_meta":{"pr_description":"`+strings.Repeat("p", 20001)+`"}}`),
			[]string{"too_big [\"input_meta\",\"pr_description\"] String must contain at most 20000 character(s)"}},
		{"patch null name", request{method: http.MethodPatch, path: "/eval-cases/" + id1, contentType: "application/json", body: `{"name":null,"input_diff":""}`},
			[]string{
				"invalid_type [\"name\"] Expected string, received null",
				"too_small [\"input_diff\"] String must contain at least 1 character(s)",
			}},
		{"patch kind number", request{method: http.MethodPatch, path: "/eval-cases/" + id1, contentType: "application/json", body: `{"kind":5,"start_line":"1"}`},
			[]string{
				"invalid_type [\"kind\"] Expected 'must_find' | 'must_not_flag', received number",
				"invalid_type [\"start_line\"] Expected number, received string",
			}},
		{"limit out of range", get("/agents/" + id1 + "/eval-runs?limit=101&period=1d"),
			[]string{
				"too_big [\"limit\"] Number must be less than or equal to 100",
				"invalid_enum_value [\"period\"] Invalid enum value. Expected '7d' | '30d' | '90d' | 'all', received '1d'",
			}},
		{"limit repeated", get("/agents/" + id1 + "/eval-runs?limit=1&limit=2&period=7d&period=30d"),
			[]string{
				"invalid_type [\"limit\"] Expected number, received nan",
				"invalid_type [\"period\"] Expected '7d' | '30d' | '90d' | 'all', received array",
			}},
		{"compare missing", get("/eval-runs/compare"), []string{"invalid_type [\"a\"] Required", "invalid_type [\"b\"] Required"}},
		{"compare repeated and empty", get("/eval-runs/compare?a=1&a=2&b="),
			[]string{"invalid_type [\"a\"] Expected string, received array", "too_small [\"b\"] String must contain at least 1 character(s)"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := &fake{}
			got := issuesOf(t, do(t, f, tc.req))
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("issues mismatch (-want +got):\n%s", diff)
			}
			if f.call != "" {
				t.Errorf("use case %s called for an invalid request", f.call)
			}
		})
	}
}

func TestBodylessPosts_IgnoreWhateverIsSent(t *testing.T) {
	paths := []string{"/findings/" + id1 + "/eval-case", "/agents/" + id1 + "/eval-runs", "/eval-runs/run-all"}
	for _, p := range paths {
		for _, ct := range []struct{ contentType, body string }{{"", ""}, {"application/json", `{"x":1}`}, {"text/plain", "garbage"}} {
			f := &fake{caseView: sampleCase(), run: runningRun()}
			got := do(t, f, request{method: http.MethodPost, path: p, contentType: ct.contentType, body: ct.body})
			if got.status != 201 && got.status != 202 {
				t.Errorf("POST %s (%q %q) = %d %s, want 201/202", p, ct.contentType, ct.body, got.status, got.body)
			}
		}
	}
}

func TestCSRFGuardsApply(t *testing.T) {
	f := &fake{}
	srv := server(t, f, httpx.RateLimit{})
	got := send(t, srv, request{method: http.MethodPost, path: "/eval-runs/run-all", header: http.Header{"Origin": {"https://evil.example"}}})
	if got.status != http.StatusForbidden || f.call != "" {
		t.Errorf("POST with a foreign Origin = %d %s (call %q), want 403 and no call", got.status, got.body, f.call)
	}
	got = send(t, srv, request{method: http.MethodPost, path: "/agents/" + id1 + "/eval-cases", contentType: "application/xml", body: "<a/>"})
	if got.status != http.StatusUnsupportedMediaType {
		t.Errorf("POST xml = %d %s, want 415", got.status, got.body)
	}
}

func TestRateLimitBuckets(t *testing.T) {
	buckets := evalshttp.RateLimitBuckets()
	want := map[string]int{"POST " + evalshttp.AgentRunsPath: 10, "POST " + evalshttp.SkillRunsPath: 10, "POST " + evalshttp.RunAllPath: 10}
	if len(buckets) != len(want) {
		t.Fatalf("RateLimitBuckets() = %d buckets, want %d", len(buckets), len(want))
	}
	for _, b := range buckets {
		if want[b.Method+" "+b.Path] != b.Limit {
			t.Errorf("bucket %s %s limit = %d", b.Method, b.Path, b.Limit)
		}
	}

	f := &fake{run: runningRun()}
	srv := server(t, f, httpx.RateLimit{Enabled: true, Limit: 1000, Window: time.Minute, Buckets: buckets})
	skillBody := `{"agent_id":"` + id2 + `"}`
	// Six agent starts and four skill starts spend the ONE shared budget of ten.
	for i := range 10 {
		var req request
		if i < 6 {
			req = post("/agents/" + id1 + "/eval-runs")
		} else {
			req = postJSON("/skills/"+id1+"/eval-runs", skillBody)
		}
		if got := send(t, srv, req); got.status != 202 {
			t.Fatalf("start %d = %d %s, want 202", i+1, got.status, got.body)
		}
	}
	for _, req := range []request{post("/agents/" + id1 + "/eval-runs"), postJSON("/skills/"+id1+"/eval-runs", skillBody)} {
		got := send(t, srv, req)
		if got.status != http.StatusTooManyRequests {
			t.Errorf("%s %s after ten starts = %d %s, want 429", req.method, req.path, got.status, got.body)
		}
	}
	// run-all has its own budget; reads are not limited by the start budget.
	if got := send(t, srv, post("/eval-runs/run-all")); got.status != 202 {
		t.Errorf("run-all = %d %s, want 202 (own bucket)", got.status, got.body)
	}
	if got := send(t, srv, get("/agents/"+id1+"/eval-runs")); got.status != 200 {
		t.Errorf("list = %d %s, want 200", got.status, got.body)
	}
	for i := range 9 {
		if got := send(t, srv, post("/eval-runs/run-all")); got.status != 202 {
			t.Fatalf("run-all %d = %d, want 202", i+2, got.status)
		}
	}
	if got := send(t, srv, post("/eval-runs/run-all")); got.status != http.StatusTooManyRequests {
		t.Errorf("11th run-all = %d, want 429", got.status)
	}
}

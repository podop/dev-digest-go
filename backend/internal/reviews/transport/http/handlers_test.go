package reviewshttp_test

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/platform/apperr"
	reviewer "dev-digest-go/backend/internal/reviewer/domain"
	"dev-digest-go/backend/internal/reviews/app"
	"dev-digest-go/backend/internal/reviews/domain"
)

func ptr[T any](v T) *T { return &v }

var ts = time.Date(2026, 10, 22, 8, 30, 15, 123_000_000, time.UTC)

const tsWire = "2026-10-22T08:30:15.123Z"

func wantJSON(t *testing.T, r response, status int, want string) {
	t.Helper()
	if r.Status != status {
		t.Fatalf("status = %d (%s), want %d", r.Status, r.Body, status)
	}
	if diff := cmp.Diff(want, strings.TrimSpace(r.Body)); diff != "" {
		t.Errorf("body mismatch (-want +got):\n%s", diff)
	}
}

func TestRoutes_NonUUIDIDIs422(t *testing.T) {
	api := &fakeAPI{}
	srv := newServer(t, api)
	routes := []struct{ method, path string }{
		{"POST", "/pulls/x/review"},
		{"GET", "/runs/x/events"},
		{"GET", "/pulls/x/runs/active"},
		{"GET", "/pulls/x/runs"},
		{"DELETE", "/runs/x"},
		{"POST", "/runs/x/cancel"},
		{"GET", "/runs/x/trace"},
		{"GET", "/pulls/x/reviews"},
		{"DELETE", "/reviews/x"},
		{"POST", "/findings/x/accept"},
		{"POST", "/findings/x/dismiss"},
		{"GET", "/multi-agent-runs/x"},
		{"GET", "/repos/x/multi-agent-runs"},
		{"POST", "/findings/x/learn"},
	}
	for _, rt := range routes {
		t.Run(rt.method+" "+rt.path, func(t *testing.T) {
			r := send(t, srv, rt.method, rt.path, "")
			if r.Status != http.StatusUnprocessableEntity || !strings.Contains(r.Body, `"code":"validation_error"`) ||
				!strings.Contains(r.Body, `"instancePath":"/id"`) {
				t.Errorf("%s %s = %d %s, want 422 validation_error at /id", rt.method, rt.path, r.Status, r.Body)
			}
		})
	}
	if n := api.callCount(); n != 0 {
		t.Errorf("use case calls = %d, want 0", n)
	}
}

func TestRoutes_ForeignOriginIsRefusedOnStateChangingRoutes(t *testing.T) {
	api := &fakeAPI{}
	srv := newServer(t, api)
	for _, rt := range []struct{ method, path string }{
		{"POST", "/pulls/" + otherID + "/review"},
		{"DELETE", "/runs/" + otherID},
		{"POST", "/runs/" + otherID + "/cancel"},
		{"DELETE", "/reviews/" + otherID},
		{"POST", "/findings/" + otherID + "/accept"},
		{"POST", "/findings/" + otherID + "/dismiss"},
		{"POST", "/findings/" + otherID + "/learn"},
	} {
		r := send(t, srv, rt.method, rt.path, "", "Origin", foreignOrigin, "Content-Type", "text/plain")
		if r.Status != http.StatusForbidden {
			t.Errorf("%s %s with a foreign Origin = %d, want 403", rt.method, rt.path, r.Status)
		}
	}
	if n := api.callCount(); n != 0 {
		t.Errorf("use case calls = %d, want 0", n)
	}
}

func TestStart_BodyParsing(t *testing.T) {
	a, b := otherID, "7e1d0000-0000-4000-8000-000000000002"
	tests := []struct {
		name    string
		body    string
		headers []string
		want    app.StartRequest
	}{
		{name: "no body"},
		{name: "null", body: "null"},
		{name: "empty object", body: "{}"},
		{name: "agent id", body: `{"agentId":"abc"}`, want: app.StartRequest{AgentID: "abc"}},
		{name: "all false", body: `{"all":false}`},
		{name: "all true with agent id", body: `{"agentId":"` + a + `","all":true}`, want: app.StartRequest{AgentID: a, All: true}},
		{name: "agent ids", body: `{"agentIds":["` + a + `","` + b + `"]}`, want: app.StartRequest{AgentIDs: []string{a, b}}},
		{name: "empty agent ids stays present", body: `{"agentIds":[]}`, want: app.StartRequest{AgentIDs: []string{}}},
		{name: "unknown keys are dropped", body: `{"agentId":"x","extra":1}`, want: app.StartRequest{AgentID: "x"}},
		{name: "charset parameter", body: `{"all":true}`, headers: []string{"Content-Type", "application/json; charset=utf-8"}, want: app.StartRequest{All: true}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			api := &fakeAPI{startRes: app.StartResult{PRID: otherID}}
			srv := newServer(t, api)
			r := send(t, srv, "POST", "/pulls/"+otherID+"/review", tc.body, tc.headers...)
			if r.Status != http.StatusOK {
				t.Fatalf("status = %d (%s), want 200", r.Status, r.Body)
			}
			if diff := cmp.Diff(tc.want, api.startReq); diff != "" {
				t.Errorf("StartRequest mismatch (-want +got):\n%s", diff)
			}
			if got := api.calls[0]; got != (call{"Start", workspaceID, otherID}) {
				t.Errorf("call = %+v, want Start in the workspace for the pull request", got)
			}
		})
	}
}

func TestStart_InvalidBodyIs422(t *testing.T) {
	tests := []struct {
		name, body, contentType string
		wantMessages            []string
		wantPaths               []string
	}{
		{"non uuid item", `{"agentIds":["x"]}`, "", []string{"Invalid uuid"}, []string{"/agentIds/0"}},
		{"all is a string", `{"all":"yes"}`, "", []string{"Expected boolean, received string"}, []string{"/all"}},
		{"agent id is a number", `{"agentId":5}`, "", []string{"Expected string, received number"}, []string{"/agentId"}},
		{"agent id is null", `{"agentId":null}`, "", []string{"Expected string, received null"}, []string{"/agentId"}},
		{"agent ids not an array", `{"agentIds":"a"}`, "", []string{"Expected array, received string"}, []string{"/agentIds"}},
		{"item not a string", `{"agentIds":[1]}`, "", []string{"Expected string, received number"}, []string{"/agentIds/0"}},
		{"several issues in field order", `{"agentIds":["x"],"all":1,"agentId":true}`, "",
			[]string{"Expected string, received boolean", "Expected boolean, received number", "Invalid uuid"},
			[]string{"/agentId", "/all", "/agentIds/0"}},
		{"array body", `[]`, "", []string{"Expected object, received array"}, []string{"/"}},
		{"plain text", `{"all":true}`, "text/plain", []string{"Expected object, received string"}, []string{"/"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			api := &fakeAPI{}
			srv := newServer(t, api)
			var headers []string
			if tc.contentType != "" {
				headers = []string{"Content-Type", tc.contentType}
			}
			r := send(t, srv, "POST", "/pulls/"+otherID+"/review", tc.body, headers...)
			if r.Status != http.StatusUnprocessableEntity || !strings.Contains(r.Body, `"code":"validation_error"`) {
				t.Fatalf("status = %d (%s), want 422 validation_error", r.Status, r.Body)
			}
			for i, m := range tc.wantMessages {
				if !strings.Contains(r.Body, `"message":"`+m+`"`) || !strings.Contains(r.Body, `"instancePath":"`+tc.wantPaths[i]+`"`) {
					t.Errorf("body %s lacks issue %q at %s", r.Body, m, tc.wantPaths[i])
				}
			}
			if n := api.callCount(); n != 0 {
				t.Errorf("use case calls = %d, want 0 (validation comes first)", n)
			}
		})
	}
}

func TestStart_ForeignOriginPlainTextNeverReachesTheUseCase(t *testing.T) {
	api := &fakeAPI{}
	srv := newServer(t, api)
	r := send(t, srv, "POST", "/pulls/"+otherID+"/review", `{"all":true}`, "Content-Type", "text/plain", "Origin", foreignOrigin)
	if r.Status != http.StatusForbidden {
		t.Errorf("status = %d, want 403", r.Status)
	}
	if n := api.callCount(); n != 0 {
		t.Errorf("use case calls = %d, want 0", n)
	}
}

func TestStart_Response(t *testing.T) {
	multi := "7e1d0000-0000-4000-8000-0000000000aa"
	api := &fakeAPI{startRes: app.StartResult{
		PRID:            otherID,
		MultiAgentRunID: &multi,
		Runs:            []app.RunTarget{{RunID: "r1", AgentID: "a1", AgentName: "Sec"}, {RunID: "r2", AgentID: "a2", AgentName: "Gen"}},
	}}
	srv := newServer(t, api)
	r := send(t, srv, "POST", "/pulls/"+otherID+"/review", `{"agentIds":["`+otherID+`"]}`)
	wantJSON(t, r, 200, `{"pr_id":"`+otherID+`","multi_agent_run_id":"`+multi+`","runs":[`+
		`{"run_id":"r1","agent_id":"a1","agent_name":"Sec"},{"run_id":"r2","agent_id":"a2","agent_name":"Gen"}],"reviews":[]}`)

	api.startRes = app.StartResult{PRID: otherID}
	r = send(t, srv, "POST", "/pulls/"+otherID+"/review", "")
	wantJSON(t, r, 200, `{"pr_id":"`+otherID+`","multi_agent_run_id":null,"runs":[],"reviews":[]}`)
}

func TestUseCaseErrorsAreRenderedAsTheEnvelope(t *testing.T) {
	tests := []struct {
		name, method, path string
		err                error
		wantStatus         int
		want               string
	}{
		{"agent not found", "POST", "/pulls/" + otherID + "/review", apperr.NotFound("Agent not found"), 404,
			`{"error":{"code":"not_found","message":"Agent not found"}}`},
		{"invalid run request", "POST", "/pulls/" + otherID + "/review", apperr.InvalidInput("Provide agentId, all:true or agentIds").WithCode("invalid_run_request"), 400,
			`{"error":{"code":"invalid_run_request","message":"Provide agentId, all:true or agentIds"}}`},
		{"run trace not found", "GET", "/runs/" + otherID + "/trace", apperr.NotFound("Run trace not found"), 404,
			`{"error":{"code":"not_found","message":"Run trace not found"}}`},
		{"trace invalid", "GET", "/runs/" + otherID + "/trace", apperr.Internal("Internal error"), 500,
			`{"error":{"code":"internal_error","message":"Internal error"}}`},
		{"multi-agent not found", "GET", "/multi-agent-runs/" + otherID, apperr.NotFound("Multi-agent run not found").WithCode("multi_agent_run_not_found"), 404,
			`{"error":{"code":"multi_agent_run_not_found","message":"Multi-agent run not found"}}`},
		{"invalid limit", "GET", "/repos/" + otherID + "/multi-agent-runs?limit=0", apperr.InvalidInput("limit must be between 1 and 20").WithCode("invalid_limit"), 400,
			`{"error":{"code":"invalid_limit","message":"limit must be between 1 and 20"}}`},
		{"cancel unknown run", "POST", "/runs/" + otherID + "/cancel", apperr.NotFound("Run not found"), 404,
			`{"error":{"code":"not_found","message":"Run not found"}}`},
		{"finding not found", "POST", "/findings/" + otherID + "/learn", apperr.NotFound("Finding not found"), 404,
			`{"error":{"code":"not_found","message":"Finding not found"}}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := newServer(t, &fakeAPI{err: tc.err})
			wantJSON(t, send(t, srv, tc.method, tc.path, ""), tc.wantStatus, tc.want)
		})
	}
}

func TestActionRoutes_IgnoreTheBody(t *testing.T) {
	api := &fakeAPI{finding: domain.FindingRecord{ID: "f1"}, learn: app.LearnResult{Finding: domain.FindingRecord{ID: "f1"}, MemoryID: "m1"}, deleted: true}
	srv := newServer(t, api)
	for _, rt := range []struct{ method, path string }{
		{"POST", "/runs/" + otherID + "/cancel"},
		{"POST", "/findings/" + otherID + "/accept"},
		{"POST", "/findings/" + otherID + "/dismiss"},
		{"POST", "/findings/" + otherID + "/learn"},
	} {
		for _, body := range []struct{ contentType, body string }{
			{"application/json", `{"reply":"not a finding"}`},
			{"application/json", `null`},
			{"text/plain", `whatever {`},
		} {
			r := send(t, srv, rt.method, rt.path, body.body, "Content-Type", body.contentType)
			if r.Status != http.StatusOK {
				t.Errorf("%s %s with %s %q = %d (%s), want 200", rt.method, rt.path, body.contentType, body.body, r.Status, r.Body)
			}
		}
	}
}

func TestSimpleRoutes_Responses(t *testing.T) {
	ran := ts
	api := &fakeAPI{
		active: []domain.ActiveRun{{RunID: "r1", AgentID: ptr("a1"), AgentName: ptr("Sec"), RanAt: &ran}, {RunID: "r2"}},
		runs: []domain.RunSummary{
			{RunID: "r1", AgentID: ptr("a1"), AgentName: ptr("Sec"), Provider: ptr("openrouter"), Model: ptr("m"), Status: domain.StatusDone,
				DurationMS: ptr(10), TokensIn: ptr(1), TokensOut: ptr(2), CostUSD: ptr(0.5), FindingsCount: ptr(3), Grounding: ptr("3/3 passed"),
				RanAt: &ran, Score: ptr(61), Blockers: ptr(1)},
			{RunID: "r2"},
		},
		deleted: true,
		estimates: []domain.AgentRunEstimate{
			{AgentID: "a1", SampleSize: 2, AvgDurationMS: ptr(1500.5), AvgCostUSD: ptr(0.002)},
			{AgentID: "a2"},
		},
	}
	srv := newServer(t, api)

	wantJSON(t, send(t, srv, "GET", "/pulls/"+otherID+"/runs/active", ""), 200,
		`[{"run_id":"r1","agent_id":"a1","agent_name":"Sec","ran_at":"`+tsWire+`"},{"run_id":"r2","agent_id":null,"agent_name":null,"ran_at":null}]`)
	wantJSON(t, send(t, srv, "GET", "/pulls/"+otherID+"/runs", ""), 200,
		`[{"run_id":"r1","agent_id":"a1","agent_name":"Sec","provider":"openrouter","model":"m","status":"done","error":null,"duration_ms":10,`+
			`"tokens_in":1,"tokens_out":2,"cost_usd":0.5,"findings_count":3,"grounding":"3/3 passed","ran_at":"`+tsWire+`","score":61,"blockers":1},`+
			`{"run_id":"r2","agent_id":null,"agent_name":null,"provider":null,"model":null,"status":null,"error":null,"duration_ms":null,`+
			`"tokens_in":null,"tokens_out":null,"cost_usd":null,"findings_count":null,"grounding":null,"ran_at":null,"score":null,"blockers":null}]`)
	wantJSON(t, send(t, srv, "DELETE", "/runs/"+otherID, ""), 200, `{"ok":true}`)
	api.deleted = false
	wantJSON(t, send(t, srv, "DELETE", "/runs/"+otherID, ""), 200, `{"ok":false}`)
	wantJSON(t, send(t, srv, "POST", "/runs/"+otherID+"/cancel", ""), 200, `{"ok":true}`)
	wantJSON(t, send(t, srv, "DELETE", "/reviews/"+otherID, ""), 200, `{"ok":true}`)
	wantJSON(t, send(t, srv, "GET", "/runs/estimates", ""), 200,
		`[{"agent_id":"a1","sample_size":2,"avg_duration_ms":1500.5,"avg_cost_usd":0.002},{"agent_id":"a2","sample_size":0,"avg_duration_ms":null,"avg_cost_usd":null}]`)
	if got := api.calls[len(api.calls)-1]; got != (call{"Estimates", workspaceID, ""}) {
		t.Errorf("last call = %+v, want Estimates in the workspace", got)
	}

	api.active, api.runs, api.estimates = nil, nil, nil
	wantJSON(t, send(t, srv, "GET", "/pulls/"+otherID+"/runs/active", ""), 200, `[]`)
	wantJSON(t, send(t, srv, "GET", "/pulls/"+otherID+"/runs", ""), 200, `[]`)
	wantJSON(t, send(t, srv, "GET", "/runs/estimates", ""), 200, `[]`)
}

func finding(id string) domain.FindingRecord {
	return domain.FindingRecord{
		ID: id, Severity: reviewer.SeverityCritical, Category: reviewer.CategorySecurity, Title: "T", File: "src/a.ts",
		StartLine: 3, EndLine: 5, Rationale: "R", Confidence: 0.9, Kind: reviewer.KindFinding, ReviewID: "rv1",
	}
}

const findingJSONHead = `"id":"%ID%","severity":"CRITICAL","category":"security","title":"T","file":"src/a.ts","start_line":3,"end_line":5,` +
	`"rationale":"R","suggestion":null,"confidence":0.9,"kind":"finding","trifecta_components":null,"evidence":null,"skill":null,` +
	`"out_of_scope":false,"review_id":"rv1"`

func findingJSON(id, tail string) string {
	return "{" + strings.ReplaceAll(findingJSONHead, "%ID%", id) + tail + "}"
}

func TestFindingRoutes_Responses(t *testing.T) {
	acc := ts
	f := finding("f1")
	f.AcceptedAt = &acc
	f.Skill = ptr("secrets-scan")
	f.Suggestion = ptr("fix")
	f.TrifectaComponents = []string{"untrusted_input"}
	f.EvalCaseID = ptr("ec1")
	f.OutOfScope = true
	api := &fakeAPI{finding: f}
	srv := newServer(t, api)

	want := `{"finding":{"id":"f1","severity":"CRITICAL","category":"security","title":"T","file":"src/a.ts","start_line":3,"end_line":5,` +
		`"rationale":"R","suggestion":"fix","confidence":0.9,"kind":"finding","trifecta_components":["untrusted_input"],"evidence":null,` +
		`"skill":"secrets-scan","out_of_scope":true,"review_id":"rv1","accepted_at":"` + tsWire + `","dismissed_at":null,"eval_case_id":"ec1"}}`
	wantJSON(t, send(t, srv, "POST", "/findings/"+otherID+"/accept", ""), 200, want)
	wantJSON(t, send(t, srv, "POST", "/findings/"+otherID+"/dismiss", ""), 200, want)
	calls := []string{api.calls[0].Method, api.calls[1].Method}
	if diff := cmp.Diff([]string{"AcceptFinding", "DismissFinding"}, calls); diff != "" {
		t.Errorf("calls mismatch (-want +got):\n%s", diff)
	}

	api.learn = app.LearnResult{Finding: finding("f2"), MemoryID: "m1"}
	wantJSON(t, send(t, srv, "POST", "/findings/"+otherID+"/learn", ""), 200,
		`{"finding":`+findingJSON("f2", `,"accepted_at":null,"dismissed_at":null,"eval_case_id":null,"memory_id":"m1"`)+`,"memoryId":"m1"}`)
}

func TestReviews_UsageKeysOnlyWithARun(t *testing.T) {
	api := &fakeAPI{reviews: []domain.ReviewRecord{
		{
			ID: "rv1", PRID: "p1", Kind: "review", AgentID: ptr("a1"), RunID: ptr("r1"), AgentName: ptr("Sec"),
			Verdict: ptr("comment"), Summary: ptr("S"), Score: ptr(70), Model: ptr("m"), CreatedAt: ts,
			Usage:    &domain.RunUsage{CostUSD: nil, TokensIn: ptr(5), TokensOut: ptr(6)},
			Findings: []domain.FindingRecord{finding("f1")},
		},
		{ID: "rv2", PRID: "p1", Kind: "summary", CreatedAt: ts},
	}}
	srv := newServer(t, api)
	wantJSON(t, send(t, srv, "GET", "/pulls/"+otherID+"/reviews", ""), 200,
		`[{"id":"rv1","pr_id":"p1","agent_id":"a1","run_id":"r1","agent_name":"Sec","kind":"review","verdict":"comment","summary":"S","score":70,`+
			`"model":"m","created_at":"`+tsWire+`","cost_usd":null,"tokens_in":5,"tokens_out":6,"findings":[`+
			findingJSON("f1", `,"accepted_at":null,"dismissed_at":null,"eval_case_id":null`)+`]},`+
			`{"id":"rv2","pr_id":"p1","agent_id":null,"run_id":null,"agent_name":null,"kind":"summary","verdict":null,"summary":null,"score":null,`+
			`"model":null,"created_at":"`+tsWire+`","findings":[]}]`)
}

func TestMultiAgentRoutes_Responses(t *testing.T) {
	col := finding("f1")
	api := &fakeAPI{
		multi: domain.MultiAgentRun{
			MultiAgentHeader: domain.MultiAgentHeader{ID: "m1", PRID: "p1", RepoID: "rp1", PRNumber: ptr(482), RanAt: ts},
			AgentCount:       1, TotalDurationMS: 20, TotalCostUSD: ptr(0.25),
			Columns: []domain.AgentColumn{{
				RunID: "r1", AgentID: "", AgentName: "", Status: domain.StatusFailed, Findings: []domain.FindingRecord{col},
			}},
			Conflicts: []domain.Conflict{{
				File: "src/a.ts", Line: 3, EndLine: 5, Title: "T", IsConflict: true,
				Takes: []domain.ConflictTake{{AgentID: "a1", Persona: "Sec", RunID: "r1", Verdict: "ignored"}},
			}},
		},
		multiList: []domain.MultiAgentSummary{{ID: "m1", PRID: "p1", RanAt: ts, AgentCount: 2, RunningCount: 1}},
	}
	srv := newServer(t, api)

	wantJSON(t, send(t, srv, "GET", "/multi-agent-runs/"+otherID, ""), 200,
		`{"id":"m1","pr_id":"p1","repo_id":"rp1","pr_number":482,"pr_title":null,"ran_at":"`+tsWire+`","agent_count":1,"total_duration_ms":20,`+
			`"total_cost_usd":0.25,"columns":[{"run_id":"r1","agent_id":"","agent_name":"","provider":null,"model":null,"status":"failed","error":null,`+
			`"verdict":null,"score":null,"summary":null,"duration_ms":null,"cost_usd":null,"findings":[`+
			findingJSON("f1", `,"accepted_at":null,"dismissed_at":null,"eval_case_id":null,"memory_id":null`)+`]}],`+
			`"conflicts":[{"file":"src/a.ts","line":3,"end_line":5,"title":"T","is_conflict":true,"takes":[{"agent_id":"a1","persona":"Sec",`+
			`"run_id":"r1","verdict":"ignored","note":"","finding_ids":[]}]}]}`)

	wantJSON(t, send(t, srv, "GET", "/repos/"+otherID+"/multi-agent-runs", ""), 200,
		`[{"id":"m1","pr_id":"p1","pr_number":null,"pr_title":null,"ran_at":"`+tsWire+`","agent_count":2,"running_count":1,"total_cost_usd":null}]`)
}

func TestListMultiAgentRuns_Limit(t *testing.T) {
	tests := []struct {
		query     string
		wantLimit *int // nil = not passed on
		wantCode  int
		wantMsg   string
	}{
		{"", nil, 200, ""},
		{"?limit=5", ptr(5), 200, ""},
		{"?limit=0", ptr(0), 200, ""},
		{"?limit=21", ptr(21), 200, ""},
		{"?limit=", ptr(0), 200, ""},
		{"?limit=1e1", ptr(10), 200, ""},
		{"?limit=abc", nil, 422, "Expected number, received nan"},
		{"?limit=2.5", nil, 422, "Expected integer, received float"},
		{"?limit=Infinity", nil, 422, "Expected integer, received float"},
		{"?limit=0x10", ptr(16), 200, ""},
		{"?limit=99999999999999", ptr(2147483647), 200, ""},
		{"?limit=-3", ptr(-3), 200, ""},
		{"?other=1", nil, 200, ""},
	}
	for _, tc := range tests {
		t.Run(tc.query, func(t *testing.T) {
			api := &fakeAPI{}
			srv := newServer(t, api)
			r := send(t, srv, "GET", "/repos/"+otherID+"/multi-agent-runs"+tc.query, "")
			if r.Status != tc.wantCode {
				t.Fatalf("status = %d (%s), want %d", r.Status, r.Body, tc.wantCode)
			}
			if tc.wantCode == 422 {
				if !strings.Contains(r.Body, `"message":"`+tc.wantMsg+`"`) || !strings.Contains(r.Body, `"instancePath":"/limit"`) {
					t.Errorf("body = %s, want issue %q at /limit", r.Body, tc.wantMsg)
				}
				if api.callCount() != 0 {
					t.Errorf("use case called on a 422")
				}
				return
			}
			if diff := cmp.Diff(tc.wantLimit, api.gotLimit); diff != "" {
				t.Errorf("limit passed on mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

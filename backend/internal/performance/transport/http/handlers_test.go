package performancehttp_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/labstack/echo/v5"

	"dev-digest-go/backend/internal/performance/domain"
	"dev-digest-go/backend/internal/platform/apperr"
)

func ptr[T any](v T) *T { return &v }

// issueDetail is the part of a 422 details item the tests compare.
type issueDetail struct {
	Keyword, InstancePath, Message string
}

func details(t *testing.T, r response) []issueDetail {
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

func TestRoutes_InvalidPeriodIs422OnBothRoutes(t *testing.T) {
	tests := []struct {
		name  string
		query string
		want  []issueDetail
	}{
		{"unknown period", "period=7d", []issueDetail{
			{"invalid_enum_value", "/period", "Invalid enum value. Expected '1d' | '30d' | 'custom', received '7d'"}}},
		{"preset with from", "period=1d&from=2026-01-01T00:00:00Z", []issueDetail{
			{"custom", "/from", "from is only allowed with period=custom"}}},
		{"custom without to", "period=custom&from=2026-01-01T00:00:00Z", []issueDetail{
			{"custom", "/to", "to is required with period=custom"}}},
		{"to not after from", "period=custom&from=2026-01-02T00:00:00Z&to=2026-01-01T00:00:00Z", []issueDetail{
			{"custom", "/to", "to must be after from"}}},
		{"367 days", "period=custom&from=2025-01-01T00:00:00Z&to=2026-01-03T00:00:00Z", []issueDetail{
			{"custom", "/to", "The range can be at most 366 days"}}},
		{"date only is not a datetime", "from=2026-01-01", []issueDetail{
			{"invalid_string", "/from", "Invalid datetime"},
			{"custom", "/from", "from is only allowed with period=custom"}}},
		{"repeated key", "period=1d&period=30d", []issueDetail{
			{"invalid_type", "/period", "Expected '1d' | '30d' | 'custom', received array"}}},
	}
	for _, route := range []string{"/agents/performance", "/agents/" + agentID + "/stats"} {
		for _, tc := range tests {
			t.Run(route+" "+tc.name, func(t *testing.T) {
				api := &fakeAPI{}
				r := get(t, newServer(t, api), route+"?"+tc.query)
				if diff := cmp.Diff(tc.want, details(t, r)); diff != "" {
					t.Errorf("GET %s?%s details mismatch (-want +got):\n%s", route, tc.query, diff)
				}
				if n := len(api.recorded()); n != 0 {
					t.Errorf("use case calls = %d, want 0 before validation passes", n)
				}
			})
		}
	}
}

func TestStats_PathIDIsValidatedBeforeTheQuery(t *testing.T) {
	api := &fakeAPI{}
	r := get(t, newServer(t, api), "/agents/not-a-uuid/stats?period=7d")
	got := details(t, r)
	if len(got) != 1 || got[0].InstancePath != "/id" {
		t.Errorf("details = %+v, want the single /id issue", got)
	}
}

func TestRoutes_PassTheValidatedQueryAndWorkspace(t *testing.T) {
	from := time.Date(2026, 1, 1, 0, 0, 0, 123_000_000, time.UTC)
	to := time.Date(2026, 1, 8, 0, 0, 0, 0, time.UTC)
	custom := domain.PeriodQuery{Key: domain.PeriodCustom, From: &from, To: &to}
	tests := []struct {
		name, path string
		wantMethod string
		wantQuery  domain.PeriodQuery
	}{
		{"default period", "/agents/performance", "Dashboard", domain.PeriodQuery{Key: domain.Period30d}},
		{"preset", "/agents/performance?period=1d", "Dashboard", domain.PeriodQuery{Key: domain.Period1d}},
		{"custom", "/agents/performance?period=custom&from=2026-01-01T00:00:00.123456Z&to=2026-01-08T00:00Z", "Dashboard", custom},
		{"stats with an offset", "/agents/" + agentID + "/stats?period=custom&from=" + url.QueryEscape("2026-01-01T05:30:00.123+05:30") + "&to=2026-01-08T00:00:00Z", "Stats", custom},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			api := &fakeAPI{}
			r := get(t, newServer(t, api), tc.path)
			if r.Status != http.StatusOK {
				t.Fatalf("GET %s = %d %s, want 200", tc.path, r.Status, r.Body)
			}
			calls := api.recorded()
			if len(calls) != 1 {
				t.Fatalf("calls = %+v, want one", calls)
			}
			want := call{Method: tc.wantMethod, WorkspaceID: workspaceID, Query: tc.wantQuery}
			if tc.wantMethod == "Stats" {
				want.AgentID = agentID
			}
			if diff := cmp.Diff(want, calls[0]); diff != "" {
				t.Errorf("use case call mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestUnknownQueryKeysAreIgnored(t *testing.T) {
	api := &fakeAPI{}
	if r := get(t, newServer(t, api), "/agents/performance?period=1d&limit=x&sort=name"); r.Status != http.StatusOK {
		t.Errorf("GET with unknown keys = %d %s, want 200", r.Status, r.Body)
	}
}

func TestStats_UnknownAgentIs404(t *testing.T) {
	api := &fakeAPI{err: apperr.NotFound("Agent not found")}
	r := get(t, newServer(t, api), "/agents/"+agentID+"/stats")
	want := `{"error":{"code":"not_found","message":"Agent not found"}}`
	if r.Status != http.StatusNotFound || strings.TrimSpace(r.Body) != want {
		t.Errorf("GET stats = %d %s, want 404 %s", r.Status, r.Body, want)
	}
}

func TestRoutes_UseCaseFailureIs500WithoutDetails(t *testing.T) {
	api := &fakeAPI{err: errors.New("pq: connection refused at 10.0.0.5")}
	for _, path := range []string{"/agents/performance", "/agents/" + agentID + "/stats"} {
		r := get(t, newServer(t, api), path)
		if r.Status != http.StatusInternalServerError || strings.Contains(r.Body, "10.0.0.5") || !strings.Contains(r.Body, `"internal_error"`) {
			t.Errorf("GET %s = %d %s, want 500 internal_error without the cause", path, r.Status, r.Body)
		}
	}
}

func TestRoutes_UnrepresentableInstantIs500(t *testing.T) {
	// Node accepted +24:00 in the schema and failed building the window (RangeError, 500).
	r := get(t, newServer(t, &fakeAPI{}), "/agents/performance?period=custom&from=2026-01-01T00:00:00%2B24:00&to=2026-01-02T00:00:00Z")
	if r.Status != http.StatusInternalServerError || !strings.Contains(r.Body, `"internal_error"`) {
		t.Errorf("GET = %d %s, want 500 internal_error", r.Status, r.Body)
	}
}

func TestDashboard_StaticRouteWinsOverAgentID(t *testing.T) {
	api := &fakeAPI{}
	shadow := func(e *echo.Echo) {
		e.GET("/agents/:id", func(c *echo.Context) error { return c.String(http.StatusTeapot, "agent by id") })
		e.GET("/agents/:id/:rest", func(c *echo.Context) error { return c.String(http.StatusTeapot, "agent sub-route") })
	}
	r := get(t, newServer(t, api, shadow), "/agents/performance")
	if r.Status != http.StatusOK || len(api.recorded()) != 1 {
		t.Errorf("GET /agents/performance = %d %s with %d use case calls, want the dashboard", r.Status, r.Body, len(api.recorded()))
	}
}

func TestDashboard_EmptyWorkspaceKeepsEveryArrayAnArray(t *testing.T) {
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	api := &fakeAPI{perf: domain.AgentPerf{
		Period: domain.PeriodInfo{Key: domain.Period30d, From: at, To: at.Add(time.Hour)},
	}}
	r := get(t, newServer(t, api), "/agents/performance")
	want := `{"period":{"key":"30d","from":"2026-01-01T00:00:00.000Z","to":"2026-01-01T01:00:00.000Z"},"min_decisions":5,"cost_basis":"estimate",` +
		`"summary":{"runs":0,"priced_runs":0,"unpriced_runs":0,"accepted":0,"dismissed":0,"pending":0,"decisions":0,"total_cost_usd":null,"avg_accept_rate":null,"most_active":null},` +
		`"previous_summary":{"runs":0,"priced_runs":0,"unpriced_runs":0,"accepted":0,"dismissed":0,"pending":0,"decisions":0,"total_cost_usd":null,"avg_accept_rate":null,"most_active":null},` +
		`"runs_trend":[],"agents":[],"cost_by_agent":[],"cost_by_model":[]}`
	if r.Status != http.StatusOK || strings.TrimSpace(r.Body) != want {
		t.Errorf("GET dashboard = %d\n%s\nwant\n%s", r.Status, r.Body, want)
	}
}

func TestStats_WireShape(t *testing.T) {
	at := time.Date(2026, 1, 1, 0, 0, 0, 999_999_000, time.UTC) // microseconds truncate to ms
	api := &fakeAPI{stats: domain.AgentStats{
		AgentID: agentID, AgentName: "alpha",
		Period:  domain.PeriodInfo{Key: domain.PeriodCustom, From: at, To: at.Add(time.Hour)},
		Metrics: domain.Metrics{Runs: 1, LastRunAt: &at, AcceptRate: ptr(0.5), TotalCostUSD: ptr(1e-7)},
		RunHistory: []domain.RunHistoryItem{{
			RunID: "r1", RanAt: at, Status: domain.RunDone, Source: domain.SourceCI,
			PR: &domain.RunPR{ID: "p", RepoID: "q", Number: 7}, Tokens: ptr(0), FindingsCount: ptr(3), HasTrace: true,
		}, {RunID: "r2", RanAt: at, Status: domain.RunFailed, Source: domain.SourceLocal}},
	}}
	r := get(t, newServer(t, api), "/agents/"+agentID+"/stats?period=custom&from=2026-01-01T00:00:00Z&to=2026-01-01T01:00:00Z")
	var got map[string]json.RawMessage
	if err := json.Unmarshal([]byte(r.Body), &got); err != nil {
		t.Fatalf("decode %q: %v", r.Body, err)
	}
	for key, want := range map[string]string{
		"period":      `{"key":"custom","from":"2026-01-01T00:00:00.999Z","to":"2026-01-01T01:00:00.999Z"}`,
		"run_history": `[{"run_id":"r1","ran_at":"2026-01-01T00:00:00.999Z","status":"done","source":"ci","pr":{"id":"p","repo_id":"q","number":7},"tokens":0,"cost_usd":null,"findings_count":3,"has_trace":true},{"run_id":"r2","ran_at":"2026-01-01T00:00:00.999Z","status":"failed","source":"local","pr":null,"tokens":null,"cost_usd":null,"findings_count":null,"has_trace":false}]`,
		"skills_used": `[]`, "memory_pulled": `[]`, "findings_by_severity": `[]`, "findings_by_category": `[]`, "cost_by_model": `[]`, "runs_trend": `[]`,
	} {
		if string(got[key]) != want {
			t.Errorf("stats.%s = %s, want %s", key, got[key], want)
		}
	}
	if m := string(got["metrics"]); !strings.Contains(m, `"total_cost_usd":1e-7`) || !strings.Contains(m, `"last_run_at":"2026-01-01T00:00:00.999Z"`) || !strings.Contains(m, `"accept_rate":0.5`) {
		t.Errorf("stats.metrics = %s", m)
	}
}

package main

import (
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/config"
)

// Component tests of Settings slice C through the real server (plan S18): the automatic_reviews
// setting (AC1-AC3), the run source `auto` in the trace, the agent stats, Agent Performance and the
// CI runs list (AC12), the status route (AC13) and the guards of both routes (AC15). The trigger
// itself is autoreview_flow_test.go; the adapters are autoreview_test.go.

const (
	scStatusPath = "/settings/automatic-reviews-status"
	scToken      = "ghp_sc_status_token_0xQ7"
)

// scClock is the settable clock of the server: the settings use case stamps enabled_at with it.
type scClock struct {
	mu sync.Mutex
	at time.Time
}

func newSCClock(at time.Time) *scClock { return &scClock{at: at} }

func (c *scClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.at
}

func (c *scClock) set(at time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.at = at
}

// scStamp is the wire form of enabled_at: ISO-8601 UTC with milliseconds.
func scStamp(at time.Time) string { return at.UTC().Format("2006-01-02T15:04:05.000Z") }

// scPollOn is the config of a deployment whose background poll is on, as the status states need.
func scPollOn(c *config.Config) { c.PRPollEnabled = true }

// scAuto reads the automatic_reviews object of GET /settings.
func scAuto(t testing.TB, s *stack) map[string]any {
	t.Helper()
	got := s.get("/settings")
	if got.Status != http.StatusOK {
		t.Fatalf("GET /settings = %d %s, want 200", got.Status, got.Body)
	}
	obj, ok := got.Obj(t)["automatic_reviews"].(map[string]any)
	if !ok {
		t.Fatalf("GET /settings automatic_reviews = %s, want an object", got.Body)
	}
	return obj
}

// scPut sends PUT /settings with automatic_reviews and requires a 200; it returns the answer's object.
func scPut(t testing.TB, s *stack, value any) map[string]any {
	t.Helper()
	got := putSettings(s, "automatic_reviews", value)
	if got.Status != http.StatusOK {
		t.Fatalf("PUT /settings automatic_reviews=%v = %d %s, want 200", value, got.Status, got.Body)
	}
	obj, ok := got.Obj(t)["automatic_reviews"].(map[string]any)
	if !ok {
		t.Fatalf("PUT /settings automatic_reviews=%v answered %s, want the object", value, got.Body)
	}
	return obj
}

// scStore writes the raw stored value, as an install that predates the object (or a hand edit) has it.
func scStore(t testing.TB, s *stack, rawJSON string) {
	t.Helper()
	execSQL(t, s, `DELETE FROM settings WHERE key = 'automatic_reviews'`)
	execSQL(t, s, `INSERT INTO settings (workspace_id, user_id, key, value)
		VALUES (?, (SELECT id FROM users ORDER BY created_at LIMIT 1), 'automatic_reviews', ?::jsonb)`, workspaceID(t, s), rawJSON)
}

// scAgents lists the ids of the seeded agents in the list order.
func scAgents(t testing.TB, s *stack) []string {
	t.Helper()
	list, _ := s.get("/agents").JSON(t).([]any)
	var out []string
	for _, a := range list {
		out = append(out, a.(map[string]any)["id"].(string))
	}
	if len(out) < 2 {
		t.Fatalf("GET /agents = %d agents, want the seeded ones (at least 2)", len(out))
	}
	return out
}

// scIssue is one issue of a 422 envelope.
type scIssue struct {
	Code    string
	Message string
	Path    string
}

// scIssues lists the issues of a 422 envelope.
func scIssues(t testing.TB, resp response) []scIssue {
	t.Helper()
	e, _ := resp.Obj(t)["error"].(map[string]any)
	details, _ := e["details"].([]any)
	var out []scIssue
	for _, d := range details {
		params, _ := d.(map[string]any)["params"].(map[string]any)
		issue, _ := params["issue"].(map[string]any)
		path, _ := issue["path"].([]any)
		code, _ := issue["code"].(string)
		msg, _ := issue["message"].(string)
		out = append(out, scIssue{Code: code, Message: msg, Path: fmt.Sprint(path)})
	}
	return out
}

// scUUID is a distinct, valid UUID per n.
func scUUID(n int) string { return fmt.Sprintf("00000000-0000-4000-8000-%012d", n) }

// ---- AC1: GET always carries the object ----

func TestSettingsC_GetCarriesTheFullObject(t *testing.T) { // AC1, FR1, EC16
	s := startStack(t, spec{})
	want := func(enabled bool) map[string]any {
		return map[string]any{"enabled": enabled, "agent_ids": []any{}, "on_new_pr": true, "on_new_commits": true, "enabled_at": nil}
	}
	tests := []struct {
		name   string
		stored string // "" = no stored value
		want   map[string]any
	}{
		{name: "no stored value", want: want(false)},
		{name: "a stored true", stored: `true`, want: want(true)},
		{name: "a stored false", stored: `false`, want: want(false)},
		{name: "a stored null", stored: `null`, want: want(false)},
		{name: "a partial object takes the defaults", stored: `{"enabled":true,"on_new_commits":false}`,
			want: map[string]any{"enabled": true, "agent_ids": []any{}, "on_new_pr": true, "on_new_commits": false, "enabled_at": nil}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.stored == "" {
				execSQL(t, s, `DELETE FROM settings WHERE key = 'automatic_reviews'`)
			} else {
				scStore(t, s, tc.stored)
			}
			got := s.get("/settings")
			if diff := cmp.Diff(tc.want, scAuto(t, s)); diff != "" {
				t.Errorf("GET /settings automatic_reviews mismatch (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff([]string{"enabled", "agent_ids", "on_new_pr", "on_new_commits", "enabled_at"},
				keysAt(t, got.Body, "automatic_reviews")); diff != "" {
				t.Errorf("GET /settings automatic_reviews key order mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// ---- AC2: PUT merges patches and stamps enabled_at ----

func TestSettingsC_PutMergesPatchesAndStampsEnabledAt(t *testing.T) { // AC2, FR2, EC15, EC16
	t0 := time.Date(2026, 10, 25, 8, 0, 0, 0, time.UTC)
	clock := newSCClock(t0)
	s := startStack(t, spec{now: clock.now})
	agents := scAgents(t, s)
	a, b := agents[0], agents[1]

	tests := []struct {
		name string
		at   time.Duration // the clock, after t0
		put  any
		want map[string]any
	}{
		{name: "agents without enabling stay unarmed", at: 0, put: map[string]any{"agent_ids": []string{a}},
			want: map[string]any{"enabled": false, "agent_ids": []any{a}, "on_new_pr": true, "on_new_commits": true, "enabled_at": nil}},
		{name: "false keeps the agents", at: time.Minute, put: false,
			want: map[string]any{"enabled": false, "agent_ids": []any{a}, "on_new_pr": true, "on_new_commits": true, "enabled_at": nil}},
		{name: "enabling stamps the clock and ignores the input enabled_at", at: 2 * time.Minute,
			put: map[string]any{"enabled": true, "enabled_at": "x", "extra": 1},
			want: map[string]any{"enabled": true, "agent_ids": []any{a}, "on_new_pr": true, "on_new_commits": true,
				"enabled_at": scStamp(t0.Add(2 * time.Minute))}},
		{name: "a later armed edit keeps enabled_at", at: 3 * time.Minute,
			put: map[string]any{"on_new_commits": false, "enabled_at": "2020-01-01T00:00:00.000Z"},
			want: map[string]any{"enabled": true, "agent_ids": []any{a}, "on_new_pr": true, "on_new_commits": false,
				"enabled_at": scStamp(t0.Add(2 * time.Minute))}},
		{name: "changing the agents keeps enabled_at", at: 4 * time.Minute, put: map[string]any{"agent_ids": []string{b, a}},
			want: map[string]any{"enabled": true, "agent_ids": []any{b, a}, "on_new_pr": true, "on_new_commits": false,
				"enabled_at": scStamp(t0.Add(2 * time.Minute))}},
		{name: "disabling clears enabled_at and keeps the rest", at: 5 * time.Minute, put: false,
			want: map[string]any{"enabled": false, "agent_ids": []any{b, a}, "on_new_pr": true, "on_new_commits": false, "enabled_at": nil}},
		{name: "re-arming stamps the new time", at: 6 * time.Minute, put: true,
			want: map[string]any{"enabled": true, "agent_ids": []any{b, a}, "on_new_pr": true, "on_new_commits": false,
				"enabled_at": scStamp(t0.Add(6 * time.Minute))}},
		{name: "no trigger left is not armed", at: 7 * time.Minute, put: map[string]any{"on_new_pr": false},
			want: map[string]any{"enabled": true, "agent_ids": []any{b, a}, "on_new_pr": false, "on_new_commits": false, "enabled_at": nil}},
		{name: "a trigger back arms again with a new time", at: 8 * time.Minute, put: map[string]any{"on_new_commits": true},
			want: map[string]any{"enabled": true, "agent_ids": []any{b, a}, "on_new_pr": false, "on_new_commits": true,
				"enabled_at": scStamp(t0.Add(8 * time.Minute))}},
		{name: "an empty agent list is not armed", at: 9 * time.Minute, put: map[string]any{"agent_ids": []string{}},
			want: map[string]any{"enabled": true, "agent_ids": []any{}, "on_new_pr": false, "on_new_commits": true, "enabled_at": nil}},
		{name: "an empty object changes nothing", at: 10 * time.Minute, put: map[string]any{},
			want: map[string]any{"enabled": true, "agent_ids": []any{}, "on_new_pr": false, "on_new_commits": true, "enabled_at": nil}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			clock.set(t0.Add(tc.at))
			if diff := cmp.Diff(tc.want, scPut(t, s, tc.put)); diff != "" {
				t.Errorf("PUT automatic_reviews=%v answer mismatch (-want +got):\n%s", tc.put, diff)
			}
			if diff := cmp.Diff(tc.want, scAuto(t, s)); diff != "" {
				t.Errorf("GET /settings after PUT automatic_reviews=%v mismatch (-want +got):\n%s", tc.put, diff)
			}
		})
	}

	t.Run("another setting leaves the value untouched", func(t *testing.T) {
		before := scAuto(t, s)
		clock.set(t0.Add(time.Hour))
		if put := putSettings(s, "theme", "light"); put.Status != http.StatusOK {
			t.Fatalf("PUT theme = %d %s, want 200", put.Status, put.Body)
		}
		if diff := cmp.Diff(before, scAuto(t, s)); diff != "" {
			t.Errorf("automatic_reviews after PUT theme changed (-before +after):\n%s", diff)
		}
	})

	t.Run("a legacy true is armed by picking an agent", func(t *testing.T) { // EC16
		scStore(t, s, `true`)
		at := t0.Add(2 * time.Hour)
		clock.set(at)
		want := map[string]any{"enabled": true, "agent_ids": []any{a}, "on_new_pr": true, "on_new_commits": true, "enabled_at": scStamp(at)}
		if diff := cmp.Diff(want, scPut(t, s, map[string]any{"agent_ids": []string{a}})); diff != "" {
			t.Errorf("PUT agent_ids over a stored true mismatch (-want +got):\n%s", diff)
		}
	})

	t.Run("a stored armed value without enabled_at counts as newly armed", func(t *testing.T) {
		scStore(t, s, fmt.Sprintf(`{"enabled":true,"agent_ids":[%q]}`, a))
		at := t0.Add(3 * time.Hour)
		clock.set(at)
		if got := scPut(t, s, map[string]any{"on_new_commits": false}); got["enabled_at"] != scStamp(at) {
			t.Errorf("enabled_at after an edit of an armed value without one = %v, want %s", got["enabled_at"], scStamp(at))
		}
	})
}

// ---- AC3: the 422 texts ----

func TestSettingsC_PutRejectsBadInput(t *testing.T) { // AC3, FR2
	s := startStack(t, spec{})
	a := scAgents(t, s)[0]
	ids21 := make([]string, 21)
	for i := range ids21 {
		ids21[i] = scUUID(i + 1)
	}
	keep := scPut(t, s, map[string]any{"enabled": true, "agent_ids": []string{a}})

	rejected := []struct {
		name string
		body any
		want []scIssue
	}{
		{name: "a string", body: "on",
			want: []scIssue{{"invalid_type", "Expected object, received string", "[automatic_reviews]"}}},
		{name: "null", body: nil,
			want: []scIssue{{"invalid_type", "Expected object, received null", "[automatic_reviews]"}}},
		{name: "a number", body: 1,
			want: []scIssue{{"invalid_type", "Expected object, received number", "[automatic_reviews]"}}},
		{name: "an array", body: []string{},
			want: []scIssue{{"invalid_type", "Expected object, received array", "[automatic_reviews]"}}},
		{name: "enabled as a number", body: map[string]any{"enabled": 1},
			want: []scIssue{{"invalid_type", "Expected boolean, received number", "[automatic_reviews enabled]"}}},
		{name: "a trigger as a string", body: map[string]any{"on_new_pr": "yes"},
			want: []scIssue{{"invalid_type", "Expected boolean, received string", "[automatic_reviews on_new_pr]"}}},
		{name: "agent_ids as a string", body: map[string]any{"agent_ids": "a"},
			want: []scIssue{{"invalid_type", "Expected array, received string", "[automatic_reviews agent_ids]"}}},
		{name: "an id that is not a uuid", body: map[string]any{"agent_ids": []string{"x"}},
			want: []scIssue{{"invalid_string", "Invalid uuid", "[automatic_reviews agent_ids 0]"}}},
		{name: "an id that is not a string", body: map[string]any{"agent_ids": []any{a, 7}},
			want: []scIssue{{"invalid_type", "Expected string, received number", "[automatic_reviews agent_ids 1]"}}},
		{name: "21 distinct ids", body: map[string]any{"agent_ids": ids21},
			want: []scIssue{{"too_big", "Array must contain at most 20 element(s)", "[automatic_reviews agent_ids]"}}},
		{name: "every issue is reported", body: map[string]any{"enabled": "x", "agent_ids": []string{"y"}},
			want: []scIssue{
				{"invalid_type", "Expected boolean, received string", "[automatic_reviews enabled]"},
				{"invalid_string", "Invalid uuid", "[automatic_reviews agent_ids 0]"},
			}},
	}
	for _, tc := range rejected {
		t.Run(tc.name, func(t *testing.T) {
			got := putSettings(s, "automatic_reviews", tc.body)
			if got.Status != http.StatusUnprocessableEntity || got.errorCode(t) != "validation_error" {
				t.Fatalf("PUT automatic_reviews=%v = %d %s, want 422 validation_error", tc.body, got.Status, got.Body)
			}
			if diff := cmp.Diff(tc.want, scIssues(t, got)); diff != "" {
				t.Errorf("PUT automatic_reviews=%v issues mismatch (-want +got):\n%s", tc.body, diff)
			}
			if diff := cmp.Diff(keep, scAuto(t, s)); diff != "" {
				t.Errorf("automatic_reviews after a rejected PUT changed (-before +after):\n%s", diff)
			}
		})
	}

	accepted := []struct {
		name string
		body map[string]any
		want []any
	}{
		{name: "two duplicates store one id", body: map[string]any{"agent_ids": []string{a, a}}, want: []any{a}},
		{name: "ids are lower-cased and de-duplicated", body: map[string]any{"agent_ids": []string{strings.ToUpper(a), a}}, want: []any{a}},
		{name: "20 distinct ids", body: map[string]any{"agent_ids": ids21[:20]}, want: toAny(ids21[:20])},
		{name: "22 ids with duplicates that make 20", body: map[string]any{"agent_ids": append(append([]string{}, ids21[:20]...), ids21[0], ids21[1])},
			want: toAny(ids21[:20])},
	}
	for _, tc := range accepted {
		t.Run(tc.name, func(t *testing.T) {
			if diff := cmp.Diff(tc.want, scPut(t, s, tc.body)["agent_ids"]); diff != "" {
				t.Errorf("stored agent_ids after PUT %v mismatch (-want +got):\n%s", tc.body, diff)
			}
		})
	}
}

func toAny(ss []string) []any {
	out := make([]any, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return out
}

// ---- AC12: the run source `auto` ----

// An automatic run is stored with source `auto`; the trace carries it in config.source, the agent's
// stats list it with source `auto`, Agent Performance counts it in runs and runs_local, and the CI
// runs list (its own table) does not show it. The runs are seeded: what starts them is the trigger
// (autoreview_flow_test.go) and the executor writing the source is the reviews package's own test.
func TestSettingsC_AutoRunsAppearInTraceStatsAndPerformance(t *testing.T) { // AC12, FR11
	s := startStack(t, spec{now: fixedClock})
	execSQL(t, s, `DELETE FROM agent_runs`)
	f := newFx(t, s)
	pr := pr482(t, s)
	agent := agentByName(t, s, agentGeneral)

	local := insertPerfRun(t, s, f, pr, perfRun{agent: agent, status: "done", source: "local", ranAt: "2026-10-20T10:00:00Z", duration: ms(1000), cost: usd(0.5)})
	auto := insertPerfRun(t, s, f, pr, perfRun{agent: agent, status: "done", source: "auto", ranAt: "2026-10-21T10:00:00Z", duration: ms(3000), cost: usd(0.25)})
	autoFailed := insertPerfRun(t, s, f, pr, perfRun{agent: agent, status: "failed", source: "auto", ranAt: "2026-10-21T11:00:00Z"})
	ci := insertPerfRun(t, s, f, pr, perfRun{agent: agent, status: "done", source: "ci", ranAt: "2026-10-19T10:00:00Z", duration: ms(2000), cost: usd(0.1)})
	execSQL(t, s, `INSERT INTO run_traces (run_id, trace) VALUES (?, ?::jsonb)`, auto, `{
		"config":{"agent":"General Reviewer","version":"1","provider":"openrouter","model":"m","pr":482,"source":"auto"},
		"stats":{"duration_ms":3000,"tokens_in":1,"tokens_out":10,"cost_usd":0.25,"findings":0,"grounding":"0/0 passed"},
		"prompt_assembly":{"system":"s","user":"u","skills":null,"memory":null,"specs":null,"callers":null,"repo_map":null,"pr_description":null,"intent":null},
		"tool_calls":[],"raw_output":"","memory_pulled":[],"specs_read":[],"log":[],"skills_used":[],"intent":null}`)

	trace := s.get("/runs/" + auto + "/trace")
	if trace.Status != http.StatusOK {
		t.Fatalf("GET /runs/%s/trace = %d %s, want 200", auto, trace.Status, trace.Body)
	}
	if cfg, _ := trace.Obj(t)["config"].(map[string]any); cfg["source"] != "auto" {
		t.Errorf("trace config.source = %v, want auto", cfg["source"])
	}

	stats := s.get("/agents/" + agent + "/stats?period=30d")
	if stats.Status != http.StatusOK {
		t.Fatalf("GET /agents/%s/stats = %d %s, want 200", agent, stats.Status, stats.Body)
	}
	history, _ := stats.Obj(t)["run_history"].([]any)
	gotSource := map[string]any{}
	for _, h := range history {
		row := h.(map[string]any)
		gotSource[row["run_id"].(string)] = row["source"]
	}
	wantSource := map[string]any{local: "local", auto: "auto", autoFailed: "auto", ci: "ci"}
	if diff := cmp.Diff(wantSource, gotSource); diff != "" {
		t.Errorf("run_history sources by run (-want +got):\n%s", diff)
	}

	dash := s.get("/agents/performance?period=30d")
	if dash.Status != http.StatusOK {
		t.Fatalf("GET /agents/performance = %d %s, want 200", dash.Status, dash.Body)
	}
	for _, row := range dash.Obj(t)["agents"].([]any) {
		r := row.(map[string]any)
		if r["agent_name"] != agentGeneral {
			continue
		}
		m := r["metrics"].(map[string]any)
		// Four counted runs: local, two automatic ones (counted as local) and one CI run.
		for key, want := range map[string]float64{"runs": 4, "runs_local": 3, "runs_ci": 1, "runs_done": 3, "runs_failed": 1} {
			if m[key] != want {
				t.Errorf("General Reviewer metrics.%s = %v, want %v", key, m[key], want)
			}
		}
		if diff := cmp.Diff(m, stats.Obj(t)["metrics"]); diff != "" {
			t.Errorf("the stats' metrics differ from the dashboard row (-dashboard +stats):\n%s", diff)
		}
		goto ciList
	}
	t.Fatalf("GET /agents/performance has no %s row: %s", agentGeneral, dash.Body)

ciList:
	list := s.get("/ci/runs")
	if list.Status != http.StatusOK {
		t.Fatalf("GET /ci/runs = %d %s, want 200", list.Status, list.Body)
	}
	if runs, _ := list.Obj(t)["runs"].([]any); len(runs) != 0 {
		t.Errorf("GET /ci/runs lists %d runs, want none (automatic and local runs are not CI runs): %s", len(runs), list.Body)
	}
}

// ---- AC13: the status route ----

func TestSettingsC_StatusShapeAndFacts(t *testing.T) { // AC13, FR12
	s := startStack(t, spec{})
	got := s.get(scStatusPath)
	if got.Status != http.StatusOK {
		t.Fatalf("GET %s = %d %s, want 200", scStatusPath, got.Status, got.Body)
	}
	if diff := cmp.Diff([]string{"state", "last_poll_at", "prs_tracked", "poll_interval_min"}, topLevelKeys(t, got.Body)); diff != "" {
		t.Errorf("GET %s key order mismatch (-want +got):\n%s", scStatusPath, diff)
	}
	open := scalar[int64](t, s.db.DB, `SELECT count(*) FROM pull_requests WHERE status NOT IN ('merged', 'closed')`)
	if open < 2 {
		t.Fatalf("the seed holds %d open PRs, want at least 2 for this test", open)
	}
	execSQL(t, s, `UPDATE repos SET last_polled_at = NULL`)
	want := map[string]any{"state": "off", "last_poll_at": nil, "prs_tracked": float64(open), "poll_interval_min": float64(5)}
	if diff := cmp.Diff(want, s.get(scStatusPath).Obj(t)); diff != "" {
		t.Errorf("status of a fresh install mismatch (-want +got):\n%s", diff)
	}

	t.Run("last_poll_at is the newest repo poll time in UTC milliseconds", func(t *testing.T) {
		execSQL(t, s, `UPDATE repos SET last_polled_at = '2026-10-24T09:15:30.250Z'::timestamptz`)
		execSQL(t, s, `UPDATE repos SET last_polled_at = '2026-10-25T07:00:00.500+02:00'::timestamptz
			WHERE id = (SELECT id FROM repos ORDER BY full_name LIMIT 1)`)
		// 07:00:00.500+02:00 is 05:00:00.500Z of the 25th, newer than the 24th.
		if got := s.get(scStatusPath).Obj(t)["last_poll_at"]; got != "2026-10-25T05:00:00.500Z" {
			t.Errorf("last_poll_at = %v, want 2026-10-25T05:00:00.500Z", got)
		}
	})

	t.Run("prs_tracked counts open PRs, drafts included, not merged or closed", func(t *testing.T) {
		id := scalar[string](t, s.db.DB, `SELECT id::text FROM pull_requests WHERE status NOT IN ('merged', 'closed') ORDER BY id LIMIT 1`)
		execSQL(t, s, `UPDATE pull_requests SET draft = true WHERE id = ?`, id)
		if got := s.get(scStatusPath).Obj(t)["prs_tracked"]; got != float64(open) {
			t.Errorf("prs_tracked with a draft = %v, want %d (drafts count)", got, open)
		}
		execSQL(t, s, `UPDATE pull_requests SET status = 'merged' WHERE id = ?`, id)
		if got := s.get(scStatusPath).Obj(t)["prs_tracked"]; got != float64(open-1) {
			t.Errorf("prs_tracked after a merge = %v, want %d", got, open-1)
		}
		execSQL(t, s, `UPDATE pull_requests SET status = 'closed' WHERE status NOT IN ('merged', 'closed')`)
		if got := s.get(scStatusPath).Obj(t)["prs_tracked"]; got != float64(0) {
			t.Errorf("prs_tracked with every PR closed = %v, want 0", got)
		}
	})

	t.Run("poll_interval_min follows polling_interval_min, clamped", func(t *testing.T) {
		for _, tc := range []struct {
			stored float64
			want   float64
		}{{15, 15}, {1, 1}, {99999, 1440}} {
			if put := putSettings(s, "polling_interval_min", tc.stored); put.Status != http.StatusOK {
				t.Fatalf("PUT polling_interval_min=%v = %d %s, want 200", tc.stored, put.Status, put.Body)
			}
			if got := s.get(scStatusPath).Obj(t)["poll_interval_min"]; got != tc.want {
				t.Errorf("poll_interval_min with polling_interval_min %v = %v, want %v", tc.stored, got, tc.want)
			}
		}
	})
}

// The state follows FR12's precedence, each combination decided by the first condition that holds.
func TestSettingsC_StatusStatePrecedence(t *testing.T) { // AC13, FR12, EC16
	type facts struct {
		env  map[string]string
		poll bool
	}
	tests := []struct {
		name  string
		facts facts
		// setup arms the setting and the facts; the answer must then be want.
		setup func(t *testing.T, s *stack, agents []string)
		want  string
	}{
		{name: "off beats everything", facts: facts{poll: false},
			setup: func(t *testing.T, s *stack, agents []string) {
				scPut(t, s, map[string]any{"enabled": false, "agent_ids": []string{agents[0]}, "on_new_pr": false, "on_new_commits": false})
				setCap(t, s, 0)
			},
			want: "off"},
		{name: "poll_disabled beats the missing token and agents", facts: facts{poll: false},
			setup: func(t *testing.T, s *stack, agents []string) { scPut(t, s, true) },
			want:  "poll_disabled"},
		{name: "no_github_token beats the missing agents", facts: facts{poll: true},
			setup: func(t *testing.T, s *stack, agents []string) { scPut(t, s, true) },
			want:  "no_github_token"},
		{name: "no_agents for an empty selection", facts: facts{poll: true, env: map[string]string{"GITHUB_TOKEN": scToken}},
			setup: func(t *testing.T, s *stack, agents []string) { scPut(t, s, true) },
			want:  "no_agents"},
		{name: "no_agents for a stored legacy true", facts: facts{poll: true, env: map[string]string{"GITHUB_TOKEN": scToken}},
			setup: func(t *testing.T, s *stack, agents []string) { scStore(t, s, `true`) },
			want:  "no_agents"},
		{name: "no_agents for an agent that no longer exists", facts: facts{poll: true, env: map[string]string{"GITHUB_TOKEN": scToken}},
			setup: func(t *testing.T, s *stack, agents []string) {
				scPut(t, s, map[string]any{"enabled": true, "agent_ids": []string{scUUID(99)}})
			},
			want: "no_agents"},
		{name: "no_agents for a disabled agent", facts: facts{poll: true, env: map[string]string{"GITHUB_TOKEN": scToken}},
			setup: func(t *testing.T, s *stack, agents []string) {
				execSQL(t, s, `UPDATE agents SET enabled = false WHERE id = ?`, agents[0])
				scPut(t, s, map[string]any{"enabled": true, "agent_ids": []string{agents[0]}})
			},
			want: "no_agents"},
		{name: "active when one of two selected agents is disabled", facts: facts{poll: true, env: map[string]string{"GITHUB_TOKEN": scToken}},
			setup: func(t *testing.T, s *stack, agents []string) {
				execSQL(t, s, `UPDATE agents SET enabled = false WHERE id = ?`, agents[0])
				scPut(t, s, map[string]any{"enabled": true, "agent_ids": []string{agents[0], agents[1]}})
			},
			want: "active"},
		{name: "no_triggers beats the spend cap", facts: facts{poll: true, env: map[string]string{"GITHUB_TOKEN": scToken}},
			setup: func(t *testing.T, s *stack, agents []string) {
				scPut(t, s, map[string]any{"enabled": true, "agent_ids": []string{agents[0]}, "on_new_pr": false, "on_new_commits": false})
				setCap(t, s, 0)
			},
			want: "no_triggers"},
		{name: "paused_spend_cap when the cap is reached", facts: facts{poll: true, env: map[string]string{"GITHUB_TOKEN": scToken}},
			setup: func(t *testing.T, s *stack, agents []string) {
				scPut(t, s, map[string]any{"enabled": true, "agent_ids": []string{agents[0]}})
				setCap(t, s, 0)
			},
			want: "paused_spend_cap"},
		{name: "active under a cap that is not reached", facts: facts{poll: true, env: map[string]string{"GITHUB_TOKEN": scToken}},
			setup: func(t *testing.T, s *stack, agents []string) {
				scPut(t, s, map[string]any{"enabled": true, "agent_ids": []string{agents[0]}})
				setCap(t, s, 100)
			},
			want: "active"},
		{name: "active with no cap", facts: facts{poll: true, env: map[string]string{"GITHUB_TOKEN": scToken}},
			setup: func(t *testing.T, s *stack, agents []string) {
				scPut(t, s, map[string]any{"enabled": true, "agent_ids": []string{agents[0]}, "on_new_pr": false})
			},
			want: "active"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			sp := spec{env: tc.facts.env}
			if tc.facts.poll {
				sp.config = scPollOn
			}
			s := startStack(t, sp)
			tc.setup(t, s, scAgents(t, s))
			got := s.get(scStatusPath)
			if got.Status != http.StatusOK {
				t.Fatalf("GET %s = %d %s, want 200", scStatusPath, got.Status, got.Body)
			}
			if state := got.Obj(t)["state"]; state != tc.want {
				t.Errorf("status state = %v, want %s", state, tc.want)
			}
			assertNoSecret(t, "status body", string(got.Body), scToken, "GITHUB_TOKEN")
			assertNoSecret(t, "server log", s.logs.String(), scToken)
		})
	}
}

// ---- AC15: the guards ----

func TestSettingsC_GuardsOfTheSettingAndTheStatusRoute(t *testing.T) { // AC15, NFR7
	s := startStack(t, spec{allowedHosts: sbHosts})
	a := scAgents(t, s)[0]
	before := scAuto(t, s)
	body := map[string]any{"automatic_reviews": map[string]any{"enabled": true, "agent_ids": []string{a}}}

	t.Run("a foreign Origin on PUT /settings is refused and changes nothing", func(t *testing.T) {
		got := s.do(http.MethodPut, "/settings", body, "Origin", "http://evil.example")
		want := map[string]any{"error": map[string]any{"code": "forbidden", "message": "Origin not allowed"}}
		if got.Status != http.StatusForbidden {
			t.Fatalf("PUT /settings with a foreign Origin = %d %s, want 403", got.Status, got.Body)
		}
		if diff := cmp.Diff(want, got.JSON(t)); diff != "" {
			t.Errorf("PUT /settings foreign Origin body mismatch (-want +got):\n%s", diff)
		}
		if diff := cmp.Diff(before, scAuto(t, s)); diff != "" {
			t.Errorf("automatic_reviews after the refused PUT changed (-before +after):\n%s", diff)
		}
	})

	t.Run("the allowed Origin is served", func(t *testing.T) {
		got := s.do(http.MethodPut, "/settings", body, "Origin", "http://localhost:3100")
		if got.Status != http.StatusOK {
			t.Fatalf("PUT /settings with the UI Origin = %d %s, want 200", got.Status, got.Body)
		}
		if enabled := scAuto(t, s)["enabled"]; enabled != true {
			t.Errorf("automatic_reviews.enabled after the allowed PUT = %v, want true", enabled)
		}
	})

	t.Run("a body that is not application/json is refused like on the other routes", func(t *testing.T) {
		stored := scAuto(t, s)
		other := s.do(http.MethodPut, "/workspace", `{"name":"Kept"}`, "Content-Type", "text/plain")
		got := s.do(http.MethodPut, "/settings", `{"automatic_reviews":false}`, "Content-Type", "text/plain")
		if got.Status == http.StatusOK || got.Status != other.Status || got.errorCode(t) != other.errorCode(t) {
			t.Errorf("PUT /settings as text/plain = %d %s, want the refusal of PUT /workspace (%d %s)", got.Status, got.Body, other.Status, other.Body)
		}
		if diff := cmp.Diff(stored, scAuto(t, s)); diff != "" {
			t.Errorf("automatic_reviews after the text/plain PUT changed (-before +after):\n%s", diff)
		}
	})

	t.Run("a foreign Host on the status route is refused", func(t *testing.T) {
		got := s.do(http.MethodGet, scStatusPath, nil, "Host", "evil.example")
		want := map[string]any{"error": map[string]any{"code": "forbidden", "message": "Host not allowed"}}
		if got.Status != http.StatusForbidden {
			t.Fatalf("GET %s with a foreign Host = %d %s, want 403", scStatusPath, got.Status, got.Body)
		}
		if diff := cmp.Diff(want, got.JSON(t)); diff != "" {
			t.Errorf("GET %s foreign Host body mismatch (-want +got):\n%s", scStatusPath, diff)
		}
		if ok := s.get(scStatusPath); ok.Status != http.StatusOK {
			t.Errorf("GET %s with the allowed Host = %d %s, want 200", scStatusPath, ok.Status, ok.Body)
		}
	})

	t.Run("the status route is read-only", func(t *testing.T) {
		for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
			got := s.do(method, scStatusPath, map[string]any{})
			if got.Status != http.StatusNotFound && got.Status != http.StatusMethodNotAllowed {
				t.Errorf("%s %s = %d %s, want 404 or 405", method, scStatusPath, got.Status, got.Body)
			}
		}
	})
}

// ---- the contract golden ----

// TestContracts_SettingsC records the status answer the UI contract test validates (NFR9, K15). It
// runs on its own server with a pinned poll time so the golden does not depend on the day of the run.
func TestContracts_SettingsC(t *testing.T) {
	s := startStack(t, spec{
		env:    map[string]string{"GITHUB_TOKEN": scToken},
		config: scPollOn,
	})
	agent := scAgents(t, s)[0]
	scPut(t, s, map[string]any{"enabled": true, "agent_ids": []string{agent}})
	execSQL(t, s, `UPDATE repos SET last_polled_at = '2026-10-24T09:15:30.250Z'::timestamptz`)

	got := s.get(scStatusPath)
	assertNoSecret(t, "status body", string(got.Body), scToken)
	g := &goldens{t: t, cloneDir: s.cfg.CloneDir, dataDir: s.cfg.DataDir, ids: map[string]string{}}
	g.check("automatic_reviews_status", got, http.StatusOK)
}

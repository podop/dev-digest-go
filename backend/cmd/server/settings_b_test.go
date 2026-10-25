package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"gorm.io/gorm"

	"dev-digest-go/backend/internal/platform/postgres/pgtest"
)

// Component tests of Settings slice B through the real server (plan S17): rename, retention
// setting, prune visible through the API, stats, export, reset refusals and the guards of the
// workspace routes, plus the contract goldens the UI contract test reads. The history side
// (export leak, reset of every kind, startup prune) is workspace_history_test.go.

// sbNow is the clock of these tests: the export instant and the date in the export file name.
var sbNow = time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)

// sbHosts is the Host allow-list of the guard tests.
var sbHosts = []string{"localhost", "127.0.0.1", "[::1]", "backend"}

// sbEnv is a migrated database that holds the default workspace and the system user, no seed.
type sbEnv struct {
	db   *pgtest.Database
	ws   string
	user string
}

func newSBEnv(t *testing.T) *sbEnv {
	t.Helper()
	db := pgtest.Migrated(t)
	e := &sbEnv{db: db}
	e.ws = scalar[string](t, db.DB, `INSERT INTO workspaces (name) VALUES ('default') RETURNING id::text`)
	e.user = scalar[string](t, db.DB, `INSERT INTO users (email, name) VALUES ('you@local', 'You') RETURNING id::text`)
	return e
}

func (e *sbEnv) exec(t testing.TB, query string, args ...any) {
	t.Helper()
	if err := e.db.DB.Exec(query, args...).Error; err != nil {
		t.Fatalf("exec %.60s: %v", query, err)
	}
}

func (e *sbEnv) id(t testing.TB, query string, args ...any) string {
	t.Helper()
	return scalar[string](t, e.db.DB, query, args...)
}

// start boots the server over the database with the fixed clock and the Host allow-list.
func (e *sbEnv) start(t *testing.T) *stack {
	t.Helper()
	return startStack(t, spec{
		db:           e.db,
		noSeed:       true,
		now:          func() time.Time { return sbNow },
		allowedHosts: sbHosts,
	})
}

// setting stores a workspace setting as the system user does.
func (e *sbEnv) setting(t testing.TB, key, rawJSON string) {
	t.Helper()
	e.exec(t, `INSERT INTO settings (workspace_id, user_id, key, value) VALUES (?, ?, ?, ?::jsonb)`, e.ws, e.user, key, rawJSON)
}

// agent inserts an agent created offset seconds after a fixed instant.
func (e *sbEnv) agent(t testing.TB, name string, enabled bool, offset int) string {
	t.Helper()
	return e.id(t, `INSERT INTO agents (workspace_id, name, provider, model, system_prompt, enabled, created_at)
		VALUES (?, ?, 'openai', 'gpt-x', 'prompt of ' || ?, ?, '2026-01-01T00:00:00Z'::timestamptz + make_interval(secs => ?))
		RETURNING id::text`, e.ws, name, name, enabled, offset)
}

func (e *sbEnv) skill(t testing.TB, name, source string) string {
	t.Helper()
	return e.id(t, `INSERT INTO skills (workspace_id, name, description, type, source, body)
		VALUES (?, ?, 'd', 'rubric', ?, 'body') RETURNING id::text`, e.ws, name, source)
}

func (e *sbEnv) repo(t testing.TB, fullName string) string {
	t.Helper()
	owner, name, _ := strings.Cut(fullName, "/")
	return e.id(t, `INSERT INTO repos (workspace_id, owner, name, full_name) VALUES (?, ?, ?, ?) RETURNING id::text`,
		e.ws, owner, name, fullName)
}

func (e *sbEnv) pull(t testing.TB, repo string, number int, reviewedSHA any) string {
	t.Helper()
	return e.id(t, `INSERT INTO pull_requests (workspace_id, repo_id, number, title, author, branch, base, head_sha, last_reviewed_sha, status)
		VALUES (?, ?, ?, 't', 'a', 'b', 'main', 'head', ?, 'reviewed') RETURNING id::text`, e.ws, repo, number, reviewedSHA)
}

// sbTableCounts counts the rows of every table of the public schema.
func sbTableCounts(t testing.TB, db *gorm.DB) map[string]int64 {
	t.Helper()
	var names []string
	if err := db.Raw(`SELECT table_name FROM information_schema.tables
		WHERE table_schema = 'public' AND table_type = 'BASE TABLE'`).Scan(&names).Error; err != nil {
		t.Fatalf("list tables: %v", err)
	}
	out := map[string]int64{}
	for _, n := range names {
		out[n] = scalar[int64](t, db, `SELECT count(*) FROM "`+n+`"`)
	}
	return out
}

// assertCountsUnchanged fails when any table has another row count than before.
func assertCountsUnchanged(t testing.TB, what string, db *gorm.DB, before map[string]int64) {
	t.Helper()
	if diff := cmp.Diff(before, sbTableCounts(t, db)); diff != "" {
		t.Errorf("%s: table row counts changed (-before +after):\n%s", what, diff)
	}
}

func sbGoldens(t *testing.T, s *stack) *goldens {
	return &goldens{t: t, cloneDir: s.cfg.CloneDir, dataDir: s.cfg.DataDir, ids: map[string]string{}}
}

// workspaceName reads the display name from GET /workspace.
func workspaceName(t testing.TB, s *stack) string {
	t.Helper()
	got := s.get("/workspace")
	if got.Status != http.StatusOK {
		t.Fatalf("GET /workspace = %d %s, want 200", got.Status, got.Body)
	}
	name, _ := got.Obj(t)["name"].(string)
	return name
}

// ---- rename (AC1-AC3) ----

func TestSettingsB_RenameIsReadBack(t *testing.T) { // AC1
	s := startStack(t, spec{})
	before := s.get("/workspace").Obj(t)

	put := s.do(http.MethodPut, "/workspace", map[string]any{"name": "  acme · payments "})
	if put.Status != http.StatusOK {
		t.Fatalf("PUT /workspace = %d %s, want 200", put.Status, put.Body)
	}
	if got := put.Obj(t)["name"]; got != "acme · payments" {
		t.Errorf("PUT /workspace name = %v, want the trimmed acme · payments", got)
	}

	after := s.get("/workspace").Obj(t)
	if after["name"] != "acme · payments" {
		t.Errorf("GET /workspace name = %v, want acme · payments", after["name"])
	}
	dir, _ := after["dataDir"].(string)
	if !strings.HasPrefix(dir, "/") || dir != s.cfg.DataDir {
		t.Errorf("GET /workspace dataDir = %q, want the absolute data directory %q", dir, s.cfg.DataDir)
	}
	for _, key := range []string{"workspaceId", "cloneDir", "repos"} {
		if diff := cmp.Diff(before[key], after[key]); diff != "" {
			t.Errorf("GET /workspace %s changed by the rename (-before +after):\n%s", key, diff)
		}
	}
	if put.Obj(t)["workspaceId"] != before["workspaceId"] {
		t.Errorf("PUT /workspace workspaceId = %v, want %v", put.Obj(t)["workspaceId"], before["workspaceId"])
	}
}

func TestSettingsB_RenameRejectsBadInput(t *testing.T) { // AC2
	s := startStack(t, spec{})
	if put := s.do(http.MethodPut, "/workspace", map[string]any{"name": "Kept"}); put.Status != http.StatusOK {
		t.Fatalf("PUT /workspace = %d %s, want 200", put.Status, put.Body)
	}

	tests := []struct {
		name    string
		body    any
		headers []string
	}{
		{name: "empty", body: map[string]any{"name": ""}},
		{name: "spaces only", body: map[string]any{"name": "   "}},
		{name: "81 characters", body: map[string]any{"name": strings.Repeat("a", 81)}},
		{name: "newline", body: map[string]any{"name": "a\nb"}},
		{name: "number", body: map[string]any{"name": 7}},
		{name: "null", body: map[string]any{"name": nil}},
		{name: "extra key", body: map[string]any{"name": "ok", "extra": 1}},
		{name: "text/plain", body: `{"name":"ok"}`, headers: []string{"Content-Type", "text/plain"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := s.do(http.MethodPut, "/workspace", tc.body, tc.headers...)
			if got.Status != http.StatusUnprocessableEntity || got.errorCode(t) != "validation_error" {
				t.Errorf("PUT /workspace %s = %d %s, want 422 validation_error", tc.name, got.Status, got.Body)
			}
		})
	}
	if name := workspaceName(t, s); name != "Kept" {
		t.Errorf("name after the rejected renames = %q, want Kept", name)
	}

	// The limits themselves are accepted: 80 characters.
	if put := s.do(http.MethodPut, "/workspace", map[string]any{"name": strings.Repeat("é", 80)}); put.Status != http.StatusOK {
		t.Errorf("PUT /workspace with 80 characters = %d %s, want 200", put.Status, put.Body)
	}
}

func TestSettingsB_RenameSurvivesRestartWithSeed(t *testing.T) { // AC3
	db := pgtest.Migrated(t)
	first := startStack(t, spec{db: db})
	info := first.get("/workspace").Obj(t)
	if put := first.do(http.MethodPut, "/workspace", map[string]any{"name": "Renamed"}); put.Status != http.StatusOK {
		t.Fatalf("PUT /workspace = %d %s, want 200", put.Status, put.Body)
	}
	repos, agents := first.get("/repos"), first.get("/agents")
	if repos.Status != http.StatusOK || agents.Status != http.StatusOK {
		t.Fatalf("GET /repos, /agents = %d, %d, want 200", repos.Status, agents.Status)
	}
	if len(repos.JSON(t).([]any)) == 0 || len(agents.JSON(t).([]any)) == 0 {
		t.Fatalf("the seeded workspace has repos %s and agents %s, want both non-empty", repos.Body, agents.Body)
	}
	if err := first.stop(); err != nil {
		t.Fatalf("stop first server: %v", err)
	}

	second := startStack(t, spec{db: db}) // the seed runs again
	if n := scalar[int](t, db.DB, `SELECT count(*) FROM workspaces`); n != 1 {
		t.Errorf("workspaces after the restart = %d, want 1", n)
	}
	again := second.get("/workspace").Obj(t)
	if again["workspaceId"] != info["workspaceId"] || again["name"] != "Renamed" {
		t.Errorf("GET /workspace after the restart = id %v name %v, want id %v name Renamed",
			again["workspaceId"], again["name"], info["workspaceId"])
	}
	if diff := cmp.Diff(repos.JSON(t), second.get("/repos").JSON(t)); diff != "" {
		t.Errorf("GET /repos after the restart (-before +after):\n%s", diff)
	}
	if diff := cmp.Diff(agents.JSON(t), second.get("/agents").JSON(t)); diff != "" {
		t.Errorf("GET /agents after the restart (-before +after):\n%s", diff)
	}
}

// ---- retention setting (AC6) ----

func TestSettingsB_RetentionDaysValidation(t *testing.T) { // AC6
	s := startStack(t, spec{})
	if _, present := s.get("/settings").Obj(t)["retention_days"]; present {
		t.Fatalf("GET /settings before any PUT holds retention_days, want it absent")
	}

	for _, v := range []any{30, 60, 90, 180, 365, nil} {
		if put := putSettings(s, "retention_days", v); put.Status != http.StatusOK {
			t.Fatalf("PUT /settings retention_days %v = %d %s, want 200", v, put.Status, put.Body)
		}
		got, present := s.get("/settings").Obj(t)["retention_days"]
		if !present {
			t.Errorf("GET /settings after retention_days %v lacks the key, want it present", v)
			continue
		}
		var want any
		if v != nil {
			want = float64(v.(int))
		}
		if got != want {
			t.Errorf("GET /settings retention_days after PUT %v = %v, want %v", v, got, want)
		}
	}

	for _, v := range []any{45, 0, "90"} {
		put := putSettings(s, "retention_days", v)
		if put.Status != http.StatusUnprocessableEntity || put.errorCode(t) != "validation_error" {
			t.Errorf("PUT /settings retention_days %#v = %d %s, want 422 validation_error", v, put.Status, put.Body)
		}
	}
	if got, present := s.get("/settings").Obj(t)["retention_days"]; !present || got != nil {
		t.Errorf("GET /settings retention_days after the rejected values = %v (present %v), want the stored null", got, present)
	}
}

// ---- pruned run (AC9) ----

func TestSettingsB_PrunedRunKeepsHistoryAndPerformance(t *testing.T) { // AC9
	e := newSBEnv(t)
	agent := e.agent(t, "Reviewer", true, 0)
	pr := e.pull(t, e.repo(t, "o/r"), 1, nil)
	run := func(ranAt string) string {
		id := e.id(t, `INSERT INTO agent_runs (workspace_id, agent_id, pr_id, status, source, ran_at)
			VALUES (?, ?, ?, 'done', 'local', `+ranAt+`) RETURNING id::text`, e.ws, agent, pr)
		e.exec(t, `INSERT INTO run_traces (run_id, trace) VALUES (?, '{}')`, id)
		return id
	}
	// Retention is unset (90 days). The old run is older than that whatever day the test runs;
	// the clock of the services stands 7 days after it, so it lies in the performance window.
	old := run(`'2026-01-25T12:00:00Z'`)
	recent := run(`now() - interval '1 day'`)

	s := startStack(t, spec{
		db: e.db, noSeed: true, now: func() time.Time { return time.Date(2026, 2, 1, 12, 0, 0, 0, time.UTC) },
	})
	eventually(t, 30*time.Second, "the startup trace prune", func() bool {
		return len(logLines(t, s.logs, "trace prune done")) >= 1
	})

	if got := s.get("/runs/" + old + "/trace"); got.Status != http.StatusNotFound || got.errorCode(t) != "not_found" {
		t.Errorf("GET /runs/%s/trace after the prune = %d %s, want 404 not_found", old, got.Status, got.Body)
	}
	if n := scalar[int](t, e.db.DB, `SELECT count(*) FROM run_traces WHERE run_id = ?`, recent); n != 1 {
		t.Errorf("traces of the recent run after the prune = %d, want 1", n)
	}

	history := s.get("/pulls/" + pr + "/runs")
	if history.Status != http.StatusOK {
		t.Fatalf("GET /pulls/%s/runs = %d %s, want 200", pr, history.Status, history.Body)
	}
	var ids []string
	for _, item := range history.JSON(t).([]any) {
		ids = append(ids, item.(map[string]any)["run_id"].(string))
	}
	if !contains(ids, old) || !contains(ids, recent) {
		t.Errorf("run history ids = %v, want both %s and %s", ids, old, recent)
	}

	perf := s.get("/agents/performance?period=30d")
	if perf.Status != http.StatusOK {
		t.Fatalf("GET /agents/performance = %d %s, want 200", perf.Status, perf.Body)
	}
	summary, _ := perf.Obj(t)["summary"].(map[string]any)
	if summary["runs"] != float64(1) {
		t.Errorf("GET /agents/performance summary.runs = %v, want 1 (the pruned run still counts)", summary["runs"])
	}
}

func contains(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}

// ---- stats (AC11) ----

// seedStats fills the workspace with 3 agents (1 disabled), 6 skills, 12 eval cases, 24 memory
// rows of mixed kinds, 2 repos and 7 runs of mixed source and status, and puts rows of another
// workspace around them that must not count.
func (e *sbEnv) seedStats(t *testing.T) {
	t.Helper()
	e.agent(t, "one", true, 0)
	e.agent(t, "two", true, 1)
	e.agent(t, "three", false, 2)
	for i := 1; i <= 6; i++ {
		e.skill(t, fmt.Sprintf("skill-%d", i), "manual")
	}
	for i := 1; i <= 12; i++ {
		e.exec(t, `INSERT INTO eval_cases (workspace_id, owner_kind, owner_id, name) VALUES (?, 'agent', gen_random_uuid(), ?)`,
			e.ws, fmt.Sprintf("case-%d", i))
	}
	repo := e.repo(t, "o/r1")
	e.repo(t, "o/r2")
	kinds := []string{"decision", "convention", "preference", "fact", "learning", "fact"}
	for i := range 24 {
		if i%3 == 0 {
			e.exec(t, `INSERT INTO memory (workspace_id, repo_id, scope, kind, content) VALUES (?, ?, 'repo', ?, 'c')`, e.ws, repo, kinds[i%len(kinds)])
			continue
		}
		e.exec(t, `INSERT INTO memory (workspace_id, scope, kind, content) VALUES (?, 'global', ?, 'c')`, e.ws, kinds[i%len(kinds)])
	}
	for _, r := range []struct{ source, status string }{
		{"local", "done"}, {"local", "failed"}, {"local", "cancelled"}, {"local", "running"},
		{"ci", "done"}, {"ci", "failed"}, {"local", "done"},
	} {
		e.exec(t, `INSERT INTO agent_runs (workspace_id, source, status) VALUES (?, ?, ?)`, e.ws, r.source, r.status)
	}

	other := e.id(t, `INSERT INTO workspaces (name) VALUES ('other') RETURNING id::text`)
	e.exec(t, `INSERT INTO agents (workspace_id, name, provider, model, system_prompt) VALUES (?, 'x', 'openai', 'm', 'p')`, other)
	e.exec(t, `INSERT INTO skills (workspace_id, name, description, type, source, body) VALUES (?, 'x', 'd', 'rubric', 'manual', 'b')`, other)
	e.exec(t, `INSERT INTO repos (workspace_id, owner, name, full_name) VALUES (?, 'o', 'r1', 'o/r1')`, other)
	e.exec(t, `INSERT INTO memory (workspace_id, scope, kind, content) VALUES (?, 'global', 'fact', 'c')`, other)
	e.exec(t, `INSERT INTO agent_runs (workspace_id, status) VALUES (?, 'done')`, other)
}

func TestSettingsB_StatsCountTheFixture(t *testing.T) { // AC11
	e := newSBEnv(t)
	e.seedStats(t)
	s := e.start(t)

	got := s.get("/workspace/stats")
	want := map[string]any{
		"agents": float64(3), "skills": float64(6), "eval_cases": float64(12),
		"learnings": float64(24), "repos": float64(2), "runs": float64(7),
	}
	if got.Status != http.StatusOK {
		t.Fatalf("GET /workspace/stats = %d %s, want 200", got.Status, got.Body)
	}
	if diff := cmp.Diff(want, got.JSON(t)); diff != "" {
		t.Errorf("GET /workspace/stats mismatch (-want +got):\n%s", diff)
	}
	sbGoldens(t, s).check("workspace_stats", got, http.StatusOK)
}

// ---- export (AC13, AC15) ----

// seedExport fills the workspace with every kind of export record: duplicate agent names, a
// skill with three versions and source imported_file, eval cases of both owner kinds, conventions,
// learnings, context files and settings, plus values that must stay out (an embedding, memory
// sources, an unknown setting, the agent ids of the automatic reviews).
func (e *sbEnv) seedExport(t *testing.T) {
	t.Helper()
	e.agent(t, "Reviewer", true, 10) // inserted first, created second
	agentA1 := e.agent(t, "Reviewer", false, 5)
	agentB := e.agent(t, "Auditor", true, 20)
	e.exec(t, `UPDATE agents SET output_schema = '{"type":"object"}', strategy = 'map-reduce',
		ci_fail_on = 'any', repo_intel = false, description = 'checks things' WHERE id = ?`, agentA1)

	alpha, zeta, imported := e.skill(t, "alpha", "manual"), e.skill(t, "Zeta", "extracted"), e.skill(t, "imported", "imported_file")
	e.exec(t, `UPDATE skills SET evidence_files = '["a.go"]' WHERE id = ?`, imported)
	e.exec(t, `INSERT INTO agent_skills (agent_id, skill_id, "order") VALUES (?, ?, 0), (?, ?, 1)`, agentA1, zeta, agentA1, alpha)
	e.exec(t, `INSERT INTO agent_skills (agent_id, skill_id, "order") VALUES (?, ?, 0)`, agentB, imported)
	for _, v := range []int{3, 1, 2} {
		var desc, msg *string
		if v == 2 {
			d := "desc"
			desc = &d
		}
		if v == 3 {
			m := "msg"
			msg = &m
		}
		e.exec(t, `INSERT INTO skill_versions (skill_id, version, body, created_at, description, message)
			VALUES (?, ?, ?, '2026-01-01T00:00:00Z'::timestamptz + make_interval(mins => ?), ?, ?)`,
			imported, v, fmt.Sprintf("body v%d", v), v, desc, msg)
	}

	e.exec(t, `INSERT INTO eval_cases (workspace_id, owner_kind, owner_id, name, input_diff, input_files,
		input_meta, expected_output, notes, created_at) VALUES
		(?, 'skill', ?, 'b-skill-case', NULL, NULL, NULL, NULL, NULL, '2026-01-01T00:00:00Z'::timestamptz),
		(?, 'agent', ?, 'a-agent-case', 'diff', '[{"path":"x"}]', '{"k":1}', '{"findings":[]}', 'note', '2026-01-01T00:00:01Z'::timestamptz)`,
		e.ws, imported, e.ws, agentA1)

	repoX, repoY := e.repo(t, "org/x"), e.repo(t, "Org/y")
	e.exec(t, `INSERT INTO conventions (workspace_id, repo_id, rule, confidence, category, evidence, status, created_at) VALUES
		(?, ?, 'rule-two-entries', 0.9, 'naming',
			'[{"path":"a.go","start_line":1,"end_line":2,"snippet":"first"},{"path":"b.go","snippet":"second"}]',
			'accepted', '2026-01-01T00:00:00Z'::timestamptz),
		(?, ?, 'rule-empty', NULL, 'other', '[]', 'pending', '2026-01-01T00:00:01Z'::timestamptz),
		(?, ?, 'rule-no-snippet', 0.5, 'style', '[{"path":"c.go"}]', 'rejected', '2026-01-01T00:00:02Z'::timestamptz)`,
		e.ws, repoX, e.ws, repoX, e.ws, repoY)
	e.exec(t, `INSERT INTO memory (workspace_id, repo_id, scope, kind, content, embedding, confidence, sources, created_at) VALUES
		(?, ?, 'repo', 'decision', 'repo learning', array_fill(0.5::real, ARRAY[1536])::vector, 0.7,
			'[{"secret":"source-text"}]', '2026-01-01T00:00:00Z'::timestamptz),
		(?, NULL, 'global', 'fact', 'global learning', NULL, NULL, NULL, '2026-01-01T00:00:01Z'::timestamptz)`,
		e.ws, repoX, e.ws)
	e.exec(t, `INSERT INTO context_files (repo_id, path, content, size_bytes) VALUES
		(?, 'docs/b.md', 'B', 1), (?, 'Docs/a.md', 'A', 1), (?, 'z.md', 'Z', 1)`, repoX, repoX, repoY)

	e.setting(t, "theme", `"dark"`)
	e.setting(t, "retention_days", `30`)
	e.setting(t, "automatic_reviews", fmt.Sprintf(
		`{"enabled":true,"agent_ids":[%q],"enabled_at":"2026-10-01T08:30:00.000Z"}`, agentA1))
	e.setting(t, "leaky_unknown_key", `"sk-LEAK-0123456789"`)
	e.setting(t, "workspace_name", `"stale"`)
}

func TestSettingsB_ExportFixtureMatchesGolden(t *testing.T) { // AC13, AC15
	e := newSBEnv(t)
	e.seedExport(t)
	s := e.start(t)
	g := sbGoldens(t, s)

	g.check("workspace_rename", s.do(http.MethodPut, "/workspace", map[string]any{"name": "  acme · payments "}), http.StatusOK)

	got := s.get("/workspace/export")
	if got.Status != http.StatusOK {
		t.Fatalf("GET /workspace/export = %d %s, want 200", got.Status, got.Body)
	}
	g.check("workspace_export", got, http.StatusOK)

	// AC15: the answer is a download named after the workspace and the day of the clock.
	if ct := got.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("export Content-Type = %q, want application/json", ct)
	}
	const wantDisposition = `attachment; filename="devdigest-workspace-acme-payments-2026-10-23.json"`
	if cd := got.Header.Get("Content-Disposition"); cd != wantDisposition {
		t.Errorf("export Content-Disposition = %q, want %q", cd, wantDisposition)
	}

	// The golden normalises timestamps, so the exported instant is asserted here.
	var bundle struct {
		Manifest struct {
			Name       string         `json:"name"`
			Kind       string         `json:"kind"`
			Format     string         `json:"format"`
			ExportedAt string         `json:"exported_at"`
			Counts     map[string]int `json:"counts"`
		} `json:"manifest"`
		Settings map[string]json.RawMessage `json:"settings"`
	}
	if err := json.Unmarshal(got.Body, &bundle); err != nil {
		t.Fatalf("export body is not JSON: %v", err)
	}
	m := bundle.Manifest
	if m.Name != "acme · payments" || m.Kind != "workspace" || m.Format != "devdigest-plugin/v1" || m.ExportedAt != "2026-10-23T12:00:00.000Z" {
		t.Errorf("export manifest = %+v, want the workspace name, kind workspace, format devdigest-plugin/v1, exported at 2026-10-23T12:00:00.000Z", m)
	}
	wantCounts := map[string]int{"agents": 3, "skills": 3, "eval_cases": 2, "conventions": 3, "learnings": 2, "context_files": 3}
	if diff := cmp.Diff(wantCounts, m.Counts); diff != "" {
		t.Errorf("export manifest counts mismatch (-want +got):\n%s", diff)
	}
	for _, key := range []string{"leaky_unknown_key", "workspace_name"} {
		if _, ok := bundle.Settings[key]; ok {
			t.Errorf("export settings hold %q, want only the allow-listed keys", key)
		}
	}
	if raw := string(got.Body); strings.Contains(raw, "agent_ids") || strings.Contains(raw, "enabled_at") {
		t.Errorf("export holds agent_ids or enabled_at of the automatic reviews (D26)")
	}
}

func TestSettingsB_ExportOfAnEmptyWorkspace(t *testing.T) { // AC15
	e := newSBEnv(t)
	s := e.start(t)

	got := s.get("/workspace/export")
	if got.Status != http.StatusOK {
		t.Fatalf("GET /workspace/export = %d %s, want 200", got.Status, got.Body)
	}
	const wantDisposition = `attachment; filename="devdigest-workspace-default-2026-10-23.json"`
	if cd := got.Header.Get("Content-Disposition"); cd != wantDisposition {
		t.Errorf("export Content-Disposition = %q, want %q", cd, wantDisposition)
	}
	body := got.Obj(t)
	counts, _ := body["manifest"].(map[string]any)["counts"].(map[string]any)
	for _, key := range []string{"agents", "skills", "eval_cases", "conventions", "learnings", "context_files"} {
		if counts[key] != float64(0) {
			t.Errorf("empty export counts.%s = %v, want 0", key, counts[key])
		}
		list, ok := body[key].([]any)
		if !ok || len(list) != 0 {
			t.Errorf("empty export %s = %v, want []", key, body[key])
		}
	}
	if settings, ok := body["settings"].(map[string]any); !ok || len(settings) != 0 {
		t.Errorf("empty export settings = %v, want {}", body["settings"])
	}
}

// ---- reset: refusals (AC17) ----

// seedHistory fills the workspace with every kind of row a reset deletes and the configuration it
// keeps; it returns what the refusal tests need.
func (e *sbEnv) seedHistory(t *testing.T) (agent string) {
	t.Helper()
	repo := e.repo(t, "o/r")
	pr1, pr2 := e.pull(t, repo, 1, "sha1"), e.pull(t, repo, 2, "sha2")
	agent = e.agent(t, "a", true, 0)
	skill := e.skill(t, "s", "manual")
	e.exec(t, `INSERT INTO agent_skills (agent_id, skill_id) VALUES (?, ?)`, agent, skill)
	inst := e.id(t, `INSERT INTO ci_installations (agent_id, repo, target_type) VALUES (?, 'o/r', 'gha') RETURNING id::text`, agent)
	evalCase := e.id(t, `INSERT INTO eval_cases (workspace_id, owner_kind, owner_id, name) VALUES (?, 'agent', ?, 'c') RETURNING id::text`, e.ws, agent)

	multi := e.id(t, `INSERT INTO multi_agent_runs (workspace_id, pr_id) VALUES (?, ?) RETURNING id::text`, e.ws, pr1)
	run1 := e.id(t, `INSERT INTO agent_runs (workspace_id, agent_id, pr_id, status, source, multi_agent_run_id)
		VALUES (?, ?, ?, 'done', 'local', ?) RETURNING id::text`, e.ws, agent, pr1, multi)
	run2 := e.id(t, `INSERT INTO agent_runs (workspace_id, agent_id, pr_id, status, source)
		VALUES (?, ?, ?, 'done', 'ci') RETURNING id::text`, e.ws, agent, pr2)
	e.exec(t, `INSERT INTO run_traces (run_id, trace) VALUES (?, '{}'), (?, '{}')`, run1, run2)
	e.exec(t, `INSERT INTO agent_run_skills (run_id, skill_id, skill_version, "order") VALUES (?, ?, 1, 0)`, run1, skill)

	review := func(run any) string {
		return e.id(t, `INSERT INTO reviews (workspace_id, pr_id, kind, run_id) VALUES (?, ?, 'review', ?) RETURNING id::text`, e.ws, pr1, run)
	}
	review1, _, review3 := review(run1), review(run2), review(nil)
	for _, rev := range []string{review1, review1, review3} {
		e.exec(t, `INSERT INTO findings (review_id, file, start_line, end_line, severity, category, title, rationale, confidence)
			VALUES (?, 'f.go', 1, 2, 'WARNING', 'bug', 't', 'r', 0.5)`, rev)
	}

	e.exec(t, `INSERT INTO ci_runs (ci_installation_id, agent_run_id, status) VALUES (?, ?, 'done'), (?, NULL, 'done')`, inst, run2, inst)
	e.exec(t, `INSERT INTO ci_runs (ci_installation_id, agent_run_id, status) VALUES (NULL, ?, 'done')`, run1)
	suite := e.id(t, `INSERT INTO eval_suite_runs (workspace_id, agent_id, agent_version, config, case_ids, status)
		VALUES (?, ?, 1, '{}', '[]', 'completed') RETURNING id::text`, e.ws, agent)
	e.exec(t, `INSERT INTO eval_runs (case_id, run_id, status) VALUES (?, ?, 'passed'), (?, NULL, 'failed')`, evalCase, suite, evalCase)
	e.exec(t, `INSERT INTO composed_reviews (pr_id, body) VALUES (?, 'b')`, pr1)
	e.exec(t, `INSERT INTO memory (workspace_id, repo_id, scope, kind, content) VALUES (?, ?, 'repo', 'fact', 'c1'), (?, NULL, 'global', 'fact', 'c2')`, e.ws, repo, e.ws)
	return agent
}

func TestSettingsB_ResetRefusalsDeleteNothing(t *testing.T) { // AC17
	e := newSBEnv(t)
	agent := e.seedHistory(t)
	s := e.start(t)
	g := sbGoldens(t, s)
	if put := s.do(http.MethodPut, "/workspace", map[string]any{"name": "Acme Corp"}); put.Status != http.StatusOK {
		t.Fatalf("PUT /workspace = %d %s, want 200", put.Status, put.Body)
	}
	before := sbTableCounts(t, e.db.DB)

	reset := func(body any) response { return s.post("/workspace/reset-history", body) }
	t.Run("bad body", func(t *testing.T) {
		for _, body := range []any{
			map[string]any{}, map[string]any{"confirm": 7}, map[string]any{"confirm": nil},
			map[string]any{"confirm": "Acme Corp", "extra": 1}, `[]`,
		} {
			if got := reset(body); got.Status != http.StatusUnprocessableEntity || got.errorCode(t) != "validation_error" {
				t.Errorf("POST /workspace/reset-history %v = %d %s, want 422 validation_error", body, got.Status, got.Body)
			}
		}
		if got := s.do(http.MethodPost, "/workspace/reset-history", `{"confirm":"Acme Corp"}`, "Content-Type", "text/plain"); got.Status != http.StatusUnprocessableEntity {
			t.Errorf("POST /workspace/reset-history as text/plain = %d %s, want 422", got.Status, got.Body)
		}
		assertCountsUnchanged(t, "bad body", e.db.DB, before)
	})

	t.Run("wrong case, untrimmed and stale name", func(t *testing.T) {
		for _, confirm := range []string{"acme corp", "ACME CORP", "Acme Corp ", " Acme Corp", "", "default"} {
			got := reset(map[string]any{"confirm": confirm})
			if got.Status != http.StatusUnprocessableEntity || got.errorCode(t) != "confirmation_mismatch" {
				t.Errorf("POST /workspace/reset-history confirm %q = %d %s, want 422 confirmation_mismatch", confirm, got.Status, got.Body)
			}
		}
		// A name that was valid before a rename is stale.
		if put := s.do(http.MethodPut, "/workspace", map[string]any{"name": "Acme Corp 2"}); put.Status != http.StatusOK {
			t.Fatalf("PUT /workspace = %d %s, want 200", put.Status, put.Body)
		}
		if got := reset(map[string]any{"confirm": "Acme Corp"}); got.Status != http.StatusUnprocessableEntity || got.errorCode(t) != "confirmation_mismatch" {
			t.Errorf("POST /workspace/reset-history with the stale name = %d %s, want 422 confirmation_mismatch", got.Status, got.Body)
		}
		assertCountsUnchanged(t, "mismatch", e.db.DB, before)
	})

	t.Run("a running agent run", func(t *testing.T) {
		e.exec(t, `INSERT INTO agent_runs (workspace_id, agent_id, status) VALUES (?, ?, 'running')`, e.ws, agent)
		withRun := sbTableCounts(t, e.db.DB)
		got := reset(map[string]any{"confirm": "Acme Corp 2"})
		g.check("workspace_reset_run_active", got, http.StatusConflict)
		if got.errorCode(t) != "run_active" {
			t.Errorf("POST /workspace/reset-history with a running run = %s, want code run_active", got.Body)
		}
		if msg, _ := got.Obj(t)["error"].(map[string]any)["message"].(string); !strings.HasPrefix(msg, "1 runs are in progress") {
			t.Errorf("run_active message = %q, want it to name the count (1 runs are in progress …)", msg)
		}
		assertCountsUnchanged(t, "running agent run", e.db.DB, withRun)
		e.exec(t, `DELETE FROM agent_runs WHERE status = 'running'`)
	})

	t.Run("a running eval suite run", func(t *testing.T) {
		e.exec(t, `INSERT INTO eval_suite_runs (workspace_id, agent_id, agent_version, config, case_ids, status)
			VALUES (?, ?, 1, '{}', '[]', 'running')`, e.ws, agent)
		withRun := sbTableCounts(t, e.db.DB)
		got := reset(map[string]any{"confirm": "Acme Corp 2"})
		if got.Status != http.StatusConflict || got.errorCode(t) != "run_active" {
			t.Errorf("POST /workspace/reset-history with a running suite run = %d %s, want 409 run_active", got.Status, got.Body)
		}
		assertCountsUnchanged(t, "running eval suite run", e.db.DB, withRun)
		e.exec(t, `DELETE FROM eval_suite_runs WHERE status = 'running'`)
	})

	// The refusals left the workspace as it was: the exact name now resets it.
	assertCountsUnchanged(t, "after the refusals", e.db.DB, before)
	done := reset(map[string]any{"confirm": "Acme Corp 2"})
	g.check("workspace_reset", done, http.StatusOK)
	deleted, _ := done.Obj(t)["deleted"].(map[string]any)
	want := map[string]any{
		"agent_runs": float64(2), "run_traces": float64(2), "reviews": float64(3), "findings": float64(3),
		"multi_agent_runs": float64(1), "eval_suite_runs": float64(1), "eval_runs": float64(2), "ci_runs": float64(3),
		"composed_reviews": float64(1), "learnings": float64(2),
	}
	if diff := cmp.Diff(want, deleted); diff != "" {
		t.Errorf("POST /workspace/reset-history deleted mismatch (-want +got):\n%s", diff)
	}
	if done.Obj(t)["prs_unmarked"] != float64(2) {
		t.Errorf("POST /workspace/reset-history prs_unmarked = %v, want 2", done.Obj(t)["prs_unmarked"])
	}
}

// ---- guards (AC19) ----

func TestSettingsB_ForeignOriginAndHostAreRefused(t *testing.T) { // AC19
	e := newSBEnv(t)
	e.seedHistory(t)
	e.setting(t, "workspace_name", `"Acme Corp"`)
	s := e.start(t)
	before := sbTableCounts(t, e.db.DB)

	const evil = "http://evil.example"
	wantOrigin := map[string]any{"error": map[string]any{"code": "forbidden", "message": "Origin not allowed"}}
	for _, req := range []struct {
		method, path string
		body         any
	}{
		{http.MethodPut, "/workspace", map[string]any{"name": "Hijacked"}},
		{http.MethodPost, "/workspace/reset-history", map[string]any{"confirm": "Acme Corp"}},
	} {
		got := s.do(req.method, req.path, req.body, "Origin", evil)
		if got.Status != http.StatusForbidden {
			t.Errorf("%s %s with a foreign Origin = %d %s, want 403", req.method, req.path, got.Status, got.Body)
			continue
		}
		if diff := cmp.Diff(wantOrigin, got.JSON(t)); diff != "" {
			t.Errorf("%s %s with a foreign Origin body mismatch (-want +got):\n%s", req.method, req.path, diff)
		}
	}
	if name := workspaceName(t, s); name != "Acme Corp" {
		t.Errorf("name after the refused rename = %q, want Acme Corp", name)
	}
	assertCountsUnchanged(t, "foreign Origin", e.db.DB, before)

	wantHost := map[string]any{"error": map[string]any{"code": "forbidden", "message": "Host not allowed"}}
	for _, req := range []struct {
		method, path string
		body         any
	}{
		{http.MethodGet, "/workspace", nil},
		{http.MethodPut, "/workspace", map[string]any{"name": "Hijacked"}},
		{http.MethodGet, "/workspace/stats", nil},
		{http.MethodGet, "/workspace/export", nil},
		{http.MethodPost, "/workspace/reset-history", map[string]any{"confirm": "Acme Corp"}},
	} {
		got := s.do(req.method, req.path, req.body, "Host", "evil.example")
		if got.Status != http.StatusForbidden {
			t.Errorf("%s %s with a foreign Host = %d %s, want 403", req.method, req.path, got.Status, got.Body)
			continue
		}
		if diff := cmp.Diff(wantHost, got.JSON(t)); diff != "" {
			t.Errorf("%s %s with a foreign Host body mismatch (-want +got):\n%s", req.method, req.path, diff)
		}
	}
	assertCountsUnchanged(t, "foreign Host", e.db.DB, before)

	// The UI's own origin works, and the reset leaves one log line that holds counts only.
	ok := s.do(http.MethodPost, "/workspace/reset-history", map[string]any{"confirm": "Acme Corp"}, "Origin", "http://localhost:3100")
	if ok.Status != http.StatusOK {
		t.Fatalf("POST /workspace/reset-history from the UI origin = %d %s, want 200", ok.Status, ok.Body)
	}
	lines := logLines(t, s.logs, "workspace run history reset")
	if len(lines) != 1 {
		t.Fatalf("reset log lines = %v, want exactly one", lines)
	}
	for key, value := range lines[0] {
		switch key {
		case "time", "level", "msg":
		default:
			if _, isNumber := value.(float64); !isNumber {
				t.Errorf("reset log attribute %s = %v (%T), want a count", key, value, value)
			}
		}
	}
	if logs := s.logs.String(); strings.Contains(logs, "Acme Corp") {
		t.Errorf("server log holds the workspace name typed to confirm the reset")
	}
}

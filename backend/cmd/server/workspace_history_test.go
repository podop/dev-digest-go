package main

import (
	"bytes"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/platform/postgres/pgtest"
	"dev-digest-go/backend/internal/platform/secrets"
)

// Component tests of the workspace run history through the real server: what the export must
// never carry (AC14, NFR1), what a reset deletes and keeps (AC16), and the startup trace prune
// with its retention rules (AC7, AC8). The active-run refusal (409 run_active) is S17's.

// historyNow is the fake clock of the export and reset tests: a month far from the seeded demo
// data, which is dated near the real now.
var historyNow = time.Date(2031, 3, 4, 5, 6, 7, 0, time.UTC)

func historyClock() time.Time { return historyNow }

// Distinctive values for the leak test: each one must be found nowhere in the export.
const (
	leakOpenAIKey     = "sk-leaktest-openai-0123456789abcdef"
	leakAnthropicKey  = "sk-ant-leaktest-0123456789abcdef"
	leakOpenRouterKey = "sk-or-leaktest-0123456789abcdef"
	leakGitHubToken   = "ghp_leaktestToken0123456789abcdef"
	leakUnknownKey    = "zz_unknown_setting_key"
	leakUnknownValue  = "ghp_tokenUnderAnUnknownKey0123456789"

	leakRunError     = "RUN-ERROR-MARKER-4c1f"
	leakRunGrounding = "RUN-GROUNDING-MARKER-9a7e"
	leakTrace        = "RUN-TRACE-MARKER-31d8"
	leakReview       = "REVIEW-SUMMARY-MARKER-b52a"
	leakFindingTitle = "FINDING-TITLE-MARKER-77e0"
	leakFindingWhy   = "FINDING-RATIONALE-MARKER-c0de"
	leakFindingFix   = "FINDING-SUGGESTION-MARKER-1b3d"
	leakFindingFile  = "FINDING-FILE-MARKER-5e5e.go"
	leakLearning     = "LEARNING-CONTENT-KEPT-IN-EXPORT"
)

// historyIDs are the rows seedRunHistory inserted.
type historyIDs struct {
	ws, repo, pr, agent, run, ciRun, review, finding, findingCase string
}

// seedRunHistory adds every kind of row a reset deletes (FR16) to the seeded workspace, plus a
// case built from a finding and two learnings. Row texts carry the leak markers.
func seedRunHistory(t testing.TB, s *stack) historyIDs {
	t.Helper()
	db := s.db.DB
	exec := func(sql string, args ...any) {
		t.Helper()
		if err := db.Exec(sql, args...).Error; err != nil {
			t.Fatalf("seed %.60s: %v", sql, err)
		}
	}
	id := historyIDs{ws: workspaceID(t, s), pr: pr482(t, s)}
	id.repo = scalar[string](t, db, `SELECT repo_id::text FROM pull_requests WHERE id = ?`, id.pr)

	id.agent = scalar[string](t, db, `INSERT INTO agents (workspace_id, name, provider, model, system_prompt)
		VALUES (?, 'History Agent', 'openai', 'm', 'p') RETURNING id::text`, id.ws)
	skill := scalar[string](t, db, `INSERT INTO skills (workspace_id, name, description, type, source, body)
		VALUES (?, 'history-skill', 'd', 'custom', 'manual', 'b') RETURNING id::text`, id.ws)
	exec(`INSERT INTO agent_skills (agent_id, skill_id) VALUES (?, ?)`, id.agent, skill)

	multi := scalar[string](t, db, `INSERT INTO multi_agent_runs (workspace_id, pr_id) VALUES (?, ?) RETURNING id::text`, id.ws, id.pr)
	id.run = scalar[string](t, db, `INSERT INTO agent_runs (workspace_id, agent_id, pr_id, status, source, multi_agent_run_id, error, grounding)
		VALUES (?, ?, ?, 'done', 'local', ?, ?, ?) RETURNING id::text`, id.ws, id.agent, id.pr, multi, leakRunError, leakRunGrounding)
	ciAgentRun := scalar[string](t, db, `INSERT INTO agent_runs (workspace_id, agent_id, pr_id, status, source)
		VALUES (?, ?, ?, 'done', 'ci') RETURNING id::text`, id.ws, id.agent, id.pr)
	exec(`INSERT INTO run_traces (run_id, trace) VALUES (?, ?::jsonb), (?, '{}')`, id.run, `{"note":"`+leakTrace+`"}`, ciAgentRun)
	exec(`INSERT INTO agent_run_skills (run_id, skill_id, skill_version, "order") VALUES (?, ?, 1, 0)`, id.run, skill)

	id.review = scalar[string](t, db, `INSERT INTO reviews (workspace_id, pr_id, agent_id, kind, summary, run_id)
		VALUES (?, ?, ?, 'review', ?, ?) RETURNING id::text`, id.ws, id.pr, id.agent, leakReview, id.run)
	exec(`INSERT INTO reviews (workspace_id, pr_id, kind, summary) VALUES (?, ?, 'review', 'a review without a run')`, id.ws, id.pr)
	id.finding = scalar[string](t, db, `INSERT INTO findings (review_id, file, start_line, end_line, severity, category, title, rationale, suggestion, confidence)
		VALUES (?, ?, 1, 2, 'WARNING', 'bug', ?, ?, ?, 0.5) RETURNING id::text`, id.review, leakFindingFile, leakFindingTitle, leakFindingWhy, leakFindingFix)

	inst := scalar[string](t, db, `INSERT INTO ci_installations (agent_id, repo, target_type) VALUES (?, 'acme/payments-api', 'gha') RETURNING id::text`, id.agent)
	id.ciRun = scalar[string](t, db, `INSERT INTO ci_runs (ci_installation_id, agent_run_id, status) VALUES (?, ?, 'done') RETURNING id::text`, inst, ciAgentRun)
	exec(`INSERT INTO ci_runs (ci_installation_id, status) VALUES (?, 'done')`, inst)

	evalCase := scalar[string](t, db, `INSERT INTO eval_cases (workspace_id, owner_kind, owner_id, name, input_diff, input_files, input_meta, expected_output)
		VALUES (?, 'agent', ?, 'history case', 'diff', '[]', '{}', '{"kind":"finding","file":"f.go","start_line":1,"end_line":2}') RETURNING id::text`, id.ws, id.agent)
	id.findingCase = scalar[string](t, db, `INSERT INTO eval_cases (workspace_id, owner_kind, owner_id, name, input_diff, input_files, input_meta, expected_output,
			source_finding_id, source_review_id, source_pr_id)
		VALUES (?, 'agent', ?, 'case from a finding', 'diff', '[]', '{}', '{"kind":"finding","file":"f.go","start_line":1,"end_line":2}', ?, ?, ?) RETURNING id::text`,
		id.ws, id.agent, id.finding, id.review, id.pr)
	suite := scalar[string](t, db, `INSERT INTO eval_suite_runs (workspace_id, agent_id, agent_version, config, case_ids, status)
		VALUES (?, ?, 1, '{}', '[]', 'completed') RETURNING id::text`, id.ws, id.agent)
	exec(`INSERT INTO eval_runs (case_id, run_id, status) VALUES (?, ?, 'passed'), (?, NULL, 'failed')`, evalCase, suite, evalCase)

	exec(`INSERT INTO composed_reviews (pr_id, body) VALUES (?, 'composed body')`, id.pr)
	exec(`INSERT INTO memory (workspace_id, repo_id, scope, kind, content) VALUES (?, ?, 'repo', 'fact', ?), (?, NULL, 'global', 'fact', 'second learning')`,
		id.ws, id.repo, leakLearning, id.ws)
	exec(`UPDATE pull_requests SET last_reviewed_sha = 'abc123' WHERE id = ?`, id.pr)
	return id
}

// uuidValues lists every uuid held by any column of any table: the "row UUIDs" of AC14.
func uuidValues(t testing.TB, s *stack) []string {
	t.Helper()
	var cols []struct{ TableName, ColumnName string }
	if err := s.db.DB.Raw(`SELECT c.table_name, c.column_name FROM information_schema.columns c
		JOIN information_schema.tables t ON t.table_schema = c.table_schema AND t.table_name = c.table_name
		WHERE c.table_schema = 'public' AND t.table_type = 'BASE TABLE' AND c.data_type = 'uuid'`).Scan(&cols).Error; err != nil {
		t.Fatalf("list uuid columns: %v", err)
	}
	seen := map[string]bool{}
	for _, c := range cols {
		var vals []string
		q := fmt.Sprintf(`SELECT DISTINCT "%s"::text FROM "%s" WHERE "%s" IS NOT NULL`, c.ColumnName, c.TableName, c.ColumnName)
		if err := s.db.DB.Raw(q).Scan(&vals).Error; err != nil {
			t.Fatalf("read %s.%s: %v", c.TableName, c.ColumnName, err)
		}
		for _, v := range vals {
			seen[v] = true
		}
	}
	out := make([]string, 0, len(seen))
	for v := range seen {
		out = append(out, v)
	}
	slices.Sort(out)
	return out
}

// secretsJSON is a secrets file holding the four secrets.
func secretsJSON() string {
	return fmt.Sprintf(`{"OPENAI_API_KEY":%q,"ANTHROPIC_API_KEY":%q,"OPENROUTER_API_KEY":%q,"GITHUB_TOKEN":%q}`,
		leakOpenAIKey, leakAnthropicKey, leakOpenRouterKey, leakGitHubToken)
}

// AC14, NFR1, FR14, D25, D26: the export holds no secret, unknown setting, path, row id or text of
// a run, review or finding.
func TestWorkspaceHistory_ExportLeaksNothing(t *testing.T) {
	s := startStack(t, spec{now: historyClock, secretFile: secretsJSON()})
	id := seedRunHistory(t, s)
	db := s.db.DB

	clonePath := filepath.Join(s.cfg.CloneDir, "CLONE-PATH-MARKER-2d9c", "acme", "payments-api")
	if err := db.Exec(`UPDATE repos SET clone_path = ? WHERE id = ?`, clonePath, id.repo).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`INSERT INTO settings (workspace_id, key, value) VALUES (?, ?, ?::jsonb), (?, 'theme', '"dark"'), (?, 'automatic_reviews', ?::jsonb)`,
		id.ws, leakUnknownKey, `"`+leakUnknownValue+`"`, id.ws, id.ws,
		fmt.Sprintf(`{"enabled":true,"agent_ids":[%q],"enabled_at":"2031-01-01T00:00:00.000Z"}`, id.agent)).Error; err != nil {
		t.Fatal(err)
	}

	got := s.get("/workspace/export")
	if got.Status != http.StatusOK {
		t.Fatalf("GET /workspace/export = %d %s, want 200", got.Status, got.Body)
	}
	body := string(got.Body)

	forbidden := map[string]string{
		"OpenAI key": leakOpenAIKey, "Anthropic key": leakAnthropicKey, "OpenRouter key": leakOpenRouterKey,
		"GitHub token": leakGitHubToken, "unknown settings key": leakUnknownKey, "value of the unknown key": leakUnknownValue,
		"data dir": s.cfg.DataDir, "clone dir": s.cfg.CloneDir, "clone path": clonePath, "clone path marker": "CLONE-PATH-MARKER-2d9c",
		"run error": leakRunError, "run grounding": leakRunGrounding, "run trace": leakTrace, "review summary": leakReview,
		"finding title": leakFindingTitle, "finding rationale": leakFindingWhy, "finding suggestion": leakFindingFix, "finding file": leakFindingFile,
	}
	for what, needle := range forbidden {
		if strings.Contains(body, needle) {
			t.Errorf("GET /workspace/export contains the %s %q, want it absent", what, needle)
		}
	}
	uuids := uuidValues(t, s)
	if len(uuids) < 20 {
		t.Fatalf("collected %d row UUIDs, want a database full of them", len(uuids))
	}
	for _, u := range uuids {
		if strings.Contains(body, u) {
			t.Errorf("GET /workspace/export contains the row UUID %s, want none", u)
		}
	}

	// The same values must not leak through the other workspace responses either (NFR1).
	for _, path := range []string{"/workspace", "/workspace/stats"} {
		other := string(s.get(path).Body)
		for _, secret := range []string{leakOpenAIKey, leakAnthropicKey, leakOpenRouterKey, leakGitHubToken, leakUnknownValue} {
			if strings.Contains(other, secret) {
				t.Errorf("GET %s contains the secret %q, want it absent", path, secret)
			}
		}
	}

	// The control: the export is not empty, and the allow-listed settings are in it (D26).
	bundle := got.Obj(t)
	manifest, _ := bundle["manifest"].(map[string]any)
	if manifest["kind"] != "workspace" || manifest["exported_at"] != "2031-03-04T05:06:07.000Z" {
		t.Errorf("manifest kind/exported_at = %v/%v, want workspace/2031-03-04T05:06:07.000Z (fixed clock)", manifest["kind"], manifest["exported_at"])
	}
	if !strings.Contains(body, "History Agent") || !strings.Contains(body, leakLearning) {
		t.Errorf("export lacks the planted agent or learning, so the absence checks above prove nothing: %.300s", body)
	}
	settings, _ := bundle["settings"].(map[string]any)
	if settings["theme"] != "dark" {
		t.Errorf("export settings[theme] = %v, want dark", settings["theme"])
	}
	if _, ok := settings[leakUnknownKey]; ok {
		t.Errorf("export settings holds the unknown key %q, want it dropped", leakUnknownKey)
	}
	auto, ok := settings["automatic_reviews"].(map[string]any)
	if !ok {
		t.Fatalf("export settings[automatic_reviews] = %v, want an object without ids", settings["automatic_reviews"])
	}
	if diff := cmp.Diff(map[string]any{"enabled": true}, auto); diff != "" {
		t.Errorf("export automatic_reviews mismatch (-want +got), want no agent_ids and no enabled_at:\n%s", diff)
	}
}

// historyKindCounts is the number of rows of every kind a reset deletes (workspace-wide: the
// seeded database has one workspace), keyed by the response field that counts it.
func historyKindCounts(t testing.TB, s *stack) map[string]float64 {
	t.Helper()
	c := func(table string) float64 { return float64(scalar[int64](t, s.db.DB, `SELECT count(*) FROM `+table)) }
	return map[string]float64{
		"agent_runs": c("agent_runs"), "run_traces": c("run_traces"), "reviews": c("reviews"), "findings": c("findings"),
		"multi_agent_runs": c("multi_agent_runs"), "eval_suite_runs": c("eval_suite_runs"), "eval_runs": c("eval_runs"),
		"ci_runs": c("ci_runs"), "composed_reviews": c("composed_reviews"), "learnings": c("memory"),
	}
}

// historyTables maps each deleted kind of the response to the tables it empties.
var historyTables = []string{
	"agent_runs", "run_traces", "agent_run_skills", "reviews", "findings", "multi_agent_runs",
	"eval_suite_runs", "eval_runs", "ci_runs", "composed_reviews", "memory",
}

func tableCounts(t testing.TB, s *stack) map[string]int64 {
	t.Helper()
	var names []string
	if err := s.db.DB.Raw(`SELECT table_name FROM information_schema.tables
		WHERE table_schema = 'public' AND table_type = 'BASE TABLE'`).Scan(&names).Error; err != nil {
		t.Fatalf("list tables: %v", err)
	}
	out := make(map[string]int64, len(names))
	for _, n := range names {
		out[n] = scalar[int64](t, s.db.DB, `SELECT count(*) FROM "`+n+`"`)
	}
	return out
}

// AC16, FR16, D22, EC6, EC7, NFR1: a reset with real history deletes every kind (learnings
// too), keeps every other table and the secrets file byte for byte, and a second reset is zeros.
func TestWorkspaceHistory_ResetDeletesHistoryAndKeepsTheRest(t *testing.T) {
	s := startStack(t, spec{now: historyClock, secretFile: secretsJSON()})
	id := seedRunHistory(t, s)
	secretsPath := filepath.Join(s.cfg.DataDir, secrets.FileName)
	secretsBefore, err := os.ReadFile(secretsPath)
	if err != nil {
		t.Fatal(err)
	}

	wantDeleted := historyKindCounts(t, s)
	for kind, n := range wantDeleted {
		if n == 0 {
			t.Fatalf("the fixture has no %s row, so the reset test would prove nothing for that kind", kind)
		}
	}
	reviewedPRs := scalar[int64](t, s.db.DB, `SELECT count(*) FROM pull_requests WHERE last_reviewed_sha IS NOT NULL`)
	if reviewedPRs == 0 {
		t.Fatal("the fixture has no reviewed PR")
	}
	before := tableCounts(t, s)

	// A wrong confirmation deletes nothing.
	bad := s.post("/workspace/reset-history", map[string]any{"confirm": "Default"})
	if bad.Status != http.StatusUnprocessableEntity || bad.errorCode(t) != "confirmation_mismatch" {
		t.Fatalf("reset with the wrong case = %d %s, want 422 confirmation_mismatch", bad.Status, bad.Body)
	}
	if diff := cmp.Diff(before, tableCounts(t, s)); diff != "" {
		t.Fatalf("row counts changed by a refused reset (-before +after):\n%s", diff)
	}

	got := s.post("/workspace/reset-history", map[string]any{"confirm": "default"})
	if got.Status != http.StatusOK {
		t.Fatalf("POST /workspace/reset-history = %d %s, want 200", got.Status, got.Body)
	}
	body := got.Obj(t)
	deleted, _ := body["deleted"].(map[string]any)
	gotDeleted := map[string]float64{}
	for k, v := range deleted {
		gotDeleted[k], _ = v.(float64)
	}
	if diff := cmp.Diff(wantDeleted, gotDeleted); diff != "" {
		t.Errorf("reset deleted counts mismatch (-want +got):\n%s", diff)
	}
	if body["prs_unmarked"] != float64(reviewedPRs) {
		t.Errorf("reset prs_unmarked = %v, want %d", body["prs_unmarked"], reviewedPRs)
	}

	// Every deleted kind is gone; every other table is unchanged.
	after := tableCounts(t, s)
	for _, table := range historyTables {
		if after[table] != 0 {
			t.Errorf("table %s holds %d rows after the reset, want 0", table, after[table])
		}
	}
	for table, n := range before {
		if slices.Contains(historyTables, table) {
			continue
		}
		if after[table] != n {
			t.Errorf("kept table %s has %d rows after the reset, want %d", table, after[table], n)
		}
	}
	if n := scalar[int64](t, s.db.DB, `SELECT count(*) FROM pull_requests WHERE last_reviewed_sha IS NOT NULL`); n != 0 {
		t.Errorf("%d PRs keep last_reviewed_sha after the reset, want 0", n)
	}

	// NFR1: the secrets file is untouched.
	secretsAfter, err := os.ReadFile(secretsPath)
	if err != nil {
		t.Fatalf("read %s after the reset: %v", secrets.FileName, err)
	}
	if !bytes.Equal(secretsBefore, secretsAfter) {
		t.Errorf("%s changed by the reset:\nbefore %q\nafter  %q", secrets.FileName, secretsBefore, secretsAfter)
	}

	// EC6: the case built from a finding still reads, though its finding and review are gone.
	cases := s.get("/agents/" + id.agent + "/eval-cases")
	if cases.Status != http.StatusOK {
		t.Fatalf("GET /agents/%s/eval-cases after the reset = %d %s, want 200", id.agent, cases.Status, cases.Body)
	}
	found := false
	list, _ := cases.Obj(t)["cases"].([]any)
	for _, c := range list {
		if m, _ := c.(map[string]any); m["id"] == id.findingCase {
			found = m["source_finding_id"] == id.finding
		}
	}
	if !found {
		t.Errorf("the case from a finding (%s) is not listed with its source finding %s after the reset: %s", id.findingCase, id.finding, cases.Body)
	}

	// EC7: the second reset deletes nothing.
	again := s.post("/workspace/reset-history", map[string]any{"confirm": "default"})
	if again.Status != http.StatusOK {
		t.Fatalf("second reset = %d %s, want 200", again.Status, again.Body)
	}
	zero := map[string]any{
		"deleted": map[string]any{
			"agent_runs": 0.0, "run_traces": 0.0, "reviews": 0.0, "findings": 0.0, "multi_agent_runs": 0.0,
			"eval_suite_runs": 0.0, "eval_runs": 0.0, "ci_runs": 0.0, "composed_reviews": 0.0, "learnings": 0.0,
		},
		"prs_unmarked": 0.0,
	}
	if diff := cmp.Diff(zero, again.Obj(t)); diff != "" {
		t.Errorf("second reset mismatch (-want +got):\n%s", diff)
	}
}

// pruneRun is one agent run of the prune fixture, aged in days.
type pruneRun struct {
	ageDays int
	id      string
}

// pruneAges are the ages of the fixture runs: both sides of 30 and of 90 days, and far beyond.
// The prune runs on the real clock (a moved test clock would shift its cutoff), so the margins
// are days; the exact cutoff instant is covered by the store test.
var pruneAges = []int{29, 31, 89, 91, 400}

// seedPruneRuns adds one run with a trace, a review and a finding per age.
func seedPruneRuns(t testing.TB, s *stack) []pruneRun {
	t.Helper()
	db := s.db.DB
	ws, pr := workspaceID(t, s), pr482(t, s)
	var runs []pruneRun
	for _, age := range pruneAges {
		run := scalar[string](t, db, `INSERT INTO agent_runs (workspace_id, pr_id, status, source, ran_at)
			VALUES (?, ?, 'done', 'local', now() - make_interval(days => ?)) RETURNING id::text`, ws, pr, age)
		review := scalar[string](t, db, `INSERT INTO reviews (workspace_id, pr_id, kind, run_id) VALUES (?, ?, 'review', ?) RETURNING id::text`, ws, pr, run)
		if err := db.Exec(`INSERT INTO run_traces (run_id, trace) VALUES (?, '{}')`, run).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Exec(`INSERT INTO findings (review_id, file, start_line, end_line, severity, category, title, rationale, confidence)
			VALUES (?, 'f.go', 1, 2, 'WARNING', 'bug', 't', 'r', 0.5)`, review).Error; err != nil {
			t.Fatal(err)
		}
		runs = append(runs, pruneRun{ageDays: age, id: run})
	}
	return runs
}

// AC7, AC8, FR7, FR8, FR9, EC9: the prune that runs at startup deletes the traces older than the
// retention (90 days when unset) and nothing else; null keeps everything; a stored value that is
// not a choice deletes nothing and warns.
func TestWorkspaceHistory_StartupPrune(t *testing.T) {
	tests := []struct {
		name        string
		setRetain   func(t *testing.T, s *stack) // nil: leave retention_days unset
		wantGone    []int                        // ages whose trace is deleted
		wantMsg     string
		wantLevel   string
		wantDeleted float64 // the "deleted" field of the log line, when the prune ran
		wantDays    any     // the "retention_days" field
	}{
		{
			name: "unset means 90 days", wantGone: []int{91, 400},
			wantMsg: "trace prune done", wantLevel: "INFO", wantDeleted: 2, wantDays: 90.0,
		},
		{
			name: "30 days", wantGone: []int{31, 89, 91, 400},
			setRetain: func(t *testing.T, s *stack) { putRetention(t, s, 30) },
			wantMsg:   "trace prune done", wantLevel: "INFO", wantDeleted: 4, wantDays: 30.0,
		},
		{
			name: "null keeps forever", wantGone: nil,
			setRetain: func(t *testing.T, s *stack) { putRetention(t, s, nil) },
			wantMsg:   "trace prune skipped: traces are kept forever", wantLevel: "INFO", wantDays: nil,
		},
		{
			name: "a stored value that is not a choice deletes nothing", wantGone: nil,
			setRetain: func(t *testing.T, s *stack) {
				if err := s.db.DB.Exec(`INSERT INTO settings (workspace_id, key, value) VALUES (?, 'retention_days', '45')`, workspaceID(t, s)).Error; err != nil {
					t.Fatal(err)
				}
			},
			wantMsg: "trace prune skipped: stored retention_days is not valid", wantLevel: "WARN",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			db := pgtest.Migrated(t)

			// First boot: migrate and seed the workspace; its own prune finds nothing of ours
			// because the fixture is added afterwards.
			first := startStack(t, spec{db: db})
			eventually(t, 30*time.Second, "the first boot's prune", func() bool {
				return len(logLines(t, first.logs, "trace prune done")) == 1
			})
			runs := seedPruneRuns(t, first)
			if tc.setRetain != nil {
				tc.setRetain(t, first)
			}
			if err := first.stop(); err != nil {
				t.Fatalf("stop the first boot: %v", err)
			}

			// Second boot on the same database: its startup prune is the one under test.
			second := startStack(t, spec{db: db})
			eventually(t, 30*time.Second, "the startup prune line", func() bool {
				return len(logLines(t, second.logs, tc.wantMsg)) == 1
			})
			line := logLines(t, second.logs, tc.wantMsg)[0]
			if line["level"] != tc.wantLevel {
				t.Errorf("%q logged at %v, want %s", tc.wantMsg, line["level"], tc.wantLevel)
			}
			if tc.wantMsg == "trace prune done" && line["deleted"] != tc.wantDeleted {
				t.Errorf("%q deleted = %v, want %v", tc.wantMsg, line["deleted"], tc.wantDeleted)
			}
			if tc.wantLevel == "INFO" && line["retention_days"] != tc.wantDays {
				t.Errorf("%q retention_days = %v, want %v", tc.wantMsg, line["retention_days"], tc.wantDays)
			}

			for _, r := range runs {
				traces := scalar[int64](t, second.db.DB, `SELECT count(*) FROM run_traces WHERE run_id = ?`, r.id)
				wantGone := slices.Contains(tc.wantGone, r.ageDays)
				if gone := traces == 0; gone != wantGone {
					t.Errorf("run aged %d days: trace deleted = %v, want %v", r.ageDays, gone, wantGone)
				}
				if wantGone {
					if got := second.get("/runs/" + r.id + "/trace"); got.Status != http.StatusNotFound {
						t.Errorf("GET /runs/%s/trace (aged %d days, pruned) = %d, want 404", r.id, r.ageDays, got.Status)
					}
				}
				// Nothing else of the run is touched (AC7).
				for what, q := range map[string]string{
					"run":     `SELECT count(*) FROM agent_runs WHERE id = ?`,
					"review":  `SELECT count(*) FROM reviews WHERE run_id = ?`,
					"finding": `SELECT count(*) FROM findings WHERE review_id IN (SELECT id FROM reviews WHERE run_id = ?)`,
				} {
					if n := scalar[int64](t, second.db.DB, q, r.id); n != 1 {
						t.Errorf("run aged %d days: %d %s rows after the prune, want 1", r.ageDays, n, what)
					}
				}
			}
		})
	}
}

// putRetention stores retention_days through PUT /settings (nil = keep forever).
func putRetention(t testing.TB, s *stack, days any) {
	t.Helper()
	put := s.do(http.MethodPut, "/settings", map[string]any{"retention_days": days})
	if put.Status != http.StatusOK {
		t.Fatalf("PUT /settings retention_days=%v = %d %s, want 200", days, put.Status, put.Body)
	}
}

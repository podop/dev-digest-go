package postgres_test

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/google/go-cmp/cmp"
	"gorm.io/gorm"

	"dev-digest-go/backend/internal/platform/postgres/pgtest"
	"dev-digest-go/backend/internal/seed"
	seedpg "dev-digest-go/backend/internal/seed/adapters/postgres"
)

func TestMain(m *testing.M) { os.Exit(pgtest.Main(m)) }

func runSeed(t *testing.T, db *gorm.DB) (seed.Result, error) {
	t.Helper()
	return seed.New(seedpg.NewStore(db), slog.New(slog.DiscardHandler)).Run(t.Context())
}

func goldenPatches(t *testing.T) map[string]string {
	t.Helper()
	raw, err := os.ReadFile("../../testdata/pr482_patches.golden.json")
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]string
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

// rowCounts returns the row count of every public table except the migration bookkeeping.
func rowCounts(t *testing.T, db *gorm.DB) map[string]int {
	t.Helper()
	var tables []string
	if err := db.Raw(`SELECT tablename FROM pg_tables WHERE schemaname = 'public' AND tablename <> 'schema_migrations'`).Scan(&tables).Error; err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	for _, name := range tables {
		var n int
		if err := db.Raw(`SELECT count(*) FROM public."` + name + `"`).Scan(&n).Error; err != nil {
			t.Fatal(err)
		}
		counts[name] = n
	}
	return counts
}

func TestSeed_WritesTheDemoData(t *testing.T) {
	d := pgtest.Migrated(t)
	res, err := runSeed(t, d.DB)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	want := seed.Result{WorkspaceID: res.WorkspaceID, UserID: res.UserID, Workspaces: 1, Users: 1, Members: 1, Settings: 4, Repos: 1, PullRequests: 1, Files: 4, Commits: 1, Agents: 4, Skills: 9, AgentSkills: 9, Reviews: 1, Findings: 2, AgentRuns: 1}
	if diff := cmp.Diff(want, res); diff != "" {
		t.Errorf("Run() result mismatch (-want +got):\n%s", diff)
	}

	counts := rowCounts(t, d.DB)
	wantCounts := map[string]int{"workspaces": 1, "users": 1, "workspace_members": 1, "settings": 4, "repos": 1, "pull_requests": 1, "pr_files": 4, "pr_commits": 1,
		"agents": 4, "skills": 9, "skill_versions": 9, "agent_skills": 9, "reviews": 1, "findings": 2, "agent_runs": 1}
	for table, n := range counts {
		if counts[table] != wantCounts[table] {
			t.Errorf("rows in %s = %d, want %d", table, n, wantCounts[table])
		}
	}

	var ws struct{ ID, Name string }
	if err := d.DB.Raw(`SELECT id, name FROM workspaces`).Scan(&ws).Error; err != nil {
		t.Fatal(err)
	}
	if ws.Name != "default" || ws.ID != res.WorkspaceID {
		t.Errorf("workspace = %+v, want default with id %s", ws, res.WorkspaceID)
	}
	var user struct{ ID, Email, Name string }
	if err := d.DB.Raw(`SELECT id, email, name FROM users`).Scan(&user).Error; err != nil {
		t.Fatal(err)
	}
	if user.Email != "you@local" || user.Name != "You" || user.ID != res.UserID {
		t.Errorf("user = %+v, want you@local/You with id %s", user, res.UserID)
	}
	var member struct{ WorkspaceID, UserID, Role string }
	if err := d.DB.Raw(`SELECT workspace_id, user_id, role FROM workspace_members`).Scan(&member).Error; err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(struct{ WorkspaceID, UserID, Role string }{res.WorkspaceID, res.UserID, "owner"}, member); diff != "" {
		t.Errorf("membership mismatch (-want +got):\n%s", diff)
	}

	type settingRow struct {
		Key    string
		Value  string
		UserID string
		WSID   string
	}
	var settings []settingRow
	if err := d.DB.Raw(`SELECT key, value::text AS value, user_id::text AS user_id, workspace_id::text AS ws_id FROM settings ORDER BY key`).Scan(&settings).Error; err != nil {
		t.Fatal(err)
	}
	wantSettings := []settingRow{
		{"density", `"regular"`, res.UserID, res.WorkspaceID},
		{"polling_interval_min", `5`, res.UserID, res.WorkspaceID},
		{"sync_to_folder", `true`, res.UserID, res.WorkspaceID},
		{"theme", `"dark"`, res.UserID, res.WorkspaceID},
	}
	if diff := cmp.Diff(wantSettings, settings); diff != "" {
		t.Errorf("settings mismatch (-want +got):\n%s", diff)
	}

	type repo struct {
		Owner, Name, FullName, DefaultBranch string
		ClonePath, CreatedBy                 *string
		LastPolledAt                         *string
	}
	var r repo
	if err := d.DB.Raw(`SELECT owner, name, full_name, default_branch, clone_path, created_by::text AS created_by, last_polled_at::text AS last_polled_at FROM repos`).Scan(&r).Error; err != nil {
		t.Fatal(err)
	}
	if r.Owner != "acme" || r.Name != "payments-api" || r.FullName != "acme/payments-api" || r.DefaultBranch != "main" ||
		r.ClonePath != nil || r.LastPolledAt != nil || r.CreatedBy == nil || *r.CreatedBy != res.UserID {
		t.Errorf("repo = %+v, want acme/payments-api on main, never cloned or polled, created by the system user", r)
	}

	type pr struct {
		Number                                       int
		Title, Author, Branch, Base, HeadSHA, Status string
		Additions, Deletions, FilesCount             int
		Body, LastReviewedSHA, OpenedAt, UpdatedAt   *string
	}
	var p pr
	if err := d.DB.Raw(`SELECT number, title, author, branch, base, head_sha, status, additions, deletions, files_count, body,
		last_reviewed_sha, opened_at::text AS opened_at, updated_at::text AS updated_at FROM pull_requests`).Scan(&p).Error; err != nil {
		t.Fatal(err)
	}
	body := "Add rate limiting to public API endpoints to prevent abuse from unauthenticated clients."
	wantPR := pr{Number: 482, Title: "Add rate limiting to public API endpoints", Author: "marisa.koch", Branch: "feat/rate-limit-public",
		Base: "main", HeadSHA: "a1b2c3d4e5f6", Status: "needs_review", Additions: 247, Deletions: 38, FilesCount: 9, Body: &body}
	if diff := cmp.Diff(wantPR, p); diff != "" {
		t.Errorf("pull request mismatch (-want +got):\n%s", diff)
	}

	type file struct {
		Path                 string
		Additions, Deletions int
		Patch                *string
	}
	var files []file
	if err := d.DB.Raw(`SELECT path, additions, deletions, patch FROM pr_files ORDER BY path`).Scan(&files).Error; err != nil {
		t.Fatal(err)
	}
	golden := goldenPatches(t)
	str := func(s string) *string { return &s }
	wantFiles := []file{
		{"src/api/public/webhooks.ts", 31, 6, str(golden["src/api/public/webhooks.ts"])},
		{"src/api/users.ts", 7, 2, str(golden["src/api/users.ts"])},
		{"src/config.ts", 4, 0, str(golden["src/config.ts"])},
		{"src/middleware/ratelimit.ts", 84, 0, str(golden["src/middleware/ratelimit.ts"])},
	}
	if diff := cmp.Diff(wantFiles, files); diff != "" {
		t.Errorf("pr_files mismatch (-want +got):\n%s", diff)
	}

	var commit struct {
		SHA, Message, Author string
		CommittedAt          *string
	}
	if err := d.DB.Raw(`SELECT sha, message, author, committed_at::text AS committed_at FROM pr_commits`).Scan(&commit).Error; err != nil {
		t.Fatal(err)
	}
	if commit.SHA != "a1b2c3d4e5f6" || commit.Message != "Add token-bucket rate limiter" || commit.Author != "marisa.koch" || commit.CommittedAt != nil {
		t.Errorf("commit = %+v, want a1b2c3d4e5f6 by marisa.koch with no commit time", commit)
	}
}

func TestSeed_RerunKeepsUserChangesAndRestoresNullPatches(t *testing.T) {
	d := pgtest.Migrated(t)
	if _, err := runSeed(t, d.DB); err != nil {
		t.Fatalf("first Run() error = %v", err)
	}
	if err := d.DB.Exec(`UPDATE settings SET value = '"light"' WHERE key = 'theme'`).Error; err != nil {
		t.Fatal(err)
	}
	if err := d.DB.Exec(`UPDATE pr_files SET patch = NULL WHERE path = 'src/config.ts'`).Error; err != nil {
		t.Fatal(err)
	}
	before := rowCounts(t, d.DB)

	res, err := runSeed(t, d.DB)
	if err != nil {
		t.Fatalf("second Run() error = %v", err)
	}
	want := seed.Result{WorkspaceID: res.WorkspaceID, UserID: res.UserID, PatchesFilled: 1}
	if diff := cmp.Diff(want, res); diff != "" {
		t.Errorf("second Run() result mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff(before, rowCounts(t, d.DB)); diff != "" {
		t.Errorf("row counts changed by the second run (-before +after):\n%s", diff)
	}
	var theme string
	if err := d.DB.Raw(`SELECT value::text FROM settings WHERE key = 'theme'`).Scan(&theme).Error; err != nil {
		t.Fatal(err)
	}
	if theme != `"light"` {
		t.Errorf("theme after rerun = %s, want \"light\"", theme)
	}
	var patch string
	if err := d.DB.Raw(`SELECT patch FROM pr_files WHERE path = 'src/config.ts'`).Scan(&patch).Error; err != nil {
		t.Fatal(err)
	}
	if want := goldenPatches(t)["src/config.ts"]; patch != want {
		t.Errorf("restored patch = %q, want %q", patch, want)
	}
}

func TestSeed_RerunLeavesChangedRowsAlone(t *testing.T) {
	d := pgtest.Migrated(t)
	if _, err := runSeed(t, d.DB); err != nil {
		t.Fatal(err)
	}
	if err := d.DB.Exec(`UPDATE pull_requests SET title = 'edited', status = 'merged'`).Error; err != nil {
		t.Fatal(err)
	}
	if err := d.DB.Exec(`UPDATE pr_files SET patch = 'custom' WHERE path = 'src/config.ts'`).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := runSeed(t, d.DB); err != nil {
		t.Fatal(err)
	}
	var got struct{ Title, Status string }
	if err := d.DB.Raw(`SELECT title, status FROM pull_requests`).Scan(&got).Error; err != nil {
		t.Fatal(err)
	}
	if got.Title != "edited" || got.Status != "merged" {
		t.Errorf("pull request after rerun = %+v, want the edited values kept", got)
	}
	var patch string
	if err := d.DB.Raw(`SELECT patch FROM pr_files WHERE path = 'src/config.ts'`).Scan(&patch).Error; err != nil {
		t.Fatal(err)
	}
	if patch != "custom" {
		t.Errorf("non-null patch after rerun = %q, want it kept", patch)
	}
}

func TestSeed_FailureMidwayWritesNothing(t *testing.T) {
	d := pgtest.Migrated(t)
	// The commit is the last insert; forbid every row in pr_commits.
	if err := d.DB.Exec(`ALTER TABLE pr_commits ADD CONSTRAINT force_fail CHECK (false)`).Error; err != nil {
		t.Fatal(err)
	}

	if _, err := runSeed(t, d.DB); err == nil {
		t.Fatal("Run() error = nil, want the CHECK violation")
	}
	for table, n := range rowCounts(t, d.DB) {
		if n != 0 {
			t.Errorf("rows in %s after failed seed = %d, want 0", table, n)
		}
	}
}

func TestSeed_ConcurrentRunsInsertOnce(t *testing.T) {
	d := pgtest.Migrated(t)
	var wg sync.WaitGroup
	for range 3 {
		wg.Go(func() {
			if _, err := runSeed(t, d.DB); err != nil {
				t.Errorf("Run() error = %v", err)
			}
		})
	}
	wg.Wait()
	counts := rowCounts(t, d.DB)
	if counts["workspaces"] != 1 || counts["users"] != 1 || counts["repos"] != 1 || counts["pr_files"] != 4 {
		t.Errorf("rows after 3 concurrent runs = %v, want one workspace, user, repo and 4 files", counts)
	}
}

func TestSeed_LogsOneLinePerRun(t *testing.T) {
	d := pgtest.Migrated(t)
	var buf bytes.Buffer
	if _, err := seed.New(seedpg.NewStore(d.DB), slog.New(slog.NewJSONHandler(&buf, nil))).Run(t.Context()); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 1 {
		t.Fatalf("seed logged %d lines, want 1:\n%s", len(lines), buf.String())
	}
	var line map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &line); err != nil {
		t.Fatal(err)
	}
	for k, want := range map[string]float64{"workspaces": 1, "users": 1, "members": 1, "settings": 4, "repos": 1, "pull_requests": 1, "pr_files": 4, "pr_commits": 1, "agents": 4, "skills": 9, "agent_skills": 9, "reviews": 1, "findings": 2, "agent_runs": 1} {
		if line[k] != want {
			t.Errorf("log field %s = %v, want %v", k, line[k], want)
		}
	}
}

// nodeSeedAgents are the seeded agents sorted by name, with the prompt byte lengths of the Node seed.
var nodeSeedAgents = []struct {
	name, description string
	promptBytes       int
}{
	{"General Reviewer", "Reviews a PR diff for bugs, correctness, and clarity.", 4343},
	{"Performance Reviewer", "Catches N+1 queries, missing indexes, and hot-path allocations.", 6034},
	{"Security Reviewer", "Flags secrets, injection, SSRF and the lethal trifecta before merge.", 5304},
	{"Test Quality Reviewer", "Checks tests for uncovered branches, missing corner cases, over-mocking and flakiness.", 5202},
}

func TestSeed_WritesAgentsSkillsAndLinks(t *testing.T) {
	d := pgtest.Migrated(t)
	res, err := runSeed(t, d.DB)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	type agent struct {
		Name, Description, Provider, Model, Strategy, CiFailOn string
		PromptBytes, Version                                   int
		Enabled, RepoIntel                                     bool
		OutputSchema                                           *string
		CreatedBy, WorkspaceID                                 string
	}
	var agents []agent
	if err := d.DB.Raw(`SELECT name, description, provider, model, strategy, ci_fail_on, octet_length(system_prompt) AS prompt_bytes,
		version, enabled, repo_intel, output_schema::text AS output_schema, created_by::text AS created_by, workspace_id::text AS workspace_id
		FROM agents ORDER BY name COLLATE "C"`).Scan(&agents).Error; err != nil {
		t.Fatal(err)
	}
	var want []agent
	for _, a := range nodeSeedAgents {
		want = append(want, agent{a.name, a.description, "openrouter", "deepseek/deepseek-v4-flash", "single-pass", "critical",
			a.promptBytes, 1, true, true, nil, res.UserID, res.WorkspaceID})
	}
	if diff := cmp.Diff(want, agents); diff != "" {
		t.Errorf("agents mismatch (-want +got):\n%s", diff)
	}

	var distinctCreatedAt int
	if err := d.DB.Raw(`SELECT count(DISTINCT created_at) FROM agents`).Scan(&distinctCreatedAt).Error; err != nil {
		t.Fatal(err)
	}
	if distinctCreatedAt != 1 {
		t.Errorf("distinct agents.created_at = %d, want 1 (one seed transaction)", distinctCreatedAt)
	}
	if n := rowCounts(t, d.DB)["agent_versions"]; n != 0 {
		t.Errorf("rows in agent_versions = %d, want 0 (seeded agents have no snapshot)", n)
	}

	// The prompt text itself is checked against the Node constants in the seed package; here the
	// stored text must round-trip the embedded data.
	var general string
	if err := d.DB.Raw(`SELECT system_prompt FROM agents WHERE name = 'General Reviewer'`).Scan(&general).Error; err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(general, "# Role\n") || strings.HasSuffix(general, "\n") || strings.Contains(general, "\\`") {
		t.Errorf("General Reviewer prompt = %.40q...%q, want it to start with \"# Role\", end without a newline and hold real backticks", general, general[len(general)-20:])
	}

	type skill struct {
		Name, Type, Source string
		Enabled            bool
		Version            int
		SnapVersion        int
		SnapMessage        string
		BodyMatches        bool
		DescMatches        bool
	}
	var skills []skill
	if err := d.DB.Raw(`SELECT s.name, s.type, s.source, s.enabled, s.version, v.version AS snap_version, v.message AS snap_message,
		v.body = s.body AS body_matches, v.description = s.description AS desc_matches
		FROM skills s JOIN skill_versions v ON v.skill_id = s.id ORDER BY s.name COLLATE "C"`).Scan(&skills).Error; err != nil {
		t.Fatal(err)
	}
	wantSkills := []skill{
		{"corner-case-hunter", "rubric", "manual", true, 1, 1, "Created", true, true},
		{"lethal-trifecta", "security", "manual", true, 1, 1, "Created", true, true},
		{"mock-discipline", "convention", "manual", true, 1, 1, "Created", true, true},
		{"n-plus-one-gate", "rubric", "manual", true, 1, 1, "Created", true, true},
		{"no-then-chains", "convention", "manual", true, 1, 1, "Created", true, true},
		{"phantom-api-gate", "security", "manual", false, 1, 1, "Created", true, true},
		{"pr-quality-rubric", "rubric", "manual", true, 1, 1, "Created", true, true},
		{"secret-leakage-gate", "security", "manual", true, 1, 1, "Created", true, true},
		{"test-coverage-nudge", "rubric", "manual", true, 1, 1, "Created", true, true},
	}
	if diff := cmp.Diff(wantSkills, skills); diff != "" {
		t.Errorf("skills mismatch (-want +got):\n%s", diff)
	}

	type link struct {
		Agent, Skill string
		Order        int
	}
	var links []link
	if err := d.DB.Raw(`SELECT a.name AS agent, s.name AS skill, l."order" AS "order" FROM agent_skills l
		JOIN agents a ON a.id = l.agent_id JOIN skills s ON s.id = l.skill_id ORDER BY a.name COLLATE "C", l."order"`).Scan(&links).Error; err != nil {
		t.Fatal(err)
	}
	wantLinks := []link{
		{"General Reviewer", "pr-quality-rubric", 0}, {"General Reviewer", "no-then-chains", 1},
		{"Performance Reviewer", "n-plus-one-gate", 0},
		{"Security Reviewer", "secret-leakage-gate", 0}, {"Security Reviewer", "lethal-trifecta", 1}, {"Security Reviewer", "phantom-api-gate", 2},
		{"Test Quality Reviewer", "test-coverage-nudge", 0}, {"Test Quality Reviewer", "mock-discipline", 1}, {"Test Quality Reviewer", "corner-case-hunter", 2},
	}
	if diff := cmp.Diff(wantLinks, links); diff != "" {
		t.Errorf("agent skill links mismatch (-want +got):\n%s", diff)
	}
}

func TestSeed_RerunAfterDeletingBuiltInsDoesNotRecreateThem(t *testing.T) {
	d := pgtest.Migrated(t)
	if _, err := runSeed(t, d.DB); err != nil {
		t.Fatal(err)
	}
	if err := d.DB.Exec(`DELETE FROM agents WHERE name = 'General Reviewer'`).Error; err != nil {
		t.Fatal(err)
	}
	if err := d.DB.Exec(`DELETE FROM skills WHERE name = 'no-then-chains'`).Error; err != nil {
		t.Fatal(err)
	}
	before := rowCounts(t, d.DB)

	res, err := runSeed(t, d.DB)
	if err != nil {
		t.Fatalf("second Run() error = %v", err)
	}
	if res.Agents != 0 || res.Skills != 0 || res.AgentSkills != 0 {
		t.Errorf("second Run() wrote agents=%d skills=%d links=%d, want none", res.Agents, res.Skills, res.AgentSkills)
	}
	if diff := cmp.Diff(before, rowCounts(t, d.DB)); diff != "" {
		t.Errorf("row counts changed by the second run (-before +after):\n%s", diff)
	}
}

func TestSeed_Phase1DatabaseGetsAgentsAndSkillsOnce(t *testing.T) {
	d := pgtest.Migrated(t)
	if _, err := runSeed(t, d.DB); err != nil {
		t.Fatal(err)
	}
	// A Phase 1 database: the workspace exists, nothing in agents or skills.
	if err := d.DB.Exec(`DELETE FROM agents`).Error; err != nil {
		t.Fatal(err)
	}
	if err := d.DB.Exec(`DELETE FROM skills`).Error; err != nil {
		t.Fatal(err)
	}
	counts := rowCounts(t, d.DB)
	for _, table := range []string{"agents", "skills", "skill_versions", "agent_skills"} {
		if counts[table] != 0 {
			t.Fatalf("rows in %s = %d after cleanup, want 0", table, counts[table])
		}
	}

	res, err := runSeed(t, d.DB)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if res.Workspaces != 0 || res.Agents != 4 || res.Skills != 9 || res.AgentSkills != 9 {
		t.Errorf("Run() = %+v, want workspace kept and 4 agents, 9 skills, 9 links written", res)
	}
	counts = rowCounts(t, d.DB)
	if counts["agents"] != 4 || counts["skills"] != 9 || counts["skill_versions"] != 9 || counts["agent_skills"] != 9 {
		t.Errorf("row counts = agents %d, skills %d, skill_versions %d, agent_skills %d, want 4, 9, 9, 9",
			counts["agents"], counts["skills"], counts["skill_versions"], counts["agent_skills"])
	}
}

func TestSeed_SkillsOnlyWorkspaceIsNotSeeded(t *testing.T) {
	d := pgtest.Migrated(t)
	if _, err := runSeed(t, d.DB); err != nil {
		t.Fatal(err)
	}
	if err := d.DB.Exec(`DELETE FROM agents`).Error; err != nil {
		t.Fatal(err)
	}
	res, err := runSeed(t, d.DB)
	if err != nil {
		t.Fatal(err)
	}
	if res.Agents != 0 || res.Skills != 0 || res.AgentSkills != 0 {
		t.Errorf("Run() wrote agents=%d skills=%d links=%d, want none while skills exist", res.Agents, res.Skills, res.AgentSkills)
	}
}

// demoRows reads the PR #482 demo review back: the review, its findings and the run it points at.
type demoRows struct {
	Review struct {
		ID, Kind, Verdict, Summary, Model string
		Score                             int
		AgentID, RunID                    *string
		CreatedAt                         string
	}
	Findings []struct {
		File, Severity, Category, Title, Rationale, Kind string
		StartLine, EndLine                               int
		Suggestion                                       *string
		Confidence                                       float64
		AcceptedAt, DismissedAt                          *string
	}
	Run struct {
		ID, Status, Source, Provider, Model, Grounding, RanAt string
		AgentID                                               *string
		DurationMs, TokensIn, TokensOut, FindingsCount        int
		Score, Blockers                                       int
		CostUSD                                               string
		Error                                                 *string
	}
}

func readDemo(t *testing.T, db *gorm.DB) demoRows {
	t.Helper()
	var d demoRows
	if err := db.Raw(`SELECT id, kind, verdict, summary, model, score, agent_id::text AS agent_id, run_id::text AS run_id, created_at::text AS created_at
		FROM reviews`).Scan(&d.Review).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Raw(`SELECT file, severity, category, title, rationale, kind, start_line, end_line, suggestion, confidence,
		accepted_at::text AS accepted_at, dismissed_at::text AS dismissed_at FROM findings ORDER BY start_line`).Scan(&d.Findings).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Raw(`SELECT id, status, source, provider, model, grounding, ran_at::text AS ran_at, agent_id::text AS agent_id, duration_ms,
		tokens_in, tokens_out, findings_count, score, blockers, cost_usd::text AS cost_usd, error FROM agent_runs`).Scan(&d.Run).Error; err != nil {
		t.Fatal(err)
	}
	return d
}

func TestSeed_WritesTheDemoReview(t *testing.T) {
	d := pgtest.Migrated(t)
	if _, err := runSeed(t, d.DB); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	got := readDemo(t, d.DB)

	var general string
	if err := d.DB.Raw(`SELECT id::text FROM agents WHERE name = 'General Reviewer'`).Scan(&general).Error; err != nil {
		t.Fatal(err)
	}
	str := func(s string) *string { return &s }

	if got.Review.Kind != "review" || got.Review.Verdict != "request_changes" || got.Review.Score != 61 || got.Review.Model != "seed" ||
		got.Review.Summary != "Solid middleware approach, but a Stripe secret key is committed in plaintext and the user-list endpoint introduces an N+1 query under the new limiter." {
		t.Errorf("review = %+v, want the PR #482 demo review", got.Review)
	}
	if diff := cmp.Diff(&general, got.Review.AgentID); diff != "" {
		t.Errorf("review agent_id mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff(&got.Run.ID, got.Review.RunID); diff != "" {
		t.Errorf("review run_id mismatch (-want +got):\n%s", diff)
	}

	type finding = struct {
		File, Severity, Category, Title, Rationale, Kind string
		StartLine, EndLine                               int
		Suggestion                                       *string
		Confidence                                       float64
		AcceptedAt, DismissedAt                          *string
	}
	wantFindings := []finding{
		{File: "src/config.ts", Severity: "CRITICAL", Category: "security", Title: "Hardcoded Stripe secret key in commit",
			Rationale: "Line 12 contains a literal `sk_live_` Stripe secret key.", Kind: "finding", StartLine: 12, EndLine: 12,
			Suggestion: str("Move to env var and rotate the key immediately."), Confidence: 0.98},
		{File: "src/api/users.ts", Severity: "WARNING", Category: "perf", Title: "N+1 query in user list endpoint",
			Rationale: "Loop issues one query per user → N+1.", Kind: "finding", StartLine: 45, EndLine: 52,
			Suggestion: str("Use a single IN query and group in memory."), Confidence: 0.86},
	}
	if diff := cmp.Diff(wantFindings, got.Findings); diff != "" {
		t.Errorf("findings mismatch (-want +got):\n%s", diff)
	}

	r := got.Run
	if r.Status != "done" || r.Source != "local" || r.Provider != "openrouter" || r.Model != "deepseek/deepseek-v4-flash" ||
		r.Grounding != "2/2 passed" || r.DurationMs != 18420 || r.TokensIn != 6214 || r.TokensOut != 842 ||
		r.FindingsCount != 2 || r.Score != 61 || r.Blockers != 1 || r.CostUSD != "0.002135" || r.Error != nil {
		t.Errorf("run = %+v, want the done demo run", r)
	}
	if diff := cmp.Diff(&general, r.AgentID); diff != "" {
		t.Errorf("run agent_id mismatch (-want +got):\n%s", diff)
	}
	if r.RanAt != got.Review.CreatedAt {
		t.Errorf("run ran_at = %s, want the review created_at %s", r.RanAt, got.Review.CreatedAt)
	}
	var prID, runPR string
	if err := d.DB.Raw(`SELECT id::text FROM pull_requests WHERE number = 482`).Scan(&prID).Error; err != nil {
		t.Fatal(err)
	}
	if err := d.DB.Raw(`SELECT pr_id::text FROM agent_runs`).Scan(&runPR).Error; err != nil || runPR != prID {
		t.Errorf("run pr_id = %q, %v, want %q", runPR, err, prID)
	}
}

func TestSeed_DemoReviewWrittenOnce(t *testing.T) {
	d := pgtest.Migrated(t)
	if _, err := runSeed(t, d.DB); err != nil {
		t.Fatal(err)
	}
	first := readDemo(t, d.DB)
	before := rowCounts(t, d.DB)

	res, err := runSeed(t, d.DB)
	if err != nil {
		t.Fatalf("second Run() error = %v", err)
	}
	if res.Reviews != 0 || res.Findings != 0 || res.AgentRuns != 0 {
		t.Errorf("second Run() wrote reviews=%d findings=%d runs=%d, want none", res.Reviews, res.Findings, res.AgentRuns)
	}
	if diff := cmp.Diff(before, rowCounts(t, d.DB)); diff != "" {
		t.Errorf("row counts changed by the second run (-before +after):\n%s", diff)
	}
	if diff := cmp.Diff(first, readDemo(t, d.DB)); diff != "" {
		t.Errorf("demo review changed by the second run (-first +second):\n%s", diff)
	}
}

// EC4035: the demo review alone deleted -> nothing is re-created.
func TestSeed_DeletedDemoReviewIsNotRecreatedWhileTheRunExists(t *testing.T) {
	d := pgtest.Migrated(t)
	if _, err := runSeed(t, d.DB); err != nil {
		t.Fatal(err)
	}
	if err := d.DB.Exec(`DELETE FROM reviews`).Error; err != nil { // findings go with it (ON DELETE CASCADE)
		t.Fatal(err)
	}
	before := rowCounts(t, d.DB)
	res, err := runSeed(t, d.DB)
	if err != nil {
		t.Fatal(err)
	}
	if res.Reviews != 0 || res.AgentRuns != 0 {
		t.Errorf("Run() wrote reviews=%d runs=%d, want none while the run exists", res.Reviews, res.AgentRuns)
	}
	if diff := cmp.Diff(before, rowCounts(t, d.DB)); diff != "" {
		t.Errorf("row counts changed (-before +after):\n%s", diff)
	}
}

// EC4035: PR #482 with a user-made run and no review -> no demo review next to it.
func TestSeed_UserRunWithoutReviewGetsNoDemoReview(t *testing.T) {
	d := pgtest.Migrated(t)
	if _, err := runSeed(t, d.DB); err != nil {
		t.Fatal(err)
	}
	// Replace the demo with a user-made failed run and no review.
	if err := d.DB.Exec(`DELETE FROM reviews`).Error; err != nil {
		t.Fatal(err)
	}
	if err := d.DB.Exec(`UPDATE agent_runs SET status = 'failed', error = 'user run'`).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := runSeed(t, d.DB); err != nil {
		t.Fatal(err)
	}
	counts := rowCounts(t, d.DB)
	if counts["reviews"] != 0 || counts["findings"] != 0 || counts["agent_runs"] != 1 {
		t.Errorf("rows after the rerun = reviews %d, findings %d, agent_runs %d, want 0, 0, 1", counts["reviews"], counts["findings"], counts["agent_runs"])
	}
}

// EC4035: the demo run deleted together with its review (DELETE /runs/:id) -> written again.
func TestSeed_DeletedDemoRunIsWrittenAgain(t *testing.T) {
	d := pgtest.Migrated(t)
	if _, err := runSeed(t, d.DB); err != nil {
		t.Fatal(err)
	}
	if err := d.DB.Exec(`DELETE FROM reviews WHERE run_id IN (SELECT id FROM agent_runs)`).Error; err != nil {
		t.Fatal(err)
	}
	if err := d.DB.Exec(`DELETE FROM agent_runs`).Error; err != nil {
		t.Fatal(err)
	}
	res, err := runSeed(t, d.DB)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if res.Reviews != 1 || res.Findings != 2 || res.AgentRuns != 1 {
		t.Errorf("Run() reviews/findings/runs = %d/%d/%d, want 1/2/1", res.Reviews, res.Findings, res.AgentRuns)
	}
	got := readDemo(t, d.DB)
	if got.Review.RunID == nil || *got.Review.RunID != got.Run.ID {
		t.Errorf("rewritten review run_id = %v, want %s", got.Review.RunID, got.Run.ID)
	}
}

// A database without "General Reviewer" (deleted) still gets the demo, with null agent ids.
func TestSeed_DemoReviewWithoutGeneralReviewer(t *testing.T) {
	d := pgtest.Migrated(t)
	if _, err := runSeed(t, d.DB); err != nil {
		t.Fatal(err)
	}
	if err := d.DB.Exec(`DELETE FROM reviews`).Error; err != nil {
		t.Fatal(err)
	}
	if err := d.DB.Exec(`DELETE FROM agent_runs`).Error; err != nil {
		t.Fatal(err)
	}
	if err := d.DB.Exec(`DELETE FROM agents WHERE name = 'General Reviewer'`).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := runSeed(t, d.DB); err != nil {
		t.Fatal(err)
	}
	got := readDemo(t, d.DB)
	if got.Review.AgentID != nil || got.Run.AgentID != nil {
		t.Errorf("agent ids = %v / %v, want both null", got.Review.AgentID, got.Run.AgentID)
	}
	if got.Review.RunID == nil || len(got.Findings) != 2 {
		t.Errorf("demo = run_id %v, %d findings, want a linked run and 2 findings", got.Review.RunID, len(got.Findings))
	}
}

func TestSeed_FailureInTheDemoReviewWritesNothing(t *testing.T) {
	d := pgtest.Migrated(t)
	if err := d.DB.Exec(`ALTER TABLE agent_runs ADD CONSTRAINT force_fail CHECK (false)`).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := runSeed(t, d.DB); err == nil {
		t.Fatal("Run() error = nil, want the CHECK violation")
	}
	for table, n := range rowCounts(t, d.DB) {
		if n != 0 {
			t.Errorf("rows in %s after failed seed = %d, want 0", table, n)
		}
	}
}

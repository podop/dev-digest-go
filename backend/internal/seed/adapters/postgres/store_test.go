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
	want := seed.Result{WorkspaceID: res.WorkspaceID, UserID: res.UserID, Workspaces: 1, Users: 1, Members: 1, Settings: 4, Repos: 1, PullRequests: 1, Files: 4, Commits: 1}
	if diff := cmp.Diff(want, res); diff != "" {
		t.Errorf("Run() result mismatch (-want +got):\n%s", diff)
	}

	counts := rowCounts(t, d.DB)
	wantCounts := map[string]int{"workspaces": 1, "users": 1, "workspace_members": 1, "settings": 4, "repos": 1, "pull_requests": 1, "pr_files": 4, "pr_commits": 1}
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
	for k, want := range map[string]float64{"workspaces": 1, "users": 1, "members": 1, "settings": 4, "repos": 1, "pull_requests": 1, "pr_files": 4, "pr_commits": 1} {
		if line[k] != want {
			t.Errorf("log field %s = %v, want %v", k, line[k], want)
		}
	}
}

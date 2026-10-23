package postgres_test

import (
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"gorm.io/gorm"

	"dev-digest-go/backend/internal/platform/postgres/pgtest"
	pgstore "dev-digest-go/backend/internal/pulls/adapters/postgres"
	"dev-digest-go/backend/internal/pulls/domain"
	"dev-digest-go/backend/internal/seed"
	seedpg "dev-digest-go/backend/internal/seed/adapters/postgres"
)

func TestMain(m *testing.M) { os.Exit(pgtest.Main(m)) }

func ptr[T any](v T) *T { return &v }

type fixture struct {
	db     *gorm.DB
	store  *pgstore.Store
	wsID   string
	userID string
	repoID string // acme/payments-api
	prID   string // PR #482
}

// seeded returns a migrated database holding the demo seed.
func seeded(t *testing.T) fixture {
	t.Helper()
	d := pgtest.Migrated(t)
	res, err := seed.New(seedpg.NewStore(d.DB), slog.New(slog.DiscardHandler)).Run(t.Context())
	if err != nil {
		t.Fatalf("seed error = %v", err)
	}
	f := fixture{db: d.DB, store: pgstore.NewStore(d.DB), wsID: res.WorkspaceID, userID: res.UserID}
	scan(t, d.DB, &f.repoID, `SELECT id FROM repos WHERE full_name = 'acme/payments-api'`)
	scan(t, d.DB, &f.prID, `SELECT id FROM pull_requests WHERE number = 482`)
	return f
}

// withoutDemoReview removes the PR #482 demo review (and its run) the seed writes, so a test
// starts from a pull request that has no review.
func (f fixture) withoutDemoReview(t *testing.T) {
	t.Helper()
	exec(t, f.db, `DELETE FROM reviews WHERE pr_id = ?`, f.prID) // findings go with their review
	exec(t, f.db, `DELETE FROM agent_runs WHERE pr_id = ?`, f.prID)
}

func scan(t *testing.T, db *gorm.DB, dest any, query string, args ...any) {
	t.Helper()
	if err := db.Raw(query, args...).Scan(dest).Error; err != nil {
		t.Fatalf("query %q: %v", query, err)
	}
}

func exec(t *testing.T, db *gorm.DB, query string, args ...any) {
	t.Helper()
	if err := db.Exec(query, args...).Error; err != nil {
		t.Fatalf("exec %q: %v", query, err)
	}
}

func day(d int, h int) *time.Time {
	t := time.Date(2026, 10, d, h, 0, 0, 0, time.UTC)
	return &t
}

var timeEqual = cmp.Comparer(func(a, b time.Time) bool { return a.Equal(b) })

func TestFindRepo_scopedToWorkspace(t *testing.T) {
	f := seeded(t)
	got, err := f.store.FindRepo(t.Context(), f.wsID, f.repoID)
	if err != nil {
		t.Fatalf("FindRepo() error = %v", err)
	}
	if want := (domain.Repo{ID: f.repoID, Owner: "acme", Name: "payments-api"}); got != want {
		t.Errorf("FindRepo() = %+v, want %+v", got, want)
	}

	var otherWS string
	scan(t, f.db, &otherWS, `INSERT INTO workspaces (name) VALUES ('other') RETURNING id`)
	if _, err := f.store.FindRepo(t.Context(), otherWS, f.repoID); err != domain.ErrRepoNotFound {
		t.Errorf("FindRepo(other workspace) error = %v, want ErrRepoNotFound", err)
	}
	if _, err := f.store.FindPull(t.Context(), otherWS, f.prID); err != domain.ErrPullNotFound {
		t.Errorf("FindPull(other workspace) error = %v, want ErrPullNotFound", err)
	}
}

func TestUpsertListed_insertThenUpdateOnlyListFields(t *testing.T) {
	f := seeded(t)
	ctx := t.Context()
	first := domain.Listed{Number: 7, Title: "old", Author: "a", Branch: "feat", Base: "main", HeadSHA: "h1",
		Status: "open", OpenedAt: day(1, 9), UpdatedAt: day(2, 9)}
	if err := f.store.UpsertListed(ctx, f.wsID, f.repoID, []domain.Listed{first}); err != nil {
		t.Fatalf("UpsertListed() error = %v", err)
	}
	// Diff stats arrive later and must survive the next list sync.
	pulls, err := f.store.ListPulls(ctx, f.wsID, f.repoID)
	if err != nil {
		t.Fatalf("ListPulls() error = %v", err)
	}
	var id7 string
	for _, p := range pulls {
		if p.Number == 7 {
			id7 = p.ID
		}
	}
	if id7 == "" {
		t.Fatalf("ListPulls() has no #7: %+v", pulls)
	}
	if err := f.store.SetDiffStats(ctx, id7, domain.DiffStats{Additions: 3, Deletions: 2, FilesCount: 1}); err != nil {
		t.Fatalf("SetDiffStats() error = %v", err)
	}

	second := domain.Listed{Number: 7, Title: "new", Author: "b", Branch: "other", Base: "dev", HeadSHA: "h2",
		Status: "closed", OpenedAt: day(5, 9), UpdatedAt: day(6, 9)}
	if err := f.store.UpsertListed(ctx, f.wsID, f.repoID, []domain.Listed{second}); err != nil {
		t.Fatalf("UpsertListed() second error = %v", err)
	}
	got, err := f.store.FindPull(ctx, f.wsID, id7)
	if err != nil {
		t.Fatalf("FindPull() error = %v", err)
	}
	want := domain.Pull{ID: id7, RepoID: f.repoID, Number: 7, Title: "new", Author: "a", Branch: "feat", Base: "main",
		HeadSHA: "h2", Additions: 3, Deletions: 2, FilesCount: 1, Status: "closed", OpenedAt: day(1, 9), UpdatedAt: day(6, 9)}
	if diff := cmp.Diff(want, got, timeEqual); diff != "" {
		t.Errorf("FindPull() after upsert mismatch (-want +got):\n%s", diff)
	}
}

type mergeState struct {
	MergedAt *time.Time
	ClosedAt *time.Time
	Draft    bool
}

func readMergeState(t *testing.T, f fixture, number int) mergeState {
	t.Helper()
	var got mergeState
	scan(t, f.db, &got, `SELECT merged_at, closed_at, draft FROM pull_requests WHERE repo_id = ? AND number = ?`, f.repoID, number)
	return got
}

func TestUpsertListed_writesMergeStateOnInsertAndUpdate(t *testing.T) {
	f := seeded(t)
	ctx := t.Context()
	base := domain.Listed{Number: 31, Title: "t", Author: "a", Branch: "b", Base: "main", HeadSHA: "h", UpdatedAt: day(2, 9)}
	open := base
	open.Status, open.Draft = "open", true
	if err := f.store.UpsertListed(ctx, f.wsID, f.repoID, []domain.Listed{open}); err != nil {
		t.Fatalf("UpsertListed(open draft) error = %v", err)
	}
	if diff := cmp.Diff(mergeState{Draft: true}, readMergeState(t, f, 31), timeEqual); diff != "" {
		t.Errorf("merge state after insert mismatch (-want +got):\n%s", diff)
	}

	merged := base
	merged.Status, merged.MergedAt, merged.ClosedAt, merged.UpdatedAt = "merged", day(3, 9), day(3, 9), day(3, 9)
	insertMerged := merged
	insertMerged.Number = 32
	if err := f.store.UpsertListed(ctx, f.wsID, f.repoID, []domain.Listed{merged, insertMerged}); err != nil {
		t.Fatalf("UpsertListed(merged) error = %v", err)
	}
	want := mergeState{MergedAt: day(3, 9), ClosedAt: day(3, 9)}
	for _, n := range []int{31, 32} { // 31 updated in place, 32 inserted already merged
		if diff := cmp.Diff(want, readMergeState(t, f, n), timeEqual); diff != "" {
			t.Errorf("merge state of #%d mismatch (-want +got):\n%s", n, diff)
		}
	}

	// A repeat of the same sync changes nothing.
	if err := f.store.UpsertListed(ctx, f.wsID, f.repoID, []domain.Listed{merged}); err != nil {
		t.Fatalf("UpsertListed(repeat) error = %v", err)
	}
	if diff := cmp.Diff(want, readMergeState(t, f, 31), timeEqual); diff != "" {
		t.Errorf("merge state after repeat mismatch (-want +got):\n%s", diff)
	}
	var count int
	scan(t, f.db, &count, `SELECT count(*) FROM pull_requests WHERE repo_id = ? AND number = 31`, f.repoID)
	if count != 1 {
		t.Errorf("rows for #31 after repeat = %d, want 1", count)
	}

	// A reopened PR clears the stored merge and close times.
	reopened := base
	reopened.Status, reopened.UpdatedAt = "open", day(4, 9)
	if err := f.store.UpsertListed(ctx, f.wsID, f.repoID, []domain.Listed{reopened}); err != nil {
		t.Fatalf("UpsertListed(reopened) error = %v", err)
	}
	if diff := cmp.Diff(mergeState{}, readMergeState(t, f, 31), timeEqual); diff != "" {
		t.Errorf("merge state after reopen mismatch (-want +got):\n%s", diff)
	}
}

func TestUpsertListed_duplicateNumberInOneCall(t *testing.T) {
	f := seeded(t)
	l := func(title string) domain.Listed {
		return domain.Listed{Number: 9, Title: title, Author: "a", Branch: "b", Base: "main", HeadSHA: "h", Status: "open"}
	}
	if err := f.store.UpsertListed(t.Context(), f.wsID, f.repoID, []domain.Listed{l("first"), l("second")}); err != nil {
		t.Fatalf("UpsertListed(duplicate number) error = %v", err)
	}
	pulls, _ := f.store.ListPulls(t.Context(), f.wsID, f.repoID)
	for _, p := range pulls {
		if p.Number == 9 && p.Title != "second" {
			t.Errorf("#9 title = %q, want second (last entry wins)", p.Title)
		}
	}
}

func TestListPulls_order(t *testing.T) {
	f := seeded(t)
	// The seeded #482 and #483 have a null updated_at.
	listed := []domain.Listed{
		{Number: 1, Title: "t", Author: "a", Branch: "b", Base: "m", HeadSHA: "h", Status: "open", UpdatedAt: day(3, 10)},
		{Number: 2, Title: "t", Author: "a", Branch: "b", Base: "m", HeadSHA: "h", Status: "open", UpdatedAt: day(3, 11)},
		{Number: 3, Title: "t", Author: "a", Branch: "b", Base: "m", HeadSHA: "h", Status: "open", UpdatedAt: day(3, 11)},
		{Number: 4, Title: "t", Author: "a", Branch: "b", Base: "m", HeadSHA: "h", Status: "open"},
	}
	if err := f.store.UpsertListed(t.Context(), f.wsID, f.repoID, listed); err != nil {
		t.Fatalf("UpsertListed() error = %v", err)
	}
	pulls, err := f.store.ListPulls(t.Context(), f.wsID, f.repoID)
	if err != nil {
		t.Fatalf("ListPulls() error = %v", err)
	}
	var got []int
	for _, p := range pulls {
		got = append(got, p.Number)
	}
	if diff := cmp.Diff([]int{3, 2, 1, 483, 482, 4}, got); diff != "" {
		t.Errorf("ListPulls() numbers mismatch (-want +got):\n%s", diff)
	}
}

func TestSetDiffStats_keepsUpdatedAt(t *testing.T) {
	f := seeded(t)
	exec(t, f.db, `UPDATE pull_requests SET updated_at = ? WHERE id = ?`, day(4, 4), f.prID)
	if err := f.store.SetDiffStats(t.Context(), f.prID, domain.DiffStats{Additions: 1, Deletions: 2, FilesCount: 3}); err != nil {
		t.Fatalf("SetDiffStats() error = %v", err)
	}
	got, _ := f.store.FindPull(t.Context(), f.wsID, f.prID)
	if got.UpdatedAt == nil || !got.UpdatedAt.Equal(*day(4, 4)) {
		t.Errorf("updated_at after SetDiffStats = %v, want %v", got.UpdatedAt, day(4, 4))
	}
	if got.Additions != 1 || got.Deletions != 2 || got.FilesCount != 3 {
		t.Errorf("stats after SetDiffStats = %d/%d/%d, want 1/2/3", got.Additions, got.Deletions, got.FilesCount)
	}
}

func TestReplaceDetail_mirrorsFilesAndCommits(t *testing.T) {
	f := seeded(t)
	ctx := t.Context()
	// Seeded: 4 files and 1 commit (a1b2c3d4e5f6).
	files, err := f.store.ListFiles(ctx, f.prID)
	if err != nil || len(files) != 4 {
		t.Fatalf("seeded ListFiles() = %d files, error %v, want 4", len(files), err)
	}

	mirror := domain.DetailMirror{
		Body:      ptr("new body"),
		DiffStats: domain.DiffStats{Additions: 10, Deletions: 5, FilesCount: 2},
		Files: []domain.File{
			{Path: "src/config.ts", Additions: 1, Deletions: 1, Patch: ptr("@@ new")}, // kept, updated
			{Path: "added.go", Additions: 9, Deletions: 4},                            // new, no patch
		},
		Commits: []domain.Commit{
			{SHA: "c1", Message: "m1", Author: "x", CommittedAt: day(1, 1)},
			{SHA: "c2", Message: "m2", Author: "y"},
		},
	}
	if err := f.store.ReplaceDetail(ctx, f.prID, mirror); err != nil {
		t.Fatalf("ReplaceDetail() error = %v", err)
	}

	gotFiles, _ := f.store.ListFiles(ctx, f.prID)
	wantFiles := []domain.File{
		{Path: "added.go", Additions: 9, Deletions: 4},
		{Path: "src/config.ts", Additions: 1, Deletions: 1, Patch: ptr("@@ new")},
	}
	if diff := cmp.Diff(wantFiles, gotFiles); diff != "" {
		t.Errorf("ListFiles() mismatch (-want +got):\n%s", diff)
	}
	gotCommits, _ := f.store.ListCommits(ctx, f.prID)
	wantCommits := []domain.Commit{
		{SHA: "c1", Message: "m1", Author: "x", CommittedAt: day(1, 1)},
		{SHA: "c2", Message: "m2", Author: "y"},
	}
	if diff := cmp.Diff(wantCommits, gotCommits, timeEqual); diff != "" {
		t.Errorf("ListCommits() mismatch (-want +got):\n%s", diff)
	}
	pr, _ := f.store.FindPull(ctx, f.wsID, f.prID)
	if pr.Body == nil || *pr.Body != "new body" || pr.Additions != 10 || pr.Deletions != 5 || pr.FilesCount != 2 {
		t.Errorf("pull after ReplaceDetail = body %v, %d/%d/%d, want new body 10/5/2", pr.Body, pr.Additions, pr.Deletions, pr.FilesCount)
	}

	// An empty mirror removes everything and clears the body.
	if err := f.store.ReplaceDetail(ctx, f.prID, domain.DetailMirror{}); err != nil {
		t.Fatalf("ReplaceDetail(empty) error = %v", err)
	}
	if files, _ := f.store.ListFiles(ctx, f.prID); len(files) != 0 {
		t.Errorf("files after empty mirror = %d, want 0", len(files))
	}
	if commits, _ := f.store.ListCommits(ctx, f.prID); len(commits) != 0 {
		t.Errorf("commits after empty mirror = %d, want 0", len(commits))
	}
	if pr, _ := f.store.FindPull(ctx, f.wsID, f.prID); pr.Body != nil {
		t.Errorf("body after empty mirror = %q, want nil", *pr.Body)
	}
}

func TestReplaceDetail_rollsBackOnFailure(t *testing.T) {
	f := seeded(t)
	// Two entries with the same path violate pr_files_pr_path_uq inside the upsert statement.
	mirror := domain.DetailMirror{
		Body:  ptr("must not persist"),
		Files: []domain.File{{Path: "dup.go"}, {Path: "dup.go"}},
	}
	if err := f.store.ReplaceDetail(t.Context(), f.prID, mirror); err == nil {
		t.Fatal("ReplaceDetail(duplicate paths) error = nil, want error")
	}
	pr, _ := f.store.FindPull(t.Context(), f.wsID, f.prID)
	if pr.Body == nil || *pr.Body == "must not persist" {
		t.Errorf("body after failed ReplaceDetail = %v, want the original", pr.Body)
	}
	if files, _ := f.store.ListFiles(t.Context(), f.prID); len(files) != 4 {
		t.Errorf("files after failed ReplaceDetail = %d, want the 4 seeded", len(files))
	}
}

func TestListFilesAndCommits_seeded(t *testing.T) {
	f := seeded(t)
	files, _ := f.store.ListFiles(t.Context(), f.prID)
	var paths []string
	for _, fl := range files {
		if fl.Patch == nil || *fl.Patch == "" {
			t.Errorf("seeded file %s has no patch", fl.Path)
		}
		paths = append(paths, fl.Path)
	}
	want := []string{"src/api/public/webhooks.ts", "src/api/users.ts", "src/config.ts", "src/middleware/ratelimit.ts"}
	if diff := cmp.Diff(want, paths); diff != "" {
		t.Errorf("seeded file paths mismatch (-want +got):\n%s", diff)
	}
	commits, _ := f.store.ListCommits(t.Context(), f.prID)
	if len(commits) != 1 || commits[0].SHA != "a1b2c3d4e5f6" || commits[0].CommittedAt != nil {
		t.Errorf("seeded commits = %+v, want one a1b2c3d4e5f6 with null committed_at", commits)
	}
}

func TestMarkPolled(t *testing.T) {
	f := seeded(t)
	at := time.Date(2026, 10, 21, 8, 30, 0, 0, time.UTC)
	if err := f.store.MarkPolled(t.Context(), f.wsID, f.repoID, at); err != nil {
		t.Fatalf("MarkPolled() error = %v", err)
	}
	var got time.Time
	scan(t, f.db, &got, `SELECT last_polled_at FROM repos WHERE id = ?`, f.repoID)
	if !got.Equal(at) {
		t.Errorf("last_polled_at = %v, want %v", got, at)
	}
	if err := f.store.MarkPolled(t.Context(), f.wsID, "00000000-0000-0000-0000-000000000000", at); err != domain.ErrRepoNotFound {
		t.Errorf("MarkPolled(unknown repo) error = %v, want ErrRepoNotFound", err)
	}
}

func TestRollups_demoReview(t *testing.T) {
	f := seeded(t)
	var reviewID string
	var createdAt time.Time
	scan(t, f.db, &reviewID, `SELECT id FROM reviews WHERE pr_id = ?`, f.prID)
	scan(t, f.db, &createdAt, `SELECT created_at FROM reviews WHERE id = ?`, reviewID)
	got, err := f.store.Rollups(t.Context(), f.wsID, []string{f.prID})
	if err != nil {
		t.Fatalf("Rollups() error = %v", err)
	}
	want := domain.Rollup{
		Score:           ptr(61),
		CostUSD:         ptr(0.002135),
		LatestReviewID:  &reviewID,
		LastReviewedAt:  &createdAt,
		LatestReviewIDs: []string{reviewID},
		FindingsCounts:  &domain.FindingsCounts{Critical: 1, Warning: 1},
	}
	if diff := cmp.Diff(want, got[f.prID], timeEqual); diff != "" {
		t.Errorf("Rollups(seeded PR with the demo review) mismatch (-want +got):\n%s", diff)
	}
}

func TestRollups_noReviews(t *testing.T) {
	f := seeded(t)
	f.withoutDemoReview(t)
	got, err := f.store.Rollups(t.Context(), f.wsID, []string{f.prID})
	if err != nil {
		t.Fatalf("Rollups() error = %v", err)
	}
	want := map[string]domain.Rollup{f.prID: {LatestReviewIDs: []string{}}}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Rollups(seeded PR) mismatch (-want +got):\n%s", diff)
	}
	if empty, err := f.store.Rollups(t.Context(), f.wsID, nil); err != nil || len(empty) != 0 {
		t.Errorf("Rollups(nil) = %v, %v, want empty", empty, err)
	}
}

func TestRollups_reviewsFindingsAndCost(t *testing.T) {
	f := seeded(t)
	f.withoutDemoReview(t)
	var agentA, agentB string
	for name, dest := range map[string]*string{"A": &agentA, "B": &agentB} {
		scan(t, f.db, dest, `INSERT INTO agents (workspace_id, name, provider, model, system_prompt)
			VALUES (?, ?, 'openai', 'm', 'p') RETURNING id`, f.wsID, name)
	}
	review := func(agent string, score int, at time.Time, kind string) string {
		var id string
		scan(t, f.db, &id, `INSERT INTO reviews (workspace_id, pr_id, agent_id, kind, score, created_at)
			VALUES (?, ?, ?, ?, ?, ?) RETURNING id`, f.wsID, f.prID, agent, kind, score, at)
		return id
	}
	finding := func(reviewID, severity string) {
		exec(t, f.db, `INSERT INTO findings (review_id, file, start_line, end_line, severity, category, title, rationale, confidence)
			VALUES (?, 'a.go', 1, 1, ?, 'bug', 't', 'r', 0.5)`, reviewID, severity)
	}
	base := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	a1 := review(agentA, 50, base, "review")
	a2 := review(agentA, 70, base.Add(time.Hour), "review")
	b1 := review(agentB, 90, base.Add(2*time.Hour), "review")
	summary := review(agentB, 10, base.Add(3*time.Hour), "summary") // not a review: ignored
	finding(a1, "CRITICAL")                                         // older review of A: not counted
	finding(a1, "CRITICAL")
	finding(a2, "CRITICAL")
	finding(a2, "WARNING")
	finding(a2, "WARNING")
	finding(b1, "SUGGESTION")
	finding(summary, "CRITICAL")
	for _, cost := range []any{0.5, nil, 0.25} {
		exec(t, f.db, `INSERT INTO agent_runs (workspace_id, pr_id, cost_usd) VALUES (?, ?, ?)`, f.wsID, f.prID, cost)
	}

	got, err := f.store.Rollups(t.Context(), f.wsID, []string{f.prID})
	if err != nil {
		t.Fatalf("Rollups() error = %v", err)
	}
	want := domain.Rollup{
		Score:           ptr(90),
		CostUSD:         ptr(0.75),
		LatestReviewID:  &b1,
		LastReviewedAt:  ptr(base.Add(2 * time.Hour)),
		LatestReviewIDs: []string{b1, a2},
		FindingsCounts:  &domain.FindingsCounts{Critical: 1, Warning: 2, Suggestion: 1},
	}
	if diff := cmp.Diff(want, got[f.prID], timeEqual); diff != "" {
		t.Errorf("Rollups() mismatch (-want +got):\n%s", diff)
	}
}

func TestRollups_unknownCostStaysNil(t *testing.T) {
	f := seeded(t)
	f.withoutDemoReview(t)
	exec(t, f.db, `INSERT INTO agent_runs (workspace_id, pr_id, cost_usd) VALUES (?, ?, NULL)`, f.wsID, f.prID)
	got, err := f.store.Rollups(t.Context(), f.wsID, []string{f.prID})
	if err != nil {
		t.Fatalf("Rollups() error = %v", err)
	}
	if got[f.prID].CostUSD != nil {
		t.Errorf("cost_usd with only unpriced runs = %v, want nil", *got[f.prID].CostUSD)
	}
}

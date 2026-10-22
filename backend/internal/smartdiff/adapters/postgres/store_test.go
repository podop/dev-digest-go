package postgres_test

import (
	"errors"
	"os"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"gorm.io/gorm"

	"dev-digest-go/backend/internal/platform/postgres/pgtest"
	"dev-digest-go/backend/internal/smartdiff/adapters/postgres"
	"dev-digest-go/backend/internal/smartdiff/domain"
)

func TestMain(m *testing.M) { os.Exit(pgtest.Main(m)) }

// fixture is a migrated database with two workspaces and a pull request of the first one.
type fixture struct {
	db    *gorm.DB
	store *postgres.Store
	ws    string
	other string
	repo  string
	pr    string
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	d := pgtest.Migrated(t)
	f := fixture{db: d.DB, store: postgres.NewStore(d.DB)}
	f.ws = f.insert(t, `INSERT INTO workspaces (name) VALUES ('default') RETURNING id`)
	f.other = f.insert(t, `INSERT INTO workspaces (name) VALUES ('other') RETURNING id`)
	f.repo = f.insert(t, `INSERT INTO repos (workspace_id, owner, name, full_name) VALUES (?, 'acme', 'api', 'acme/api') RETURNING id`, f.ws)
	f.pr = f.pull(t, f.repo, 1)
	return f
}

func (f fixture) insert(t *testing.T, query string, args ...any) string {
	t.Helper()
	var id string
	if err := f.db.Raw(query, args...).Scan(&id).Error; err != nil {
		t.Fatalf("fixture %q: %v", query, err)
	}
	return id
}

func (f fixture) exec(t *testing.T, query string, args ...any) {
	t.Helper()
	if err := f.db.Exec(query, args...).Error; err != nil {
		t.Fatalf("fixture %q: %v", query, err)
	}
}

func (f fixture) pull(t *testing.T, repo string, number int) string {
	t.Helper()
	return f.insert(t, `INSERT INTO pull_requests (workspace_id, repo_id, number, title, author, branch, base, head_sha)
		VALUES (?, ?, ?, 'Add rate limiting', 'dev', 'feat', 'main', 'a1b2c3d4e5f6') RETURNING id`, f.ws, repo, number)
}

func (f fixture) agent(t *testing.T, name string) string {
	t.Helper()
	return f.insert(t, `INSERT INTO agents (workspace_id, name, provider, model, system_prompt)
		VALUES (?, ?, 'openai', 'gpt', 'be careful') RETURNING id`, f.ws, name)
}

// review inserts a review of the pull request; agent "" is NULL; createdAt is an RFC 3339 time.
func (f fixture) review(t *testing.T, pr, agent, kind, createdAt string) string {
	t.Helper()
	var agentID *string
	if agent != "" {
		agentID = &agent
	}
	return f.insert(t, `INSERT INTO reviews (workspace_id, pr_id, agent_id, kind, created_at)
		VALUES (?, ?, ?, ?, ?::timestamptz) RETURNING id`, f.ws, pr, agentID, kind, createdAt)
}

func (f fixture) finding(t *testing.T, review, file string, line int, dismissed bool) {
	t.Helper()
	f.exec(t, `INSERT INTO findings (review_id, file, start_line, end_line, severity, category, title, rationale, confidence, dismissed_at)
		VALUES (?, ?, ?, ?, 'WARNING', 'bug', 't', 'r', 0.9, CASE WHEN ? THEN now() END)`, review, file, line, line, dismissed)
}

func TestPullFiles(t *testing.T) {
	f := newFixture(t)
	f.exec(t, `INSERT INTO pr_files (pr_id, path, additions, deletions) VALUES (?, 'src/b.go', 3, 1), (?, 'README.md', 1, 0), (?, 'Src/a.go', 5, 5)`, f.pr, f.pr, f.pr)
	otherPR := f.pull(t, f.repo, 2)
	f.exec(t, `INSERT INTO pr_files (pr_id, path, additions, deletions) VALUES (?, 'unrelated.go', 9, 9)`, otherPR)

	got, err := f.store.PullFiles(t.Context(), f.ws, f.pr)
	if err != nil {
		t.Fatalf("PullFiles() error = %v", err)
	}
	// Byte order: upper case before lower case.
	want := []domain.PullFile{
		{Path: "README.md", Additions: 1, Deletions: 0},
		{Path: "Src/a.go", Additions: 5, Deletions: 5},
		{Path: "src/b.go", Additions: 3, Deletions: 1},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("PullFiles() mismatch (-want +got):\n%s", diff)
	}
}

func TestPullFiles_NoFilesIsEmptyNotAnError(t *testing.T) {
	f := newFixture(t)
	got, err := f.store.PullFiles(t.Context(), f.ws, f.pr)
	if err != nil || len(got) != 0 {
		t.Errorf("PullFiles(no files) = %v, %v, want empty and no error", got, err)
	}
}

func TestPullFiles_PullRequestOfAnotherWorkspaceIsNotFound(t *testing.T) {
	f := newFixture(t)
	f.exec(t, `INSERT INTO pr_files (pr_id, path) VALUES (?, 'a.go')`, f.pr)
	const unknownPR = "00000000-0000-4000-8000-000000000000"
	for _, tc := range []struct{ name, ws, pr string }{
		{"other workspace", f.other, f.pr},
		{"unknown pull request", f.ws, unknownPR},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := f.store.PullFiles(t.Context(), tc.ws, tc.pr)
			if !errors.Is(err, domain.ErrNotFound) {
				t.Errorf("PullFiles(%s, %s) error = %v, want domain.ErrNotFound", tc.ws, tc.pr, err)
			}
		})
	}
}

// AC5006 (store): the newest review of each agent counts, an older run of the same agent does
// not, dismissed findings do, summary reviews and other pull requests do not.
func TestCurrentFindings_NewestReviewPerAgent(t *testing.T) {
	f := newFixture(t)
	a1, a2 := f.agent(t, "security"), f.agent(t, "style")

	old1 := f.review(t, f.pr, a1, "review", "2026-10-20T10:00:00Z")
	f.finding(t, old1, "a.go", 1, false) // replaced by the re-run
	new1 := f.review(t, f.pr, a1, "review", "2026-10-21T10:00:00Z")
	f.finding(t, new1, "a.go", 5, false)
	f.finding(t, new1, "a.go", 5, true) // dismissed still counts; the same line twice stays twice here
	f.finding(t, new1, "b.go", 7, true)
	only2 := f.review(t, f.pr, a2, "review", "2026-10-19T10:00:00Z")
	f.finding(t, only2, "c.go", 9, false)
	summary := f.review(t, f.pr, a2, "summary", "2026-10-22T10:00:00Z") // a newer summary must not hide the review
	f.finding(t, summary, "ignored.go", 1, false)
	nullOld := f.review(t, f.pr, "", "review", "2026-10-18T10:00:00Z")
	f.finding(t, nullOld, "n.go", 1, false)
	nullNew := f.review(t, f.pr, "", "review", "2026-10-18T11:00:00Z")
	f.finding(t, nullNew, "n.go", 2, false)
	otherPR := f.pull(t, f.repo, 2)
	foreign := f.review(t, otherPR, a1, "review", "2026-10-22T10:00:00Z")
	f.finding(t, foreign, "other.go", 1, false)

	got, err := f.store.CurrentFindings(t.Context(), f.pr)
	if err != nil {
		t.Fatalf("CurrentFindings() error = %v", err)
	}
	want := []domain.FindingRef{
		{File: "a.go", StartLine: 5}, {File: "a.go", StartLine: 5}, {File: "b.go", StartLine: 7},
		{File: "c.go", StartLine: 9}, {File: "n.go", StartLine: 2},
	}
	byFileLine := cmpopts.SortSlices(func(x, y domain.FindingRef) bool {
		return x.File < y.File || x.File == y.File && x.StartLine < y.StartLine
	})
	if diff := cmp.Diff(want, got, byFileLine); diff != "" {
		t.Errorf("CurrentFindings() mismatch (-want +got):\n%s", diff)
	}
}

// Two reviews of one agent with the same created_at: the smaller id wins (D5009).
func TestCurrentFindings_TieBreaksByID(t *testing.T) {
	f := newFixture(t)
	a := f.agent(t, "security")
	r1 := f.review(t, f.pr, a, "review", "2026-10-21T10:00:00Z")
	r2 := f.review(t, f.pr, a, "review", "2026-10-21T10:00:00Z")
	f.finding(t, r1, "one.go", 1, false)
	f.finding(t, r2, "two.go", 2, false)
	winner := map[bool]string{true: "one.go", false: "two.go"}[r1 < r2]

	got, err := f.store.CurrentFindings(t.Context(), f.pr)
	if err != nil {
		t.Fatalf("CurrentFindings() error = %v", err)
	}
	if len(got) != 1 || got[0].File != winner {
		t.Errorf("CurrentFindings() = %v, want only the findings of the review with the smaller id (%s)", got, winner)
	}
}

func TestCurrentFindings_NoReviewIsEmpty(t *testing.T) {
	f := newFixture(t)
	got, err := f.store.CurrentFindings(t.Context(), f.pr)
	if err != nil || len(got) != 0 {
		t.Errorf("CurrentFindings(no review) = %v, %v, want empty and no error", got, err)
	}
}

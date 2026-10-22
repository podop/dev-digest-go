package postgres_test

import (
	"errors"
	"os"
	"testing"

	"github.com/google/go-cmp/cmp"
	"gorm.io/gorm"

	"dev-digest-go/backend/internal/blast/adapters/postgres"
	"dev-digest-go/backend/internal/blast/domain"
	"dev-digest-go/backend/internal/platform/postgres/pgtest"
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
	f.pr = f.pull(t, f.repo, 5)
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

func TestFindPull(t *testing.T) {
	f := newFixture(t)
	got, err := f.store.FindPull(t.Context(), f.ws, f.pr)
	if err != nil {
		t.Fatalf("FindPull() error = %v", err)
	}
	want := domain.Pull{ID: f.pr, RepoID: f.repo, Number: 5, Owner: "acme", Name: "api"}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("FindPull() mismatch (-want +got):\n%s", diff)
	}
}

func TestFindPull_NotInTheWorkspaceIsNotFound(t *testing.T) {
	f := newFixture(t)
	const unknown = "00000000-0000-4000-8000-000000000000"
	for _, tc := range []struct{ name, ws, pr string }{
		{"other workspace", f.other, f.pr},
		{"unknown pull request", f.ws, unknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := f.store.FindPull(t.Context(), tc.ws, tc.pr)
			if !errors.Is(err, domain.ErrNotFound) {
				t.Errorf("FindPull(%s, %s) error = %v, want domain.ErrNotFound", tc.ws, tc.pr, err)
			}
		})
	}
}

func TestChangedPaths_ByteOrderOfThisPullOnly(t *testing.T) {
	f := newFixture(t)
	f.exec(t, `INSERT INTO pr_files (pr_id, path) VALUES (?, 'src/b.go'), (?, 'README.md'), (?, 'Src/a.go'), (?, 'src/a.go')`, f.pr, f.pr, f.pr, f.pr)
	otherPR := f.pull(t, f.repo, 6)
	f.exec(t, `INSERT INTO pr_files (pr_id, path) VALUES (?, 'unrelated.go')`, otherPR)

	got, err := f.store.ChangedPaths(t.Context(), f.pr)
	if err != nil {
		t.Fatalf("ChangedPaths() error = %v", err)
	}
	// Byte order: upper case before lower case.
	want := []string{"README.md", "Src/a.go", "src/a.go", "src/b.go"}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("ChangedPaths() mismatch (-want +got):\n%s", diff)
	}
}

func TestChangedPaths_NoFilesIsEmptyNotAnError(t *testing.T) {
	f := newFixture(t)
	got, err := f.store.ChangedPaths(t.Context(), f.pr)
	if err != nil || len(got) != 0 {
		t.Errorf("ChangedPaths(no files) = %v, %v, want empty and no error", got, err)
	}
}

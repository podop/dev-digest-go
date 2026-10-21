package postgres_test

import (
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"gorm.io/gorm"

	"dev-digest-go/backend/internal/platform/postgres/pgtest"
	"dev-digest-go/backend/internal/repos/adapters/postgres"
	"dev-digest-go/backend/internal/repos/app"
	"dev-digest-go/backend/internal/repos/domain"
)

func TestMain(m *testing.M) { os.Exit(pgtest.Main(m)) }

// fixture is a migrated database with two workspaces and a user.
type fixture struct {
	db    *gorm.DB
	store *postgres.Store
	ws    string
	other string
	user  string
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	d := pgtest.Migrated(t)
	f := fixture{db: d.DB, store: postgres.NewStore(d.DB)}
	insert := func(query string, args ...any) string {
		var id string
		if err := d.DB.Raw(query, args...).Scan(&id).Error; err != nil {
			t.Fatalf("fixture %q: %v", query, err)
		}
		return id
	}
	f.ws = insert(`INSERT INTO workspaces (name) VALUES ('default') RETURNING id`)
	f.other = insert(`INSERT INTO workspaces (name) VALUES ('other') RETURNING id`)
	f.user = insert(`INSERT INTO users (email, name) VALUES ('you@local', 'You') RETURNING id`)
	return f
}

func add(t *testing.T, f fixture, wsID, owner, name string) (domain.Repo, bool) {
	t.Helper()
	repo, created, err := f.store.Add(t.Context(), app.NewRepo{WorkspaceID: wsID, CreatedBy: f.user, Ref: domain.Ref{Owner: owner, Name: name}})
	if err != nil {
		t.Fatalf("Add(%s/%s) error = %v", owner, name, err)
	}
	return repo, created
}

func TestStore_Add(t *testing.T) {
	f := newFixture(t)

	repo, created := add(t, f, f.ws, "o", "r")
	if !created {
		t.Error("Add(new) created = false, want true")
	}
	if repo.ID == "" || repo.WorkspaceID != f.ws || repo.Owner != "o" || repo.Name != "r" || repo.FullName != "o/r" ||
		repo.DefaultBranch != "main" || repo.ClonePath != nil || repo.LastPolledAt != nil ||
		repo.CreatedBy == nil || *repo.CreatedBy != f.user {
		t.Errorf("Add(new) repo = %+v, want o/r on main created by the user, not cloned", repo)
	}

	again, created := add(t, f, f.ws, "o", "r")
	if created || again.ID != repo.ID {
		t.Errorf("Add(existing) = %+v, created=%v, want the same repo and created=false", again, created)
	}

	upper, created := add(t, f, f.ws, "O", "r")
	if !created || upper.ID == repo.ID {
		t.Errorf("Add(O/r) = %+v, created=%v, want a different repo (case-sensitive)", upper, created)
	}

	elsewhere, created := add(t, f, f.other, "o", "r")
	if !created || elsewhere.ID == repo.ID {
		t.Errorf("Add(o/r in other workspace) = %+v, created=%v, want a new repo", elsewhere, created)
	}
}

func TestStore_Add_Concurrent(t *testing.T) {
	f := newFixture(t)
	const n = 12

	type result struct {
		id      string
		created bool
		err     error
	}
	results := make([]result, n)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range n {
		wg.Go(func() {
			<-start
			repo, created, err := f.store.Add(t.Context(), app.NewRepo{WorkspaceID: f.ws, CreatedBy: f.user, Ref: domain.Ref{Owner: "o", Name: "c"}})
			results[i] = result{repo.ID, created, err}
		})
	}
	close(start)
	wg.Wait()

	createdCount := 0
	for i, r := range results {
		if r.err != nil {
			t.Fatalf("Add #%d error = %v", i, r.err)
		}
		if r.id != results[0].id {
			t.Errorf("Add #%d id = %s, want %s", i, r.id, results[0].id)
		}
		if r.created {
			createdCount++
		}
	}
	if createdCount != 1 {
		t.Errorf("created results = %d, want exactly 1", createdCount)
	}
	var rows int64
	if err := f.db.Table("repos").Where("full_name = 'o/c'").Count(&rows).Error; err != nil || rows != 1 {
		t.Errorf("repo rows = %d, %v, want 1", rows, err)
	}
}

func TestStore_List_OrderAndScope(t *testing.T) {
	f := newFixture(t)
	for _, name := range []string{"c", "a", "b"} {
		add(t, f, f.ws, "o", name)
	}
	add(t, f, f.other, "o", "z")

	got, err := f.store.List(t.Context(), f.ws)
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	var names []string
	for _, r := range got {
		names = append(names, r.Name)
	}
	if want := "c,a,b"; join(names) != want {
		t.Errorf("List() order = %s, want %s (creation order)", join(names), want)
	}
	empty, err := f.store.List(t.Context(), "00000000-0000-0000-0000-000000000000")
	if err != nil || len(empty) != 0 {
		t.Errorf("List(unknown workspace) = %v, %v, want empty", empty, err)
	}
}

func join(s []string) string {
	out := ""
	for i, v := range s {
		if i > 0 {
			out += ","
		}
		out += v
	}
	return out
}

func TestStore_GetAndDelete_WorkspaceScoped(t *testing.T) {
	f := newFixture(t)
	repo, _ := add(t, f, f.other, "o", "r")

	if _, err := f.store.Get(t.Context(), f.ws, repo.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("Get(other workspace's repo) error = %v, want ErrNotFound", err)
	}
	if err := f.store.Delete(t.Context(), f.ws, repo.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("Delete(other workspace's repo) error = %v, want ErrNotFound", err)
	}
	if _, err := f.store.GetByID(t.Context(), repo.ID); err != nil {
		t.Errorf("GetByID() error = %v, want the row to still exist", err)
	}
	if _, err := f.store.Get(t.Context(), f.other, "00000000-0000-0000-0000-000000000000"); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("Get(unknown id) error = %v, want ErrNotFound", err)
	}
	if err := f.store.Delete(t.Context(), f.other, repo.ID); err != nil {
		t.Errorf("Delete() error = %v", err)
	}
	if err := f.store.Delete(t.Context(), f.other, repo.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("second Delete() error = %v, want ErrNotFound", err)
	}
}

func TestStore_Delete_Cascades(t *testing.T) {
	f := newFixture(t)
	repo, _ := add(t, f, f.ws, "o", "r")
	exec := func(q string, args ...any) {
		t.Helper()
		if err := f.db.Exec(q, args...).Error; err != nil {
			t.Fatalf("%q: %v", q, err)
		}
	}
	var prID string
	if err := f.db.Raw(`INSERT INTO pull_requests (workspace_id, repo_id, number, title, author, branch, base, head_sha)
		VALUES (?, ?, 1, 't', 'a', 'b', 'main', 'sha') RETURNING id`, f.ws, repo.ID).Scan(&prID).Error; err != nil {
		t.Fatal(err)
	}
	exec(`INSERT INTO pr_files (pr_id, path) VALUES (?, 'a.go')`, prID)
	exec(`INSERT INTO pr_commits (pr_id, sha, message, author) VALUES (?, 'abc', 'm', 'a')`, prID)

	if err := f.store.Delete(t.Context(), f.ws, repo.ID); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	for _, table := range []string{"repos", "pull_requests", "pr_files", "pr_commits"} {
		var n int64
		if err := f.db.Table(table).Count(&n).Error; err != nil || n != 0 {
			t.Errorf("rows in %s after delete = %d, %v, want 0", table, n, err)
		}
	}
}

func TestStore_SetClonedAndPolled(t *testing.T) {
	f := newFixture(t)
	repo, _ := add(t, f, f.ws, "o", "r")
	at := time.Date(2026, 10, 21, 9, 15, 0, 0, time.UTC)

	if err := f.store.SetCloned(t.Context(), repo.ID, "/data/workspace/o/r", at); err != nil {
		t.Fatalf("SetCloned() error = %v", err)
	}
	got, err := f.store.GetByID(t.Context(), repo.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ClonePath == nil || *got.ClonePath != "/data/workspace/o/r" || got.LastPolledAt == nil || !got.LastPolledAt.Equal(at) {
		t.Errorf("after SetCloned = %+v, want clone path and last_polled_at %v", got, at)
	}

	later := at.Add(time.Hour)
	if err := f.store.SetPolled(t.Context(), repo.ID, later); err != nil {
		t.Fatalf("SetPolled() error = %v", err)
	}
	got, _ = f.store.GetByID(t.Context(), repo.ID)
	if got.LastPolledAt == nil || !got.LastPolledAt.Equal(later) || got.ClonePath == nil {
		t.Errorf("after SetPolled = %+v, want last_polled_at %v and clone path kept", got, later)
	}

	const gone = "00000000-0000-0000-0000-000000000000"
	if err := f.store.SetCloned(t.Context(), gone, "/x", at); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("SetCloned(unknown) error = %v, want ErrNotFound", err)
	}
	if err := f.store.SetPolled(t.Context(), gone, at); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("SetPolled(unknown) error = %v, want ErrNotFound", err)
	}
}

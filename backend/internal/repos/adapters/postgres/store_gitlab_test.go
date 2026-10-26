package postgres_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"dev-digest-go/backend/internal/platform/codehost"
	pg "dev-digest-go/backend/internal/platform/postgres"
	"dev-digest-go/backend/internal/repos/app"
	"dev-digest-go/backend/internal/repos/domain"
)

const glHost = "gitlab.example.com"

// newGitLab is a GitLab repo of the fixture's workspace on glHost.
func newGitLab(f fixture, wsID string, projectID int64, owner, name string) app.NewRepo {
	ref := domain.Ref{Owner: owner, Name: name}
	return app.NewRepo{
		WorkspaceID: wsID, CreatedBy: f.user, Ref: ref,
		Provider: codehost.GitLab, Host: glHost, ProjectID: projectID,
		WebURL: codehost.WebURL("https://"+glHost, ref.FullName()), DefaultBranch: "trunk",
	}
}

func addGitLab(t *testing.T, f fixture, in app.NewRepo) (domain.Repo, bool) {
	t.Helper()
	repo, created, err := f.store.Add(t.Context(), in)
	if err != nil {
		t.Fatalf("Add(%s) error = %v", in.Ref.FullName(), err)
	}
	return repo, created
}

// connect inserts a connection for glHost and returns its id.
func connect(t *testing.T, f fixture, host, base string) string {
	t.Helper()
	var id string
	if err := f.db.Raw(`INSERT INTO gitlab_connections (name, base_url, host) VALUES ('corp', ?, ?) RETURNING id`, base, host).
		Scan(&id).Error; err != nil {
		t.Fatalf("insert connection: %v", err)
	}
	return id
}

func TestStore_Add_GitLab(t *testing.T) {
	f := newFixture(t)
	in := newGitLab(f, f.ws, 42, "acme/platform", "web")

	repo, created := addGitLab(t, f, in)
	if !created {
		t.Error("Add(new GitLab repo) created = false, want true")
	}
	if repo.Provider != codehost.GitLab || repo.Host != glHost || repo.ProjectID != 42 ||
		repo.WebURL != "https://gitlab.example.com/acme/platform/web" || repo.Owner != "acme/platform" ||
		repo.Name != "web" || repo.FullName != "acme/platform/web" || repo.DefaultBranch != "trunk" ||
		repo.ConnectionID != nil {
		t.Errorf("Add(GitLab) repo = %+v, want the GitLab columns stored and no connection", repo)
	}

	again, created := addGitLab(t, f, in)
	if created || again.ID != repo.ID {
		t.Errorf("Add(same project) = %+v, created=%v, want the same repo and created=false", again, created)
	}

	// The same project under a new path is the same repository (identity is the project id).
	renamed := newGitLab(f, f.ws, 42, "acme/platform", "web2")
	again, created = addGitLab(t, f, renamed)
	if created || again.ID != repo.ID || again.FullName != "acme/platform/web" {
		t.Errorf("Add(project 42 under a new path) = %+v, created=%v, want the stored repo unchanged", again, created)
	}

	// Another project holding a stale path: the stored repo comes back with its own project id.
	stale, created := addGitLab(t, f, newGitLab(f, f.ws, 43, "acme/platform", "web"))
	if created || stale.ID != repo.ID || stale.ProjectID != 42 {
		t.Errorf("Add(project 43 on path of 42) = %+v, created=%v, want repo of 42, created=false", stale, created)
	}

	other, created := addGitLab(t, f, newGitLab(f, f.other, 42, "acme/platform", "web"))
	if !created || other.ID == repo.ID {
		t.Errorf("Add(same project in another workspace) = %+v, created=%v, want a new repo", other, created)
	}
}

func TestStore_Add_GitHubAndGitLabCoexist(t *testing.T) {
	f := newFixture(t)
	gh, created := add(t, f, f.ws, "acme", "web")
	if !created || gh.Provider != codehost.GitHub || gh.Host != codehost.GitHubHost || gh.ProjectID != 0 || gh.WebURL != "" {
		t.Fatalf("Add(GitHub) = %+v, created=%v, want provider github on github.com without project id", gh, created)
	}
	gl, created := addGitLab(t, f, newGitLab(f, f.ws, 7, "acme", "web"))
	if !created || gl.ID == gh.ID {
		t.Errorf("Add(GitLab acme/web) = %+v, created=%v, want a repo next to the GitHub one", gl, created)
	}
	again, created := add(t, f, f.ws, "acme", "web")
	if created || again.ID != gh.ID || again.Provider != codehost.GitHub {
		t.Errorf("Add(GitHub acme/web again) = %+v, created=%v, want the GitHub repo", again, created)
	}
	// A second GitLab host with the same path is a third repo.
	in := newGitLab(f, f.ws, 7, "acme", "web")
	in.Host = "other.example.com"
	if other, created := addGitLab(t, f, in); !created || other.ID == gl.ID {
		t.Errorf("Add(acme/web on another host) = %+v, created=%v, want a new repo", other, created)
	}
}

func TestStore_Add_GitLab_Concurrent(t *testing.T) {
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
			// Half the callers see the project under the old path, half under the new one:
			// one insert wins on either unique index.
			name := "web"
			if i%2 == 1 {
				name = "web-new"
			}
			repo, created, err := f.store.Add(t.Context(), newGitLab(f, f.ws, 9, "acme", name))
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
	if err := f.db.Table("repos").Where("project_id = 9").Count(&rows).Error; err != nil || rows != 1 {
		t.Errorf("repo rows of project 9 = %d, %v, want 1", rows, err)
	}
}

func TestStore_Add_GitLab_ConcurrentSamePathOtherProject(t *testing.T) {
	f := newFixture(t)
	const n = 8
	ids := make([]string, n)
	created := make([]bool, n)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range n {
		wg.Go(func() {
			<-start
			repo, c, err := f.store.Add(t.Context(), newGitLab(f, f.ws, int64(100+i), "acme", "web"))
			if err != nil {
				t.Errorf("Add #%d error = %v", i, err)
				return
			}
			ids[i], created[i] = repo.ID, c
		})
	}
	close(start)
	wg.Wait()

	count := 0
	for i := range n {
		if created[i] {
			count++
		}
		if ids[i] != ids[0] {
			t.Errorf("Add #%d id = %s, want %s (the one stored repo)", i, ids[i], ids[0])
		}
	}
	if count != 1 {
		t.Errorf("created results = %d, want exactly 1", count)
	}
}

func TestStore_ConnectionBinding(t *testing.T) {
	f := newFixture(t)
	gh, _ := add(t, f, f.ws, "o", "r")
	gl, _ := addGitLab(t, f, newGitLab(f, f.ws, 5, "acme", "web"))

	conn := connect(t, f, glHost, "https://gitlab.example.com/gl")
	// A connection on another host binds nothing.
	connect(t, f, "elsewhere.example.com", "https://elsewhere.example.com")

	want := func(t *testing.T, repo domain.Repo, wantConn *string, wantWeb string) {
		t.Helper()
		switch {
		case (repo.ConnectionID == nil) != (wantConn == nil), repo.ConnectionID != nil && *repo.ConnectionID != *wantConn:
			t.Errorf("%s ConnectionID = %v, want %v", repo.FullName, deref(repo.ConnectionID), deref(wantConn))
		case repo.WebURL != wantWeb:
			t.Errorf("%s WebURL = %q, want %q", repo.FullName, repo.WebURL, wantWeb)
		}
	}
	checkAll := func(t *testing.T, wantConn *string, wantWeb string) {
		t.Helper()
		list, err := f.store.List(t.Context(), f.ws)
		if err != nil || len(list) != 2 {
			t.Fatalf("List() = %v, %v, want 2 repos", list, err)
		}
		want(t, list[0], nil, "")
		want(t, list[1], wantConn, wantWeb)
		got, err := f.store.Get(t.Context(), f.ws, gl.ID)
		if err != nil {
			t.Fatalf("Get() error = %v", err)
		}
		want(t, got, wantConn, wantWeb)
		byID, err := f.store.GetByID(t.Context(), gl.ID)
		if err != nil {
			t.Fatalf("GetByID() error = %v", err)
		}
		want(t, byID, wantConn, wantWeb)
	}

	// Connected: the id of the connection and its base URL in the web address (FR16).
	checkAll(t, &conn, "https://gitlab.example.com/gl/acme/web")
	if got, _ := f.store.Get(t.Context(), f.ws, gh.ID); got.ConnectionID != nil {
		t.Errorf("GitHub repo ConnectionID = %v, want nil", *got.ConnectionID)
	}

	// Deleting the connection keeps the repo, not connected, with the stored web URL (AC5, FR48).
	if err := f.db.Exec(`DELETE FROM gitlab_connections WHERE id = ?`, conn).Error; err != nil {
		t.Fatal(err)
	}
	checkAll(t, nil, "https://gitlab.example.com/acme/web")

	// A new connection for the same host key re-binds it, with no change to the repo (D26).
	again := connect(t, f, glHost, "https://gitlab.example.com")
	checkAll(t, &again, "https://gitlab.example.com/acme/web")
}

func deref(s *string) string {
	if s == nil {
		return "<nil>"
	}
	return *s
}

func TestStore_Add_ReturnsConnection(t *testing.T) {
	f := newFixture(t)
	conn := connect(t, f, glHost, "https://gitlab.example.com")
	repo, _ := addGitLab(t, f, newGitLab(f, f.ws, 5, "acme", "web"))
	if repo.ConnectionID == nil || *repo.ConnectionID != conn {
		t.Errorf("Add() ConnectionID = %v, want %s", deref(repo.ConnectionID), conn)
	}
}

func TestStore_UpdatePath(t *testing.T) {
	f := newFixture(t)
	a, _ := addGitLab(t, f, newGitLab(f, f.ws, 1, "acme", "old"))
	b, _ := addGitLab(t, f, newGitLab(f, f.ws, 2, "acme", "taken"))
	moved := app.ProjectPath{
		FullName: "acme/sub/new", Owner: "acme/sub", Name: "new", WebURL: "https://gitlab.example.com/acme/sub/new",
	}

	skipped, err := f.store.UpdatePath(t.Context(), f.ws, a.ID, moved)
	if err != nil || skipped != "" {
		t.Fatalf("UpdatePath() = %q, %v, want it applied", skipped, err)
	}
	got, _ := f.store.Get(t.Context(), f.ws, a.ID)
	if got.FullName != "acme/sub/new" || got.Owner != "acme/sub" || got.Name != "new" ||
		got.WebURL != "https://gitlab.example.com/acme/sub/new" || got.ProjectID != 1 || got.DefaultBranch != "trunk" {
		t.Errorf("repo after UpdatePath = %+v, want the new path, same project id and branch", got)
	}

	// The same path again is not a collision with itself.
	if skipped, err := f.store.UpdatePath(t.Context(), f.ws, a.ID, moved); err != nil || skipped != "" {
		t.Errorf("UpdatePath(unchanged) = %q, %v, want applied", skipped, err)
	}

	// A path held by another repo of the workspace changes nothing and names the holder.
	taken := app.ProjectPath{FullName: "acme/taken", Owner: "acme", Name: "taken", WebURL: "https://gitlab.example.com/acme/taken"}
	skipped, err = f.store.UpdatePath(t.Context(), f.ws, a.ID, taken)
	if err != nil || skipped != b.ID {
		t.Errorf("UpdatePath(taken path) = %q, %v, want skipped for %s", skipped, err, b.ID)
	}
	if got, _ := f.store.Get(t.Context(), f.ws, a.ID); got.FullName != "acme/sub/new" {
		t.Errorf("full name after a skipped update = %q, want acme/sub/new", got.FullName)
	}

	// The path of the same name in another workspace is not a collision.
	if skipped, err := f.store.UpdatePath(t.Context(), f.other, mustAdd(t, f, f.other, 3, "x", "y").ID, taken); err != nil || skipped != "" {
		t.Errorf("UpdatePath(path held in another workspace) = %q, %v, want applied", skipped, err)
	}
}

func mustAdd(t *testing.T, f fixture, wsID string, project int64, owner, name string) domain.Repo {
	t.Helper()
	repo, _ := addGitLab(t, f, newGitLab(f, wsID, project, owner, name))
	return repo
}

func TestStore_UpdatePath_NotFound(t *testing.T) {
	f := newFixture(t)
	p := app.ProjectPath{FullName: "a/b", Owner: "a", Name: "b", WebURL: "https://gitlab.example.com/a/b"}
	const gone = "00000000-0000-0000-0000-000000000000"
	if _, err := f.store.UpdatePath(t.Context(), f.ws, gone, p); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("UpdatePath(unknown id) error = %v, want ErrNotFound", err)
	}
	gl := mustAdd(t, f, f.other, 1, "x", "y")
	if _, err := f.store.UpdatePath(t.Context(), f.ws, gl.ID, p); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("UpdatePath(repo of another workspace) error = %v, want ErrNotFound", err)
	}
	gh, _ := add(t, f, f.ws, "o", "r")
	if _, err := f.store.UpdatePath(t.Context(), f.ws, gh.ID, p); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("UpdatePath(GitHub repo) error = %v, want ErrNotFound", err)
	}
	if got, _ := f.store.Get(t.Context(), f.ws, gh.ID); got.FullName != "o/r" || got.WebURL != "" {
		t.Errorf("GitHub repo after a refused update = %+v, want it unchanged", got)
	}
}

func TestStore_UsesTransactionOfContext(t *testing.T) {
	f := newFixture(t)
	// A write inside a rolled-back InTx must not survive: the store takes its handle from the ctx.
	tx := pg.NewTx(f.db)
	sentinel := errors.New("roll back")
	err := tx.InTx(t.Context(), func(ctx context.Context) error {
		if _, _, err := f.store.Add(ctx, newGitLab(f, f.ws, 77, "acme", "tx")); err != nil {
			return err
		}
		if got, err := f.store.List(ctx, f.ws); err != nil || len(got) != 1 {
			t.Errorf("List() inside the transaction = %v, %v, want the uncommitted repo", got, err)
		}
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("InTx() error = %v, want the sentinel", err)
	}
	if got, err := f.store.List(t.Context(), f.ws); err != nil || len(got) != 0 {
		t.Errorf("List() after rollback = %v, %v, want none", got, err)
	}
}

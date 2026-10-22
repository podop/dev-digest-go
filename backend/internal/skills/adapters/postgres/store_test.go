package postgres_test

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"gorm.io/gorm"

	pg "dev-digest-go/backend/internal/platform/postgres"
	"dev-digest-go/backend/internal/platform/postgres/pgtest"
	"dev-digest-go/backend/internal/skills/adapters/postgres"
	"dev-digest-go/backend/internal/skills/domain"
)

func TestMain(m *testing.M) { os.Exit(pgtest.Main(m)) }

// fixture is a migrated database with a workspace, a second workspace, a repo and a pull request.
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
	f.repo = f.insert(t, `INSERT INTO repos (workspace_id, owner, name, full_name) VALUES (?, 'o', 'r', 'o/r') RETURNING id`, f.ws)
	f.pr = f.insert(t, `INSERT INTO pull_requests (workspace_id, repo_id, number, title, author, branch, base, head_sha)
		VALUES (?, ?, 1, 't', 'a', 'b', 'main', 'sha') RETURNING id`, f.ws, f.repo)
	return f
}

// insert runs a statement that ends in RETURNING id and returns the id.
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

func (f fixture) count(t *testing.T, query string, args ...any) int {
	t.Helper()
	var n int
	if err := f.db.Raw(query, args...).Scan(&n).Error; err != nil {
		t.Fatalf("fixture %q: %v", query, err)
	}
	return n
}

func (f fixture) agent(t *testing.T, ws, name string, enabled bool) string {
	t.Helper()
	return f.insert(t, `INSERT INTO agents (workspace_id, name, provider, model, system_prompt, enabled)
		VALUES (?, ?, 'openai', 'm', 'p', ?) RETURNING id`, ws, name, enabled)
}

func (f fixture) link(t *testing.T, agent, skill string, order int) {
	t.Helper()
	f.exec(t, `INSERT INTO agent_skills (agent_id, skill_id, "order") VALUES (?, ?, ?)`, agent, skill, order)
}

func (f fixture) skill(t *testing.T, name string) domain.Skill {
	t.Helper()
	return f.skillIn(t, f.ws, name)
}

func (f fixture) skillIn(t *testing.T, ws, name string) domain.Skill {
	t.Helper()
	sk, err := f.store.Insert(t.Context(), domain.NewSkill{
		WorkspaceID: ws, Name: name, Description: "d", Type: domain.TypeCustom, Source: domain.SourceManual, Body: "b", Enabled: true,
	})
	if err != nil {
		t.Fatalf("Insert(%q) error = %v", name, err)
	}
	return sk
}

func TestStore_Insert(t *testing.T) {
	f := newFixture(t)
	ref := "https://h/x.md"
	got, err := f.store.Insert(t.Context(), domain.NewSkill{
		WorkspaceID: f.ws, Name: "x", Description: "d", Type: domain.TypeRubric, Source: domain.SourceImportedURL,
		SourceRef: &ref, Body: "body", Enabled: false,
	})
	if err != nil {
		t.Fatalf("Insert error = %v", err)
	}
	if got.ID == "" || got.CreatedAt.IsZero() || got.UpdatedAt.IsZero() {
		t.Errorf("Insert returned id=%q created=%v updated=%v, want database-filled values", got.ID, got.CreatedAt, got.UpdatedAt)
	}
	want := domain.Skill{
		ID: got.ID, WorkspaceID: f.ws, Name: "x", Description: "d", Type: domain.TypeRubric, Source: domain.SourceImportedURL,
		SourceRef: &ref, Body: "body", Enabled: false, Version: 1, CreatedAt: got.CreatedAt, UpdatedAt: got.UpdatedAt,
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Insert mismatch (-want +got):\n%s", diff)
	}
	reread, err := f.store.Get(t.Context(), f.ws, got.ID)
	if err != nil {
		t.Fatalf("Get error = %v", err)
	}
	if diff := cmp.Diff(got, reread, cmpopts.EquateApproxTime(time.Millisecond)); diff != "" {
		t.Errorf("Get after Insert mismatch (-insert +get):\n%s", diff)
	}

	if _, err := f.store.Insert(t.Context(), domain.NewSkill{WorkspaceID: f.ws, Name: "x", Type: domain.TypeCustom, Source: domain.SourceManual, Body: "b"}); !errors.Is(err, domain.ErrDuplicateName) {
		t.Errorf("Insert(duplicate name) error = %v, want ErrDuplicateName", err)
	}
	// The same name in another workspace is fine.
	f.skillIn(t, f.other, "x")
}

func TestStore_GetIsWorkspaceScoped(t *testing.T) {
	f := newFixture(t)
	sk := f.skill(t, "x")
	if _, err := f.store.Get(t.Context(), f.other, sk.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("Get(other workspace) error = %v, want ErrNotFound", err)
	}
	if _, err := f.store.Lock(t.Context(), f.other, sk.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("Lock(other workspace) error = %v, want ErrNotFound", err)
	}
}

func TestStore_ListOrdersByNameInByteOrderWithUsedBy(t *testing.T) {
	f := newFixture(t)
	b, a := f.skill(t, "b"), f.skill(t, "a")
	f.skill(t, "a-2")
	f.skillIn(t, f.other, "zzz")
	x, y := f.agent(t, f.ws, "x", true), f.agent(t, f.ws, "y", true)
	f.link(t, x, a.ID, 0)
	f.link(t, y, a.ID, 0)
	f.link(t, x, b.ID, 1)

	got, err := f.store.List(t.Context(), f.ws)
	if err != nil {
		t.Fatalf("List error = %v", err)
	}
	type line struct {
		Name   string
		UsedBy int
	}
	var lines []line
	for _, s := range got {
		lines = append(lines, line{s.Name, s.UsedBy})
	}
	want := []line{{"a", 2}, {"a-2", 0}, {"b", 1}}
	if diff := cmp.Diff(want, lines); diff != "" {
		t.Errorf("List mismatch (-want +got):\n%s", diff)
	}
}

func TestStore_UpdateWritesOnlySentFieldsAndTouchesUpdatedAt(t *testing.T) {
	f := newFixture(t)
	sk := f.skill(t, "x")
	f.skill(t, "taken")

	body, next, name := "new body", 2, "y"
	got, err := f.store.Update(t.Context(), f.ws, sk.ID, domain.Write{Body: &body, Version: &next, Name: &name})
	if err != nil {
		t.Fatalf("Update error = %v", err)
	}
	if got.Body != body || got.Version != 2 || got.Name != "y" || got.Description != "d" || !got.Enabled {
		t.Errorf("Update result = %+v, want body, version 2 and name y changed, rest kept", got)
	}
	if !got.UpdatedAt.After(sk.UpdatedAt) {
		t.Errorf("Update left updated_at %v, want later than %v", got.UpdatedAt, sk.UpdatedAt)
	}

	if _, err := f.store.Update(t.Context(), f.ws, sk.ID, domain.Write{}); err != nil {
		t.Errorf("Update(empty write) error = %v, want nil (only updated_at changes)", err)
	}
	taken := "taken"
	if _, err := f.store.Update(t.Context(), f.ws, sk.ID, domain.Write{Name: &taken}); !errors.Is(err, domain.ErrDuplicateName) {
		t.Errorf("Update(duplicate name) error = %v, want ErrDuplicateName", err)
	}
	if _, err := f.store.Update(t.Context(), f.other, sk.ID, domain.Write{Name: &name}); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("Update(other workspace) error = %v, want ErrNotFound", err)
	}
}

func TestStore_LockBlocksConcurrentWriterUntilCommit(t *testing.T) {
	f := newFixture(t)
	sk := f.skill(t, "x")
	tx := pg.NewTx(f.db)

	locked := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- tx.InTx(context.WithoutCancel(t.Context()), func(ctx context.Context) error {
			if _, err := f.store.Lock(ctx, f.ws, sk.ID); err != nil {
				return err
			}
			close(locked)
			<-release
			return nil
		})
	}()
	<-locked

	second := make(chan time.Time, 1)
	go func() {
		_ = tx.InTx(context.WithoutCancel(t.Context()), func(ctx context.Context) error {
			_, err := f.store.Lock(ctx, f.ws, sk.ID)
			second <- time.Now()
			return err
		})
	}()
	select {
	case <-second:
		t.Fatal("second Lock returned while the first transaction held the row")
	case <-time.After(200 * time.Millisecond):
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatalf("first transaction error = %v", err)
	}
	select {
	case <-second:
	case <-time.After(5 * time.Second):
		t.Fatal("second Lock did not proceed after the first transaction ended")
	}
}

func TestStore_Versions(t *testing.T) {
	f := newFixture(t)
	sk := f.skill(t, "x")
	for v, msg := range []string{"Created", "Edited body"} {
		if err := f.store.InsertVersion(t.Context(), domain.NewVersion{SkillID: sk.ID, Version: v + 1, Body: "b", Description: "d", Message: msg}); err != nil {
			t.Fatalf("InsertVersion(%d) error = %v", v+1, err)
		}
	}
	got, err := f.store.ListVersions(t.Context(), sk.ID)
	if err != nil {
		t.Fatalf("ListVersions error = %v", err)
	}
	if len(got) != 2 || got[0].Version != 2 || got[1].Version != 1 {
		t.Fatalf("ListVersions = %+v, want versions 2, 1", got)
	}
	if got[0].Message == nil || *got[0].Message != "Edited body" || got[0].Description == nil || *got[0].Description != "d" {
		t.Errorf("ListVersions[0] = %+v, want message and description set", got[0])
	}
	one, err := f.store.GetVersion(t.Context(), sk.ID, 1)
	if err != nil || one.Version != 1 {
		t.Errorf("GetVersion(1) = %+v, %v, want version 1", one, err)
	}
	if _, err := f.store.GetVersion(t.Context(), sk.ID, 9); !errors.Is(err, domain.ErrVersionNotFound) {
		t.Errorf("GetVersion(9) error = %v, want ErrVersionNotFound", err)
	}
}

func TestStore_DeleteCascadesAndKeepsReferences(t *testing.T) {
	f := newFixture(t)
	s, other := f.skill(t, "s"), f.skill(t, "t")
	x, y := f.agent(t, f.ws, "x", true), f.agent(t, f.ws, "y", true)
	f.link(t, x, s.ID, 0)
	f.link(t, x, other.ID, 1)
	f.link(t, y, s.ID, 0)
	if err := f.store.InsertVersion(t.Context(), domain.NewVersion{SkillID: s.ID, Version: 1, Body: "b"}); err != nil {
		t.Fatal(err)
	}
	conv := f.insert(t, `INSERT INTO conventions (workspace_id, repo_id, rule, skill_id) VALUES (?, ?, 'r', ?) RETURNING id`, f.ws, f.repo, s.ID)
	review := f.insert(t, `INSERT INTO reviews (workspace_id, pr_id, kind) VALUES (?, ?, 'review') RETURNING id`, f.ws, f.pr)
	finding := f.insert(t, `INSERT INTO findings (review_id, file, start_line, end_line, severity, category, title, rationale, confidence, skill_id)
		VALUES (?, 'f', 1, 1, 'WARNING', 'bug', 't', 'r', 0.5, ?) RETURNING id`, review, s.ID)
	owned := f.insert(t, `INSERT INTO eval_cases (workspace_id, owner_kind, owner_id, name) VALUES (?, 'skill', ?, 'c') RETURNING id`, f.ws, s.ID)
	foreign := f.insert(t, `INSERT INTO eval_cases (workspace_id, owner_kind, owner_id, name) VALUES (?, 'skill', ?, 'c') RETURNING id`, f.ws, other.ID)

	if err := f.store.DeleteEvalCases(t.Context(), f.ws, s.ID); err != nil {
		t.Fatalf("DeleteEvalCases error = %v", err)
	}
	if err := f.store.Delete(t.Context(), f.ws, s.ID); err != nil {
		t.Fatalf("Delete error = %v", err)
	}

	if n := f.count(t, `SELECT count(*) FROM eval_cases WHERE id = ?`, owned); n != 0 {
		t.Errorf("owned eval cases left = %d, want 0", n)
	}
	if n := f.count(t, `SELECT count(*) FROM eval_cases WHERE id = ?`, foreign); n != 1 {
		t.Errorf("other skill's eval cases = %d, want 1", n)
	}
	if n := f.count(t, `SELECT count(*) FROM skill_versions WHERE skill_id = ?`, s.ID); n != 0 {
		t.Errorf("snapshots left = %d, want 0", n)
	}
	if n := f.count(t, `SELECT count(*) FROM agent_skills WHERE skill_id = ?`, s.ID); n != 0 {
		t.Errorf("links left = %d, want 0", n)
	}
	if n := f.count(t, `SELECT "order" FROM agent_skills WHERE agent_id = ? AND skill_id = ?`, x, other.ID); n != 1 {
		t.Errorf("order of the other link = %d, want 1 (unchanged)", n)
	}
	if n := f.count(t, `SELECT count(*) FROM conventions WHERE id = ? AND skill_id IS NULL`, conv); n != 1 {
		t.Errorf("convention with skill_id null = %d, want 1", n)
	}
	if n := f.count(t, `SELECT count(*) FROM findings WHERE id = ? AND skill_id IS NULL`, finding); n != 1 {
		t.Errorf("finding with skill_id null = %d, want 1", n)
	}
	if err := f.store.Delete(t.Context(), f.ws, s.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("Delete(again) error = %v, want ErrNotFound", err)
	}
}

func TestStore_AgentsOrderedByNameThenID(t *testing.T) {
	f := newFixture(t)
	sk := f.skill(t, "s")
	beta, alpha := f.agent(t, f.ws, "beta", false), f.agent(t, f.ws, "alpha", true)
	f.link(t, beta, sk.ID, 0)
	f.link(t, alpha, sk.ID, 1)

	got, err := f.store.Agents(t.Context(), sk.ID)
	if err != nil {
		t.Fatalf("Agents error = %v", err)
	}
	want := []domain.AgentRef{{ID: alpha, Name: "alpha", Enabled: true}, {ID: beta, Name: "beta", Enabled: false}}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Agents mismatch (-want +got):\n%s", diff)
	}
}

func TestStore_FindRepoAndAgents(t *testing.T) {
	f := newFixture(t)
	a := f.agent(t, f.ws, "a", true)
	foreign := f.agent(t, f.other, "z", true)

	repo, err := f.store.FindRepo(t.Context(), f.ws, f.repo)
	if err != nil || repo.FullName != "o/r" || repo.ID != f.repo {
		t.Errorf("FindRepo = %+v, %v, want o/r", repo, err)
	}
	if _, err := f.store.FindRepo(t.Context(), f.other, f.repo); !errors.Is(err, domain.ErrRepoNotFound) {
		t.Errorf("FindRepo(other workspace) error = %v, want ErrRepoNotFound", err)
	}

	got, err := f.store.FindAgents(t.Context(), f.ws, []string{a, foreign, "00000000-0000-4000-8000-000000000000"})
	if err != nil {
		t.Fatalf("FindAgents error = %v", err)
	}
	if diff := cmp.Diff([]domain.AgentRef{{ID: a, Name: "a", Enabled: true}}, got); diff != "" {
		t.Errorf("FindAgents mismatch (-want +got):\n%s", diff)
	}
	if got, err := f.store.FindAgents(t.Context(), f.ws, nil); err != nil || len(got) != 0 {
		t.Errorf("FindAgents(none) = %v, %v, want empty", got, err)
	}
}

func TestStore_TransactionRollsBackInsert(t *testing.T) {
	f := newFixture(t)
	boom := errors.New("boom")
	err := pg.NewTx(f.db).InTx(t.Context(), func(ctx context.Context) error {
		if _, err := f.store.Insert(ctx, domain.NewSkill{WorkspaceID: f.ws, Name: "gone", Type: domain.TypeCustom, Source: domain.SourceManual, Body: "b"}); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("InTx error = %v, want boom", err)
	}
	if got, _ := f.store.List(t.Context(), f.ws); len(got) != 0 {
		t.Errorf("List after rollback = %d skills, want 0", len(got))
	}
}

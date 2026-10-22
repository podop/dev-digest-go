package postgres_test

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/google/go-cmp/cmp"
	"gorm.io/gorm"

	pg "dev-digest-go/backend/internal/platform/postgres"
	"dev-digest-go/backend/internal/platform/postgres/pgtest"
	pcpg "dev-digest-go/backend/internal/projectcontext/adapters/postgres"
	"dev-digest-go/backend/internal/projectcontext/app"
)

func TestMain(m *testing.M) { os.Exit(pgtest.Main(m)) }

const none = "00000000-0000-4000-8000-000000000000"

type fixture struct {
	db    *gorm.DB
	store *pcpg.Store
	ws    string
	repo  string
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	d := pgtest.Migrated(t)
	f := fixture{db: d.DB, store: pcpg.NewStore(d.DB)}
	scan := func(dst *string, sql string, args ...any) {
		t.Helper()
		if err := d.DB.Raw(sql, args...).Scan(dst).Error; err != nil {
			t.Fatal(err)
		}
	}
	scan(&f.ws, `INSERT INTO workspaces (name) VALUES ('default') RETURNING id`)
	scan(&f.repo, `INSERT INTO repos (workspace_id, owner, name, full_name) VALUES (?, 'o', 'r', 'o/r') RETURNING id`, f.ws)
	return f
}

func (f fixture) agent(t *testing.T, name string) string {
	t.Helper()
	var id string
	err := f.db.Raw(`INSERT INTO agents (workspace_id, name, provider, model, system_prompt) VALUES (?, ?, 'openai', 'm', 'p') RETURNING id`,
		f.ws, name).Scan(&id).Error
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func (f fixture) skill(t *testing.T, name string) string {
	t.Helper()
	var id string
	err := f.db.Raw(`INSERT INTO skills (workspace_id, name, description, type, source, body) VALUES (?, ?, 'd', 'custom', 'manual', 'b') RETURNING id`,
		f.ws, name).Scan(&id).Error
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestFiles(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()

	a, err := f.store.InsertFile(ctx, f.repo, "b.md", "héllo", 6)
	if err != nil || a.Version != 1 || a.Content != "héllo" || a.UpdatedAt.IsZero() {
		t.Fatalf("InsertFile() = %+v, %v, want version 1", a, err)
	}
	if _, err := f.store.InsertFile(ctx, f.repo, "b.md", "", 0); !errors.Is(err, app.ErrPathExists) {
		t.Errorf("InsertFile(duplicate) error = %v, want ErrPathExists", err)
	}
	if _, err := f.store.InsertFile(ctx, none, "x.md", "", 0); !errors.Is(err, app.ErrRepoGone) {
		t.Errorf("InsertFile(unknown repo) error = %v, want ErrRepoGone", err)
	}
	if _, err := f.store.InsertFile(ctx, f.repo, "A.md", "", 0); err != nil {
		t.Fatal(err)
	}

	list, err := f.store.ListFiles(ctx, f.repo)
	if err != nil || len(list) != 2 || list[0].Path != "A.md" || list[1].Path != "b.md" || list[1].Chars != 5 {
		t.Errorf("ListFiles() = %+v, %v, want byte order (A before b) and 5 characters", list, err)
	}

	saved, ok, err := f.store.SaveFile(ctx, f.repo, "b.md", "new", 3, 1)
	if err != nil || !ok || saved.Version != 2 || saved.Content != "new" || saved.SizeBytes != 3 || !saved.UpdatedAt.After(a.UpdatedAt) {
		t.Errorf("SaveFile(base 1) = %+v, %v, %v, want version 2", saved, ok, err)
	}
	if _, ok, err := f.store.SaveFile(ctx, f.repo, "b.md", "stale", 5, 1); ok || err != nil {
		t.Errorf("SaveFile(stale base) = ok %v, err %v, want not ok", ok, err)
	}
	if got, _, _ := f.store.FindFile(ctx, f.repo, "b.md"); got.Content != "new" {
		t.Errorf("content after a stale save = %q, want it unchanged", got.Content)
	}

	if _, ok, err := f.store.RenameFile(ctx, f.repo, "b.md", "A.md", 2); !errors.Is(err, app.ErrPathExists) || ok {
		t.Errorf("RenameFile(onto an existing path) = ok %v, err %v, want ErrPathExists", ok, err)
	}
	moved, ok, err := f.store.RenameFile(ctx, f.repo, "b.md", "c.md", 2)
	if err != nil || !ok || moved.Path != "c.md" || moved.Version != 3 || moved.Content != "new" {
		t.Errorf("RenameFile() = %+v, %v, %v, want c.md at version 3", moved, ok, err)
	}
	if _, ok, _ := f.store.FindFile(ctx, f.repo, "b.md"); ok {
		t.Error("FindFile(old path) found the renamed file")
	}

	if deleted, err := f.store.DeleteFile(ctx, f.repo, "c.md"); err != nil || !deleted {
		t.Errorf("DeleteFile() = %v, %v, want true", deleted, err)
	}
	if deleted, err := f.store.DeleteFile(ctx, f.repo, "c.md"); err != nil || deleted {
		t.Errorf("DeleteFile(again) = %v, %v, want false", deleted, err)
	}
}

func TestLookups(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	agent, skill := f.agent(t, "a"), f.skill(t, "s")

	if _, ok, err := f.store.FindRepo(ctx, f.ws, f.repo); err != nil || !ok {
		t.Errorf("FindRepo() = %v, %v, want found", ok, err)
	}
	if _, ok, _ := f.store.FindRepo(ctx, none, f.repo); ok {
		t.Error("FindRepo(other workspace) found the repo")
	}
	if ok, _ := f.store.LockRepo(ctx, f.ws, none); ok {
		t.Error("LockRepo(unknown) = true")
	}
	for name, c := range map[string]struct {
		exists func(ctx context.Context, ws, id string) (bool, error)
		id     string
	}{"agent": {f.store.AgentExists, agent}, "skill": {f.store.SkillExists, skill}} {
		if ok, err := c.exists(ctx, f.ws, c.id); err != nil || !ok {
			t.Errorf("%s exists = %v, %v, want true", name, ok, err)
		}
		if ok, _ := c.exists(ctx, none, c.id); ok {
			t.Errorf("%s exists in another workspace", name)
		}
		if ok, _ := c.exists(ctx, f.ws, none); ok {
			t.Errorf("unknown %s exists", name)
		}
	}
}

func TestReplacePaths(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	agent, skill := f.agent(t, "a"), f.skill(t, "s")

	if err := f.store.ReplaceAgentPaths(ctx, agent, f.repo, []string{"b.md", "a.md"}); err != nil {
		t.Fatal(err)
	}
	if err := f.store.ReplaceAgentPaths(ctx, agent, f.repo, []string{"c.md", "b.md"}); err != nil {
		t.Fatal(err)
	}
	if got, _ := f.store.AgentPaths(ctx, agent, f.repo); !cmp.Equal(got, []string{"c.md", "b.md"}) {
		t.Errorf("AgentPaths() = %v, want [c.md b.md]", got)
	}
	if err := f.store.ReplaceSkillPaths(ctx, skill, f.repo, []string{"z.md", "y.md"}); err != nil {
		t.Fatal(err)
	}
	if got, _ := f.store.SkillPaths(ctx, skill, f.repo); !cmp.Equal(got, []string{"z.md", "y.md"}) {
		t.Errorf("SkillPaths() = %v, want [z.md y.md]", got)
	}
	if err := f.store.ReplaceAgentPaths(ctx, agent, f.repo, nil); err != nil {
		t.Fatal(err)
	}
	if got, _ := f.store.AgentPaths(ctx, agent, f.repo); len(got) != 0 {
		t.Errorf("AgentPaths() after clearing = %v", got)
	}

	if err := f.store.ReplaceAgentPaths(ctx, agent, none, []string{"a.md"}); !errors.Is(err, app.ErrOwnerGone) {
		t.Errorf("ReplaceAgentPaths(unknown repo) error = %v, want ErrOwnerGone", err)
	}
	if err := f.store.ReplaceSkillPaths(ctx, none, f.repo, []string{"a.md"}); !errors.Is(err, app.ErrOwnerGone) {
		t.Errorf("ReplaceSkillPaths(unknown skill) error = %v, want ErrOwnerGone", err)
	}
}

func TestListUsage(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	x, y := f.agent(t, "X"), f.agent(t, "Y")
	s := f.skill(t, "S")
	f.db.Exec(`INSERT INTO agent_skills (agent_id, skill_id) VALUES (?, ?)`, y, s)
	_ = f.store.ReplaceAgentPaths(ctx, x, f.repo, []string{"a.md"})
	_ = f.store.ReplaceSkillPaths(ctx, s, f.repo, []string{"a.md"})

	got, err := f.store.ListUsage(ctx, f.repo)
	if err != nil {
		t.Fatal(err)
	}
	want := []app.UsageRow{
		{Path: "a.md", AgentID: x, AgentName: "X", Via: app.ViaDirect},
		{Path: "a.md", AgentID: y, AgentName: "Y", Via: app.ViaSkill, SkillName: "S"},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("ListUsage() mismatch (-want +got):\n%s", diff)
	}
}

func TestMoveAttachments(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	x, y := f.agent(t, "X"), f.agent(t, "Y")
	s := f.skill(t, "S")
	for agent, paths := range map[string][]string{x: {"first.md", "old.md"}, y: {"old.md", "new.md"}} {
		if err := f.store.ReplaceAgentPaths(ctx, agent, f.repo, paths); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.store.ReplaceSkillPaths(ctx, s, f.repo, []string{"old.md"}); err != nil {
		t.Fatal(err)
	}

	if err := f.store.MoveAttachments(ctx, f.repo, "old.md", "new.md"); err != nil {
		t.Fatal(err)
	}
	if got, _ := f.store.AgentPaths(ctx, x, f.repo); !cmp.Equal(got, []string{"first.md", "new.md"}) {
		t.Errorf("X paths = %v, want [first.md new.md]", got)
	}
	if got, _ := f.store.AgentPaths(ctx, y, f.repo); !cmp.Equal(got, []string{"new.md"}) {
		t.Errorf("Y paths = %v, want one new.md at the old position", got)
	}
	if got, _ := f.store.SkillPaths(ctx, s, f.repo); !cmp.Equal(got, []string{"new.md"}) {
		t.Errorf("skill paths = %v, want [new.md]", got)
	}
}

func TestStoreJoinsTransaction(t *testing.T) {
	f := newFixture(t)
	boom := errors.New("boom")
	err := pg.NewTx(f.db).InTx(t.Context(), func(ctx context.Context) error {
		if _, err := f.store.InsertFile(ctx, f.repo, "tx.md", "", 0); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("InTx() error = %v, want boom", err)
	}
	if _, ok, _ := f.store.FindFile(t.Context(), f.repo, "tx.md"); ok {
		t.Error("the insert survived a rolled-back transaction")
	}
}

package postgres_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"testing"

	"github.com/google/go-cmp/cmp"
	"gorm.io/gorm"

	"dev-digest-go/backend/internal/agents/adapters/postgres"
	"dev-digest-go/backend/internal/agents/app"
	"dev-digest-go/backend/internal/agents/domain"
	"dev-digest-go/backend/internal/llm"
	"dev-digest-go/backend/internal/platform/apperr"
	pg "dev-digest-go/backend/internal/platform/postgres"
	"dev-digest-go/backend/internal/platform/postgres/pgtest"
)

func TestMain(m *testing.M) { os.Exit(pgtest.Main(m)) }

type noModels struct{}

func (noModels) ListModels(context.Context, llm.Provider) ([]llm.ModelInfo, error) { return nil, nil }

// fixture is a migrated database with two workspaces, a user and the agents service on it.
type fixture struct {
	db    *gorm.DB
	store *postgres.Store
	tx    *pg.Tx
	svc   *app.Service
	ws    string
	other string
	user  string
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	d := pgtest.Migrated(t)
	f := fixture{db: d.DB, store: postgres.NewStore(d.DB), tx: pg.NewTx(d.DB)}
	f.svc = app.NewService(f.store, f.tx, noModels{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	f.ws = f.insert(t, `INSERT INTO workspaces (name) VALUES ('default') RETURNING id`)
	f.other = f.insert(t, `INSERT INTO workspaces (name) VALUES ('other') RETURNING id`)
	f.user = f.insert(t, `INSERT INTO users (email, name) VALUES ('you@local', 'You') RETURNING id`)
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

func (f fixture) skill(t *testing.T, ws, name string) string {
	t.Helper()
	return f.insert(t, `INSERT INTO skills (workspace_id, name, description, type, source, body)
		VALUES (?, ?, 'd', 'custom', 'manual', 'b') RETURNING id`, ws, name)
}

func (f fixture) count(t *testing.T, query string, args ...any) int {
	t.Helper()
	var n int
	if err := f.db.Raw(query, args...).Scan(&n).Error; err != nil {
		t.Fatalf("count %q: %v", query, err)
	}
	return n
}

func (f fixture) create(t *testing.T, name string) domain.Agent {
	t.Helper()
	a, err := f.svc.Create(t.Context(), f.ws, f.user, domain.CreateInput{Name: name, Provider: "openai", Model: "gpt-4.1", SystemPrompt: "p"})
	if err != nil {
		t.Fatalf("Create(%s) error = %v", name, err)
	}
	return a
}

func ptr[T any](v T) *T { return &v }

func TestStore_CreateDefaultsAndVersionOne(t *testing.T) {
	f := newFixture(t)
	a := f.create(t, "A")
	want := domain.Agent{
		ID: a.ID, Name: "A", Provider: "openai", Model: "gpt-4.1", SystemPrompt: "p", Enabled: true,
		Version: 1, Strategy: "single-pass", CIFailOn: "critical", RepoIntel: true,
	}
	if diff := cmp.Diff(want, a); diff != "" {
		t.Errorf("Create() mismatch (-want +got):\n%s", diff)
	}
	got, err := f.svc.Get(t.Context(), f.ws, a.ID)
	if err != nil || got.ID != a.ID || got.OutputSchema != nil {
		t.Errorf("Get() = %+v, %v, want the stored agent with a null schema", got, err)
	}
	if n := f.count(t, `SELECT count(*) FROM agents WHERE id = ? AND created_by = ?`, a.ID, f.user); n != 1 {
		t.Errorf("agents with created_by = system user: %d, want 1", n)
	}

	vs, err := f.svc.Versions(t.Context(), f.ws, a.ID)
	if err != nil || len(vs) != 1 {
		t.Fatalf("Versions() = %v, %v, want one version", vs, err)
	}
	wantCfg := domain.VersionConfig{Provider: "openai", Model: "gpt-4.1", SystemPrompt: "p", Strategy: "single-pass", CIFailOn: "critical", RepoIntel: true, Skills: []string{}}
	if diff := cmp.Diff(wantCfg, vs[0].Config); diff != "" {
		t.Errorf("version 1 config mismatch (-want +got):\n%s", diff)
	}
	var raw string
	if err := f.db.Raw(`SELECT config_json::text FROM agent_versions WHERE agent_id = ?`, a.ID).Scan(&raw).Error; err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		t.Fatal(err)
	}
	if v, ok := doc["output_schema"]; !ok || v != nil {
		t.Errorf("config_json output_schema = %v (present %v), want an explicit null", v, ok)
	}
	if diff := cmp.Diff([]any{}, doc["skills"]); diff != "" {
		t.Errorf("config_json skills mismatch (-want +got):\n%s", diff)
	}
}

func TestStore_ListOrder(t *testing.T) {
	f := newFixture(t)
	// One transaction: every row gets the same created_at, so name (byte order) decides.
	err := f.tx.InTx(t.Context(), func(ctx context.Context) error {
		for _, n := range []string{"b", "a", "B", "Zed"} {
			if _, err := f.svc.Create(ctx, f.ws, f.user, domain.CreateInput{Name: n, Provider: "openai", Model: "m", SystemPrompt: "p"}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	later := f.create(t, "Aaa")
	if _, err := f.svc.Create(t.Context(), f.other, f.user, domain.CreateInput{Name: "foreign", Provider: "openai", Model: "m", SystemPrompt: "p"}); err != nil {
		t.Fatal(err)
	}

	agents, err := f.svc.List(t.Context(), f.ws)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, a := range agents {
		names = append(names, a.Name)
	}
	if diff := cmp.Diff([]string{"B", "Zed", "a", "b", later.Name}, names); diff != "" {
		t.Errorf("List() names mismatch (-want +got):\n%s", diff)
	}
}

func TestStore_OtherWorkspaceIsNotFound(t *testing.T) {
	f := newFixture(t)
	a := f.create(t, "A")
	_, err := f.store.Get(t.Context(), f.other, a.ID)
	if !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("Get(other workspace) error = %v, want ErrNotFound", err)
	}
	if err := f.store.Delete(t.Context(), f.other, a.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("Delete(other workspace) error = %v, want ErrNotFound", err)
	}
	if err := f.store.Save(t.Context(), f.other, a); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("Save(other workspace) error = %v, want ErrNotFound", err)
	}
}

func TestService_UpdateVersioning(t *testing.T) {
	f := newFixture(t)
	a := f.create(t, "A")
	ctx := t.Context()

	steps := []struct {
		name  string
		patch domain.Patch
		want  int
	}{
		{"model", domain.Patch{Model: ptr("gpt-5.4")}, 2},
		{"enabled false", domain.Patch{Enabled: ptr(false)}, 2},
		{"empty", domain.Patch{}, 2},
		{"same model", domain.Patch{Model: ptr("gpt-5.4")}, 2},
		{"schema null", domain.Patch{SetOutputSchema: true}, 3},
		{"schema value", domain.Patch{SetOutputSchema: true, OutputSchema: json.RawMessage(`{"type":"object","n":1.50}`)}, 4},
	}
	for _, st := range steps {
		got, err := f.svc.Update(ctx, f.ws, a.ID, st.patch)
		if err != nil {
			t.Fatalf("Update(%s) error = %v", st.name, err)
		}
		if got.Version != st.want {
			t.Errorf("Update(%s).Version = %d, want %d", st.name, got.Version, st.want)
		}
	}
	stored, err := f.svc.Get(ctx, f.ws, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Enabled || stored.Model != "gpt-5.4" || stored.Version != 4 {
		t.Errorf("stored agent = %+v, want disabled, gpt-5.4, v4", stored)
	}
	var schema map[string]any
	if err := json.Unmarshal(stored.OutputSchema, &schema); err != nil || schema["type"] != "object" {
		t.Errorf("stored output_schema = %s (%v), want the object", stored.OutputSchema, err)
	}
	vs, _ := f.svc.Versions(ctx, f.ws, a.ID)
	var nums []int
	for _, v := range vs {
		nums = append(nums, v.Version)
	}
	if diff := cmp.Diff([]int{4, 3, 2, 1}, nums); diff != "" {
		t.Errorf("Versions() mismatch (-want +got):\n%s", diff)
	}
	if vs[2].Config.Model != "gpt-5.4" || vs[3].Config.Model != "gpt-4.1" {
		t.Errorf("snapshots models = %q (v2), %q (v1), want gpt-5.4 and gpt-4.1", vs[2].Config.Model, vs[3].Config.Model)
	}

	// Clearing the schema writes SQL NULL back.
	cleared, err := f.svc.Update(ctx, f.ws, a.ID, domain.Patch{SetOutputSchema: true, OutputSchema: json.RawMessage(`null`)})
	if err != nil || cleared.OutputSchema != nil {
		t.Errorf("Update(schema null) = %+v, %v, want a nil schema", cleared, err)
	}
	if n := f.count(t, `SELECT count(*) FROM agents WHERE id = ? AND output_schema IS NULL`, a.ID); n != 1 {
		t.Errorf("agents with SQL NULL output_schema = %d, want 1", n)
	}
}

func TestService_Version(t *testing.T) {
	f := newFixture(t)
	a := f.create(t, "A")
	if v, err := f.svc.Version(t.Context(), f.ws, a.ID, 1); err != nil || v.Version != 1 || v.CreatedAt.IsZero() {
		t.Errorf("Version(1) = %+v, %v, want version 1 with a creation time", v, err)
	}
	for _, id := range []string{a.ID, "00000000-0000-4000-8000-000000000000"} {
		_, err := f.svc.Version(t.Context(), f.ws, id, 2)
		if ae, ok := apperr.As(err); !ok || ae.Kind != apperr.KindNotFound || ae.Message != "Agent version not found" {
			t.Errorf("Version(%s, 2) error = %v, want 404 Agent version not found", id, err)
		}
	}
}

func TestService_SkillLinks(t *testing.T) {
	f := newFixture(t)
	a := f.create(t, "X")
	sa, sb, sc := f.skill(t, f.ws, "A"), f.skill(t, f.ws, "B"), f.skill(t, f.ws, "C")
	foreign := f.skill(t, f.other, "S")
	ctx := t.Context()
	ids := func(links []domain.SkillLink) []string {
		out := []string{}
		for i, l := range links {
			if l.Order != i || l.AgentID != a.ID {
				t.Errorf("link %d = %+v, want order %d of %s", i, l, i, a.ID)
			}
			out = append(out, l.SkillID)
		}
		return out
	}
	version := func() int { g, _ := f.svc.Get(ctx, f.ws, a.ID); return g.Version }

	if _, err := f.svc.SetSkills(ctx, f.ws, a.ID, []string{sa, sb}); err != nil {
		t.Fatal(err)
	}
	if v := version(); v != 2 {
		t.Fatalf("version after first set = %d, want 2", v)
	}
	got, err := f.svc.SetSkills(ctx, f.ws, a.ID, []string{sb, sa})
	if err != nil || version() != 3 {
		t.Fatalf("SetSkills([B A]) = %v, version %d", err, version())
	}
	if diff := cmp.Diff([]string{sb, sa}, ids(got)); diff != "" {
		t.Errorf("SetSkills([B A]) mismatch (-want +got):\n%s", diff)
	}
	if _, err := f.svc.SetSkills(ctx, f.ws, a.ID, []string{sb, sa}); err != nil || version() != 3 {
		t.Errorf("SetSkills(same) = %v, version %d, want ok and 3", err, version())
	}
	got, err = f.svc.LinkSkill(ctx, f.ws, a.ID, sc, ptr(0))
	if err != nil {
		t.Fatalf("LinkSkill(C, 0) error = %v", err)
	}
	if diff := cmp.Diff([]string{sc, sb, sa}, ids(got)); diff != "" {
		t.Errorf("LinkSkill(C, 0) mismatch (-want +got):\n%s", diff)
	}
	got, _ = f.svc.LinkSkill(ctx, f.ws, a.ID, sa, nil)
	if diff := cmp.Diff([]string{sc, sb, sa}, ids(got)); diff != "" || version() != 4 {
		t.Errorf("LinkSkill(A) mismatch (-want +got):\n%s\nversion %d, want 4", diff, version())
	}

	// A snapshot carries the skills at that moment.
	v4, _ := f.svc.Version(ctx, f.ws, a.ID, 4)
	if diff := cmp.Diff([]string{sc, sb, sa}, v4.Config.Skills); diff != "" {
		t.Errorf("version 4 skills mismatch (-want +got):\n%s", diff)
	}

	// Rejected requests change nothing.
	random := "00000000-0000-4000-8000-000000000001"
	_, err = f.svc.SetSkills(ctx, f.ws, a.ID, []string{sa, foreign, random})
	ae, ok := apperr.As(err)
	if !ok || ae.Code != "unknown_skill" || ae.Message != "Unknown skill id(s) for this workspace: "+foreign+", "+random {
		t.Fatalf("SetSkills(foreign, random) error = %v, want unknown_skill", err)
	}
	if links, _ := f.svc.SkillLinks(ctx, f.ws, a.ID); len(links) != 3 || version() != 4 {
		t.Errorf("after rejected request: %d links, version %d, want 3 and 4", len(links), version())
	}

	cleared, err := f.svc.SetSkills(ctx, f.ws, a.ID, []string{})
	if err != nil || len(cleared) != 0 || version() != 5 {
		t.Errorf("SetSkills([]) = %v, %v, version %d, want empty and 5", cleared, err, version())
	}
}

func TestService_AppendSkillJoinsOuterTransaction(t *testing.T) {
	f := newFixture(t)
	a := f.create(t, "X")
	sa := f.skill(t, f.ws, "A")
	errAbort := errors.New("abort")

	err := f.tx.InTx(t.Context(), func(ctx context.Context) error {
		if err := f.svc.AppendSkill(ctx, f.ws, a.ID, sa); err != nil {
			return err
		}
		return errAbort
	})
	if !errors.Is(err, errAbort) {
		t.Fatalf("InTx error = %v, want errAbort", err)
	}
	if n := f.count(t, `SELECT count(*) FROM agent_skills WHERE agent_id = ?`, a.ID); n != 0 {
		t.Errorf("links after rolled-back outer tx = %d, want 0", n)
	}
	if err := f.svc.AppendSkill(t.Context(), f.ws, a.ID, sa); err != nil {
		t.Fatal(err)
	}
	if n := f.count(t, `SELECT count(*) FROM agent_skills WHERE agent_id = ?`, a.ID); n != 1 {
		t.Errorf("links after AppendSkill = %d, want 1", n)
	}
}

func TestService_SnapshotFailureRollsBackLinks(t *testing.T) {
	f := newFixture(t)
	a := f.create(t, "X")
	sa := f.skill(t, f.ws, "A")
	// Without its table the snapshot fails after the links were replaced and the version raised.
	if err := f.db.Exec(`ALTER TABLE agent_versions RENAME TO agent_versions_gone`).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.SetSkills(t.Context(), f.ws, a.ID, []string{sa}); err == nil {
		t.Fatal("SetSkills() error = nil, want the snapshot failure")
	}
	if n := f.count(t, `SELECT count(*) FROM agent_skills WHERE agent_id = ?`, a.ID); n != 0 {
		t.Errorf("links after failed SetSkills = %d, want 0 (rolled back)", n)
	}
	if v := f.count(t, `SELECT version FROM agents WHERE id = ?`, a.ID); v != 1 {
		t.Errorf("version after failed SetSkills = %d, want 1", v)
	}
}

func TestService_DeleteCascades(t *testing.T) {
	f := newFixture(t)
	a := f.create(t, "X")
	keep := f.create(t, "Keep")
	sa, sb := f.skill(t, f.ws, "A"), f.skill(t, f.ws, "B")
	if _, err := f.svc.SetSkills(t.Context(), f.ws, a.ID, []string{sa, sb}); err != nil {
		t.Fatal(err)
	}
	repo := f.insert(t, `INSERT INTO repos (workspace_id, owner, name, full_name) VALUES (?, 'o', 'r', 'o/r') RETURNING id`, f.ws)
	pr := f.insert(t, `INSERT INTO pull_requests (workspace_id, repo_id, number, title, author, branch, base, head_sha)
		VALUES (?, ?, 1, 't', 'a', 'b', 'main', 'sha') RETURNING id`, f.ws, repo)
	f.insert(t, `INSERT INTO agent_context_docs (agent_id, repo_id, path, position) VALUES (?, ?, 'a.md', 0) RETURNING agent_id`, a.ID, repo)
	f.insert(t, `INSERT INTO eval_cases (workspace_id, owner_kind, owner_id, name) VALUES (?, 'agent', ?, 'c') RETURNING id`, f.ws, a.ID)
	f.insert(t, `INSERT INTO eval_cases (workspace_id, owner_kind, owner_id, name) VALUES (?, 'agent', ?, 'c') RETURNING id`, f.ws, keep.ID)
	f.insert(t, `INSERT INTO eval_cases (workspace_id, owner_kind, owner_id, name) VALUES (?, 'skill', ?, 'c') RETURNING id`, f.ws, a.ID)
	run := f.insert(t, `INSERT INTO agent_runs (workspace_id, agent_id) VALUES (?, ?) RETURNING id`, f.ws, a.ID)
	review := f.insert(t, `INSERT INTO reviews (workspace_id, pr_id, kind, agent_id) VALUES (?, ?, 'review', ?) RETURNING id`, f.ws, pr, a.ID)

	if err := f.svc.Delete(t.Context(), f.ws, a.ID); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}

	checks := []struct {
		what  string
		query string
		args  []any
		want  int
	}{
		{"agent", `SELECT count(*) FROM agents WHERE id = ?`, []any{a.ID}, 0},
		{"versions", `SELECT count(*) FROM agent_versions WHERE agent_id = ?`, []any{a.ID}, 0},
		{"links", `SELECT count(*) FROM agent_skills WHERE agent_id = ?`, []any{a.ID}, 0},
		{"context docs", `SELECT count(*) FROM agent_context_docs WHERE agent_id = ?`, []any{a.ID}, 0},
		{"its agent eval cases", `SELECT count(*) FROM eval_cases WHERE owner_kind = 'agent' AND owner_id = ?`, []any{a.ID}, 0},
		{"other agent eval cases", `SELECT count(*) FROM eval_cases WHERE owner_kind = 'agent' AND owner_id = ?`, []any{keep.ID}, 1},
		{"skill-owned eval cases", `SELECT count(*) FROM eval_cases WHERE owner_kind = 'skill'`, nil, 1},
		{"skills", `SELECT count(*) FROM skills WHERE id IN (?, ?)`, []any{sa, sb}, 2},
		{"run with null agent", `SELECT count(*) FROM agent_runs WHERE id = ? AND agent_id IS NULL`, []any{run}, 1},
		{"review with null agent", `SELECT count(*) FROM reviews WHERE id = ? AND agent_id IS NULL`, []any{review}, 1},
	}
	for _, c := range checks {
		if n := f.count(t, c.query, c.args...); n != c.want {
			t.Errorf("%s after Delete = %d, want %d", c.what, n, c.want)
		}
	}
	err := f.svc.Delete(t.Context(), f.ws, a.ID)
	if ae, ok := apperr.As(err); !ok || ae.Kind != apperr.KindNotFound {
		t.Errorf("Delete(again) error = %v, want not found", err)
	}
}

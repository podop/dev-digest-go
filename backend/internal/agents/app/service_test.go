package app_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/agents/app"
	"dev-digest-go/backend/internal/agents/domain"
	"dev-digest-go/backend/internal/llm"
	"dev-digest-go/backend/internal/platform/apperr"
)

const ws = "ws-1"

func ptr[T any](v T) *T { return &v }

type env struct {
	svc    *app.Service
	store  *fakeStore
	models *fakeModels
	logs   *bytes.Buffer
}

func newEnv() env {
	store := newFakeStore()
	models := &fakeModels{models: map[llm.Provider][]llm.ModelInfo{}}
	logs := &bytes.Buffer{}
	log := slog.New(slog.NewTextHandler(logs, nil))
	return env{svc: app.NewService(store, &fakeTx{store: store}, models, log), store: store, models: models, logs: logs}
}

func (e env) createAgent(t *testing.T) domain.Agent {
	t.Helper()
	a, err := e.svc.Create(t.Context(), ws, "user-1", domain.CreateInput{Name: "A", Provider: "openai", Model: "gpt-4.1", SystemPrompt: "p"})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	return a
}

func (e env) skills(ids ...string) {
	for _, id := range ids {
		e.store.skills[id] = ws
	}
}

func versionNumbers(vs []domain.Version) []int {
	out := make([]int, len(vs))
	for i, v := range vs {
		out[i] = v.Version
	}
	return out
}

func linkIDs(links []domain.SkillLink) []string {
	out := make([]string, len(links))
	for i, l := range links {
		out[i] = l.SkillID
	}
	return out
}

func wantApperr(t *testing.T, err error, kind apperr.Kind, code, msg string) *apperr.Error {
	t.Helper()
	ae, ok := apperr.As(err)
	if !ok {
		t.Fatalf("error = %v, want an apperr", err)
	}
	if ae.Kind != kind || ae.Code != code || ae.Message != msg {
		t.Fatalf("error = {%v %q %q}, want {%v %q %q}", ae.Kind, ae.Code, ae.Message, kind, code, msg)
	}
	return ae
}

func TestService_CreateWritesVersionOne(t *testing.T) {
	e := newEnv()
	a := e.createAgent(t)

	if a.Version != 1 || a.ID == "" {
		t.Errorf("Create() = %+v, want version 1 and an id", a)
	}
	if got := e.store.createdBy[a.ID]; got != "user-1" {
		t.Errorf("Create() created_by = %q, want user-1", got)
	}
	vs, err := e.svc.Versions(t.Context(), ws, a.ID)
	if err != nil {
		t.Fatalf("Versions() error = %v", err)
	}
	if diff := cmp.Diff([]int{1}, versionNumbers(vs)); diff != "" {
		t.Errorf("Versions() mismatch (-want +got):\n%s", diff)
	}
	if vs[0].Config.Skills == nil || len(vs[0].Config.Skills) != 0 {
		t.Errorf("version 1 skills = %#v, want an empty list", vs[0].Config.Skills)
	}

	// Names are not unique.
	if _, err := e.svc.Create(t.Context(), ws, "user-1", domain.CreateInput{Name: "A", Provider: "openai", Model: "m", SystemPrompt: "p"}); err != nil {
		t.Errorf("Create(same name) error = %v, want nil", err)
	}
}

func TestService_CreateRollsBackWhenSnapshotFails(t *testing.T) {
	e := newEnv()
	e.store.failAddVersion = errBoom
	_, err := e.svc.Create(t.Context(), ws, "u", domain.CreateInput{Name: "A", Provider: "openai", Model: "m", SystemPrompt: "p"})
	if !errors.Is(err, errBoom) {
		t.Fatalf("Create() error = %v, want errBoom", err)
	}
	if n := len(e.store.agents); n != 0 {
		t.Errorf("agents after failed Create = %d, want 0 (rolled back)", n)
	}
}

func TestService_UpdateVersioning(t *testing.T) {
	e := newEnv()
	a := e.createAgent(t)
	ctx := t.Context()

	steps := []struct {
		name        string
		patch       domain.Patch
		wantVersion int
	}{
		{"new model", domain.Patch{Model: ptr("gpt-5.4")}, 2},
		{"enabled only", domain.Patch{Enabled: ptr(false)}, 2},
		{"empty", domain.Patch{}, 2},
		{"same model", domain.Patch{Model: ptr("gpt-5.4")}, 2},
		{"output_schema null", domain.Patch{SetOutputSchema: true}, 3},
	}
	for _, st := range steps {
		got, err := e.svc.Update(ctx, ws, a.ID, st.patch)
		if err != nil {
			t.Fatalf("Update(%s) error = %v", st.name, err)
		}
		if got.Version != st.wantVersion {
			t.Errorf("Update(%s).Version = %d, want %d", st.name, got.Version, st.wantVersion)
		}
	}
	vs, _ := e.svc.Versions(ctx, ws, a.ID)
	if diff := cmp.Diff([]int{3, 2, 1}, versionNumbers(vs)); diff != "" {
		t.Errorf("Versions() mismatch (-want +got):\n%s", diff)
	}
	if vs[1].Config.Model != "gpt-5.4" {
		t.Errorf("version 2 model = %q, want gpt-5.4", vs[1].Config.Model)
	}
	if stored, _ := e.svc.Get(ctx, ws, a.ID); stored.Enabled {
		t.Error("agent still enabled after the enabled=false patch")
	}
	if len(e.store.locked) == 0 {
		t.Error("Update did not lock the agent row")
	}
}

func TestService_UpdateSnapshotHasCurrentSkills(t *testing.T) {
	e := newEnv()
	e.skills("s1", "s2")
	a := e.createAgent(t)
	if _, err := e.svc.SetSkills(t.Context(), ws, a.ID, []string{"s2", "s1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Update(t.Context(), ws, a.ID, domain.Patch{Name: ptr("B")}); err != nil {
		t.Fatal(err)
	}
	v, err := e.svc.Version(t.Context(), ws, a.ID, 3)
	if err != nil {
		t.Fatalf("Version(3) error = %v", err)
	}
	if diff := cmp.Diff([]string{"s2", "s1"}, v.Config.Skills); diff != "" {
		t.Errorf("snapshot skills mismatch (-want +got):\n%s", diff)
	}
}

func TestService_NotFound(t *testing.T) {
	e := newEnv()
	ctx := t.Context()
	other := e.createAgent(t)
	e.store.owner[other.ID] = "ws-other"

	for _, id := range []string{"missing", other.ID} {
		_, err := e.svc.Get(ctx, ws, id)
		wantApperr(t, err, apperr.KindNotFound, "not_found", "Agent not found")
		_, err = e.svc.Update(ctx, ws, id, domain.Patch{})
		wantApperr(t, err, apperr.KindNotFound, "not_found", "Agent not found")
		wantApperr(t, e.svc.Delete(ctx, ws, id), apperr.KindNotFound, "not_found", "Agent not found")
		_, err = e.svc.Versions(ctx, ws, id)
		wantApperr(t, err, apperr.KindNotFound, "not_found", "Agent not found")
		_, err = e.svc.SkillLinks(ctx, ws, id)
		wantApperr(t, err, apperr.KindNotFound, "not_found", "Agent not found")
		_, err = e.svc.SetSkills(ctx, ws, id, nil)
		wantApperr(t, err, apperr.KindNotFound, "not_found", "Agent not found")
		_, err = e.svc.AgentModels(ctx, ws, id)
		wantApperr(t, err, apperr.KindNotFound, "not_found", "Agent not found")
		_, err = e.svc.Version(ctx, ws, id, 1)
		wantApperr(t, err, apperr.KindNotFound, "not_found", "Agent version not found")
	}
	if len(e.models.asked) != 0 {
		t.Errorf("ListModels called for unknown agents: %v", e.models.asked)
	}
}

func TestService_UnknownAgentBeforeSkillCheck(t *testing.T) {
	e := newEnv()
	_, err := e.svc.SetSkills(t.Context(), ws, "missing", []string{"not-a-skill"})
	wantApperr(t, err, apperr.KindNotFound, "not_found", "Agent not found")
}

func TestService_VersionNotFound(t *testing.T) {
	e := newEnv()
	a := e.createAgent(t)
	_, err := e.svc.Version(t.Context(), ws, a.ID, 7)
	wantApperr(t, err, apperr.KindNotFound, "not_found", "Agent version not found")
	if v, err := e.svc.Version(t.Context(), ws, a.ID, 1); err != nil || v.Version != 1 {
		t.Errorf("Version(1) = %+v, %v, want version 1", v, err)
	}
}

func TestService_DeleteRemovesAgentAndEvalCases(t *testing.T) {
	e := newEnv()
	a := e.createAgent(t)
	e.store.evalCases[a.ID] = []string{"c1"}
	if err := e.svc.Delete(t.Context(), ws, a.ID); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if _, ok := e.store.agents[a.ID]; ok {
		t.Error("agent still stored after Delete")
	}
	if len(e.store.evalCases[a.ID]) != 0 {
		t.Error("eval cases still stored after Delete")
	}
}

func TestService_SkillLinks(t *testing.T) {
	e := newEnv()
	e.skills("A", "B", "C")
	a := e.createAgent(t)
	ctx := t.Context()
	version := func() int { s, _ := e.svc.Get(ctx, ws, a.ID); return s.Version }

	links, err := e.svc.SetSkills(ctx, ws, a.ID, []string{"A", "B"})
	if err != nil || version() != 2 {
		t.Fatalf("SetSkills([A B]) = %v, version %d, want ok and version 2", err, version())
	}
	if diff := cmp.Diff([]string{"A", "B"}, linkIDs(links)); diff != "" {
		t.Errorf("SetSkills mismatch (-want +got):\n%s", diff)
	}

	steps := []struct {
		name        string
		run         func() ([]domain.SkillLink, error)
		want        []string
		wantVersion int
	}{
		{"reorder", func() ([]domain.SkillLink, error) { return e.svc.SetSkills(ctx, ws, a.ID, []string{"B", "A"}) }, []string{"B", "A"}, 3},
		{"same order again", func() ([]domain.SkillLink, error) { return e.svc.SetSkills(ctx, ws, a.ID, []string{"B", "A"}) }, []string{"B", "A"}, 3},
		{"link at 0", func() ([]domain.SkillLink, error) { return e.svc.LinkSkill(ctx, ws, a.ID, "C", ptr(0)) }, []string{"C", "B", "A"}, 4},
		{"relink A at the end", func() ([]domain.SkillLink, error) { return e.svc.LinkSkill(ctx, ws, a.ID, "A", nil) }, []string{"C", "B", "A"}, 4},
		{"clear", func() ([]domain.SkillLink, error) { return e.svc.SetSkills(ctx, ws, a.ID, []string{}) }, []string{}, 5},
	}
	for _, st := range steps {
		got, err := st.run()
		if err != nil {
			t.Fatalf("%s: error = %v", st.name, err)
		}
		if diff := cmp.Diff(st.want, linkIDs(got)); diff != "" {
			t.Errorf("%s: links mismatch (-want +got):\n%s", st.name, diff)
		}
		for i, l := range got {
			if l.Order != i || l.AgentID != a.ID {
				t.Errorf("%s: link %d = %+v, want order %d of agent %s", st.name, i, l, i, a.ID)
			}
		}
		if v := version(); v != st.wantVersion {
			t.Errorf("%s: version = %d, want %d", st.name, v, st.wantVersion)
		}
	}
	vs, _ := e.svc.Versions(ctx, ws, a.ID)
	if diff := cmp.Diff([]int{5, 4, 3, 2, 1}, versionNumbers(vs)); diff != "" {
		t.Errorf("Versions() mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff([]string{"C", "B", "A"}, vs[1].Config.Skills); diff != "" {
		t.Errorf("version 4 skills mismatch (-want +got):\n%s", diff)
	}
}

func TestService_UnknownSkills(t *testing.T) {
	e := newEnv()
	e.skills("A")
	e.store.skills["S"] = "ws-other"
	a := e.createAgent(t)
	if _, err := e.svc.SetSkills(t.Context(), ws, a.ID, []string{"A"}); err != nil {
		t.Fatal(err)
	}

	_, err := e.svc.SetSkills(t.Context(), ws, a.ID, []string{"A", "S", "R"})
	ae := wantApperr(t, err, apperr.KindValidation, "unknown_skill", "Unknown skill id(s) for this workspace: S, R")
	if diff := cmp.Diff(map[string]any{"skill_ids": []string{"S", "R"}}, ae.Details); diff != "" {
		t.Errorf("details mismatch (-want +got):\n%s", diff)
	}
	if got, _ := e.svc.SkillLinks(t.Context(), ws, a.ID); len(got) != 1 {
		t.Errorf("links after rejected request = %v, want the original [A]", linkIDs(got))
	}
	if stored, _ := e.svc.Get(t.Context(), ws, a.ID); stored.Version != 2 {
		t.Errorf("version after rejected request = %d, want 2", stored.Version)
	}

	_, err = e.svc.LinkSkill(t.Context(), ws, a.ID, "R", nil)
	wantApperr(t, err, apperr.KindValidation, "unknown_skill", "Unknown skill id(s) for this workspace: R")
}

func TestService_AppendSkill(t *testing.T) {
	e := newEnv()
	e.skills("A", "B")
	a := e.createAgent(t)
	for _, id := range []string{"A", "B", "A"} {
		if err := e.svc.AppendSkill(t.Context(), ws, a.ID, id); err != nil {
			t.Fatalf("AppendSkill(%s) error = %v", id, err)
		}
	}
	links, _ := e.svc.SkillLinks(t.Context(), ws, a.ID)
	if diff := cmp.Diff([]string{"B", "A"}, linkIDs(links)); diff != "" {
		t.Errorf("links after AppendSkill A, B, A mismatch (-want +got):\n%s", diff)
	}
	err := e.svc.AppendSkill(t.Context(), ws, "missing", "A")
	wantApperr(t, err, apperr.KindNotFound, "not_found", "Agent not found")
}

func TestService_Models(t *testing.T) {
	e := newEnv()
	anth := []llm.ModelInfo{{ID: "claude-x", Provider: llm.Anthropic}}
	e.models.models[llm.Anthropic] = anth
	a, err := e.svc.Create(t.Context(), ws, "u", domain.CreateInput{Name: "A", Provider: "anthropic", Model: "m", SystemPrompt: "p"})
	if err != nil {
		t.Fatal(err)
	}

	got, err := e.svc.AgentModels(t.Context(), ws, a.ID)
	if err != nil {
		t.Fatalf("AgentModels() error = %v", err)
	}
	if diff := cmp.Diff(anth, got); diff != "" {
		t.Errorf("AgentModels() mismatch (-want +got):\n%s", diff)
	}

	for _, provider := range []string{"openai", "nope"} {
		got, err := e.svc.ProviderModels(t.Context(), ws, provider)
		if err != nil || got == nil || len(got) != 0 {
			t.Errorf("ProviderModels(%q) with no models = %#v, %v, want an empty non-nil list", provider, got, err)
		}
	}
}

func TestService_ModelsFailureIsEmptyAndLogged(t *testing.T) {
	e := newEnv()
	e.models.err = apperr.ExternalService("OpenAI model list failed: 500")
	got, err := e.svc.ProviderModels(context.Background(), ws, "openai")
	if err != nil || got == nil || len(got) != 0 {
		t.Errorf("ProviderModels() on error = %#v, %v, want an empty non-nil list", got, err)
	}
	out := e.logs.String()
	if !strings.Contains(out, "level=WARN") || !strings.Contains(out, "model listing failed") || !strings.Contains(out, "provider=openai") {
		t.Errorf("log = %q, want a warn line naming the provider", out)
	}
}

func TestServiceKeepsJSONSchemaAsGiven(t *testing.T) {
	e := newEnv()
	schema := json.RawMessage(`{"type":"object"}`)
	a, err := e.svc.Create(t.Context(), ws, "u", domain.CreateInput{Name: "A", Provider: "openai", Model: "m", SystemPrompt: "p", OutputSchema: schema})
	if err != nil {
		t.Fatal(err)
	}
	if string(a.OutputSchema) != string(schema) {
		t.Errorf("Create().OutputSchema = %s, want %s", a.OutputSchema, schema)
	}
}

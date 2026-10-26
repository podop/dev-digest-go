package postgres_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/plugins/app"
	"dev-digest-go/backend/internal/plugins/domain"
)

func TestInstall_RoundTrip(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	in := plan("acme")

	got, err := e.store.Install(ctx, e.ws, in)
	if err != nil {
		t.Fatalf("Install() error = %v", err)
	}

	p := got.Plugin
	if p.Enabled {
		t.Error("Install().Plugin.Enabled = true, want false (a new plugin starts disabled)")
	}
	if p.Name != "acme" || p.Version == nil || *p.Version != "1.2.0" || p.Source == nil || *p.Source != "acme.zip" ||
		p.Description == nil || *p.Description != "the acme plugin" {
		t.Errorf("Install().Plugin = %+v, want name/version/source/description of the plan", p)
	}
	if want := (domain.PluginCounts{Agents: 2, Skills: 3, EvalCases: 2, Learnings: 2}); p.Counts != want {
		t.Errorf("Install().Plugin.Counts = %+v, want %+v", p.Counts, want)
	}
	if want := (domain.Created{Agents: 2, Skills: 3, EvalCases: 2, Learnings: 2}); got.Created != want {
		t.Errorf("Install().Created = %+v, want %+v", got.Created, want)
	}

	// Everything the plugin created points at it.
	for _, table := range []string{"agents", "skills", "memory"} {
		if n := e.count(t, `SELECT count(*) FROM `+table+` WHERE workspace_id = ? AND plugin_id IS DISTINCT FROM ?`, e.ws, p.ID); n != 0 {
			t.Errorf("%s rows of the workspace not owned by the plugin = %d, want 0", table, n)
		}
	}

	// Agents: the one with skills sits at version 2 (create, then link), the other at 1.
	versions := e.strings(t, `SELECT name||':'||version FROM agents WHERE plugin_id = ? ORDER BY created_at, id`, p.ID)
	if diff := cmp.Diff([]string{"acme-reviewer:2", "acme-idle:1"}, versions); diff != "" {
		t.Errorf("agent versions mismatch (-want +got):\n%s", diff)
	}
	reviewer := e.scalar(t, `SELECT id FROM agents WHERE plugin_id = ? AND name = 'acme-reviewer'`, p.ID)
	if n := e.count(t, `SELECT count(*) FROM agent_versions WHERE agent_id = ?`, reviewer); n != 2 {
		t.Errorf("agent_versions rows of the linked agent = %d, want 2", n)
	}
	snapSkills := e.scalar(t, `SELECT config_json->'skills'->>0 FROM agent_versions WHERE agent_id = ? AND version = 2`, reviewer)
	styleID := e.scalar(t, `SELECT id FROM skills WHERE plugin_id = ? AND name = 'acme-style'`, p.ID)
	if snapSkills != styleID {
		t.Errorf("version 2 snapshot first skill = %q, want the first linked skill %q", snapSkills, styleID)
	}
	if n := e.count(t, `SELECT jsonb_array_length(config_json->'skills') FROM agent_versions WHERE agent_id = ? AND version = 1`, reviewer); n != 0 {
		t.Errorf("version 1 snapshot skills = %d, want 0", n)
	}
	if cfg := e.scalar(t, `SELECT config_json->>'strategy' FROM agent_versions WHERE agent_id = ? AND version = 1`, reviewer); cfg != "map-reduce" {
		t.Errorf("snapshot strategy = %q, want map-reduce", cfg)
	}
	if schema := e.scalar(t, `SELECT coalesce(output_schema::text, 'NULL') FROM agents WHERE plugin_id = ? AND name = 'acme-idle'`, p.ID); schema != `{"type": "object"}` {
		t.Errorf("output_schema = %q, want the plan's schema", schema)
	}
	if schema := e.scalar(t, `SELECT coalesce(output_schema::text, 'NULL') FROM agents WHERE id = ?`, reviewer); schema != "NULL" {
		t.Errorf("output_schema of an agent without one = %q, want NULL", schema)
	}

	// Links keep the plan's order.
	links := e.strings(t, `SELECT s.name||':'||l."order" FROM agent_skills l JOIN skills s ON s.id = l.skill_id WHERE l.agent_id = ? ORDER BY l."order"`, reviewer)
	if diff := cmp.Diff([]string{"acme-style:0", "acme-sec:1"}, links); diff != "" {
		t.Errorf("agent links mismatch (-want +got):\n%s", diff)
	}

	// Skills: version 1 with the plan's note, source and ref.
	skills := e.strings(t, `SELECT name||'|'||source||'|'||source_ref||'|'||version||'|'||enabled FROM skills WHERE plugin_id = ? ORDER BY created_at, id`, p.ID)
	wantSkills := []string{
		"acme-style|imported_file|plugin:acme|1|true", "acme-sec|imported_file|plugin:acme|1|true", "acme-docs|imported_file|plugin:acme|1|false",
	}
	if diff := cmp.Diff(wantSkills, skills); diff != "" {
		t.Errorf("skills mismatch (-want +got):\n%s", diff)
	}
	notes := e.strings(t, `SELECT v.version||'|'||v.message||'|'||v.description FROM skill_versions v JOIN skills s ON s.id = v.skill_id WHERE s.plugin_id = ? ORDER BY s.created_at`, p.ID)
	wantNotes := []string{
		"1|Installed from plugin acme|style rules", "1|Installed from plugin acme|security rules", "1|Installed from plugin acme|docs",
	}
	if diff := cmp.Diff(wantNotes, notes); diff != "" {
		t.Errorf("skill versions mismatch (-want +got):\n%s", diff)
	}
	if ev := e.scalar(t, `SELECT evidence_files::text FROM skills WHERE plugin_id = ? AND name = 'acme-docs'`, p.ID); ev != `["a.md", "b.md"]` {
		t.Errorf("evidence_files = %q, want the plan's files", ev)
	}

	// Eval cases belong to the agent and skill their owner_ref named.
	owners := e.strings(t, `SELECT c.name||'|'||c.owner_kind||'|'||
			CASE c.owner_kind WHEN 'agent' THEN (SELECT name FROM agents WHERE id = c.owner_id) ELSE (SELECT name FROM skills WHERE id = c.owner_id) END
		FROM eval_cases c WHERE c.workspace_id = ? ORDER BY c.created_at`, e.ws)
	if diff := cmp.Diff([]string{"agent case|agent|acme-reviewer", "skill case|skill|acme-sec"}, owners); diff != "" {
		t.Errorf("eval case owners mismatch (-want +got):\n%s", diff)
	}
	if meta := e.scalar(t, `SELECT input_meta->>'pr_title' FROM eval_cases WHERE name = 'agent case'`); meta != "t" {
		t.Errorf("input_meta.pr_title = %q, want t", meta)
	}
	if n := e.count(t, `SELECT count(*) FROM eval_cases WHERE name = 'skill case' AND input_files IS NULL AND input_meta IS NULL AND expected_output IS NULL AND input_diff IS NULL`); n != 1 {
		t.Errorf("skill case with no JSON = %d rows, want 1 with NULL columns", n)
	}

	// Learnings: no repository, no embedding, the plan's scope.
	learnings := e.strings(t, `SELECT scope||'|'||kind||'|'||content||'|'||coalesce(confidence::text,'-')||'|'||(repo_id IS NULL)||'|'||(embedding IS NULL) FROM memory WHERE plugin_id = ? ORDER BY created_at`, p.ID)
	wantLearn := []string{"global|fact|first learning|0.75|true|true", "repo|decision|second learning|-|true|true"}
	if diff := cmp.Diff(wantLearn, learnings); diff != "" {
		t.Errorf("learnings mismatch (-want +got):\n%s", diff)
	}

	// The same plugin read back through List and an export keeps the plan's order.
	listed, err := e.store.List(ctx, e.ws)
	if err != nil || len(listed) != 1 || listed[0].ID != p.ID || listed[0].Counts != p.Counts {
		t.Errorf("List() = %+v, %v, want the installed plugin with the same counts", listed, err)
	}
	data, missing, err := e.store.ReadExport(ctx, e.ws, app.ExportSelection{EvalCases: true, Learnings: true})
	if err != nil || len(missing) != 0 {
		t.Fatalf("ReadExport() = missing %v, error %v", missing, err)
	}
	var agentNames, skillNames, caseNames, learnContents []string
	for _, a := range data.Agents {
		agentNames = append(agentNames, a.Name)
	}
	for _, s := range data.Skills {
		skillNames = append(skillNames, s.Name)
	}
	for _, c := range data.EvalCases {
		caseNames = append(caseNames, c.Name)
	}
	for _, l := range data.Learnings {
		learnContents = append(learnContents, l.Content)
	}
	for _, c := range []struct {
		what      string
		got, want []string
	}{
		{"agents", agentNames, []string{"acme-reviewer", "acme-idle"}},
		{"skills", skillNames, []string{"acme-style", "acme-sec", "acme-docs"}},
		{"eval cases", caseNames, []string{"agent case", "skill case"}},
		{"learnings", learnContents, []string{"first learning", "second learning"}},
		{"skill names of the first agent", data.Agents[0].SkillNames, []string{"acme-style", "acme-sec"}},
	} {
		if diff := cmp.Diff(c.want, c.got); diff != "" {
			t.Errorf("exported %s mismatch (-want +got):\n%s", c.what, diff)
		}
	}
}

func TestInstall_EmptyPlugin(t *testing.T) {
	e := newEnv(t)
	got, err := e.store.Install(context.Background(), e.ws, domain.Install{Name: "bare", Version: "1"})
	if err != nil {
		t.Fatalf("Install(bare) error = %v", err)
	}
	if got.Plugin.Counts != (domain.PluginCounts{}) || got.Plugin.Source != nil || got.Plugin.Description != nil || got.Plugin.Enabled {
		t.Errorf("Install(bare).Plugin = %+v, want disabled with no counts, source or description", got.Plugin)
	}
}

func TestInstall_PluginNameConflict(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	if _, err := e.store.Install(ctx, e.ws, plan("acme")); err != nil {
		t.Fatalf("first Install() error = %v", err)
	}

	// Same plugin name, different skill names.
	again := plan("acme")
	for i := range again.Skills {
		again.Skills[i].Name += "-2"
	}
	for i := range again.Agents {
		again.Agents[i].Skills = nil
	}
	again.EvalCases = nil
	_, err := e.store.Install(ctx, e.ws, again)
	var ce *domain.ConflictError
	if !errors.As(err, &ce) {
		t.Fatalf("second Install() error = %v, want *domain.ConflictError", err)
	}
	if !errors.Is(err, domain.ErrConflict) {
		t.Errorf("errors.Is(%v, ErrConflict) = false, want true", err)
	}
	if want := (domain.Conflicts{Plugin: true, Skills: []string{}}); !cmp.Equal(want, ce.Conflicts) {
		t.Errorf("conflicts = %+v, want %+v", ce.Conflicts, want)
	}
	if n := e.count(t, `SELECT count(*) FROM skills WHERE name LIKE '%-2'`); n != 0 {
		t.Errorf("skills of the refused install = %d, want 0", n)
	}

	// The same name is free in another workspace.
	if _, err := e.store.Install(ctx, e.other, plan("acme")); err != nil {
		t.Errorf("Install() in another workspace error = %v, want nil", err)
	}
}

func TestInstall_SkillNameConflict(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.userSkill(t, e.ws, "acme-sec", true)
	e.userSkill(t, e.ws, "acme-docs", true)

	_, err := e.store.Install(ctx, e.ws, plan("acme"))
	var ce *domain.ConflictError
	if !errors.As(err, &ce) {
		t.Fatalf("Install() error = %v, want *domain.ConflictError", err)
	}
	want := domain.Conflicts{Plugin: false, Skills: []string{"acme-sec", "acme-docs"}}
	if diff := cmp.Diff(want, ce.Conflicts); diff != "" {
		t.Errorf("conflicts mismatch (-want +got):\n%s", diff)
	}
	for _, table := range []string{"installed_plugins", "agents", "agent_versions", "agent_skills", "eval_cases", "memory"} {
		if n := e.count(t, `SELECT count(*) FROM `+table); n != 0 {
			t.Errorf("rows in %s after a refused install = %d, want 0", table, n)
		}
	}
	if n := e.count(t, `SELECT count(*) FROM skills`); n != 2 {
		t.Errorf("skills after a refused install = %d, want only the 2 that existed", n)
	}
}

func TestInstall_ConcurrentSameName(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()

	const racers = 2
	start := make(chan struct{})
	errs := make([]error, racers)
	var wg sync.WaitGroup
	for i := range racers {
		wg.Go(func() {
			<-start
			_, errs[i] = e.store.Install(ctx, e.ws, plan("race"))
		})
	}
	close(start)
	wg.Wait()

	var ok, conflicts int
	for _, err := range errs {
		var ce *domain.ConflictError
		switch {
		case err == nil:
			ok++
		case errors.As(err, &ce) && ce.Conflicts.Plugin:
			conflicts++
		default:
			t.Errorf("Install() error = %v, want nil or a plugin *domain.ConflictError", err)
		}
	}
	if ok != 1 || conflicts != 1 {
		t.Fatalf("results = %d ok, %d conflicts, want exactly 1 and 1", ok, conflicts)
	}
	if n := e.count(t, `SELECT count(*) FROM installed_plugins`); n != 1 {
		t.Errorf("plugins = %d, want 1", n)
	}
	// Nothing of the loser remains: one install's worth of rows.
	for table, want := range map[string]int{"agents": 2, "skills": 3, "eval_cases": 2, "memory": 2, "agent_versions": 3, "skill_versions": 3} {
		if n := e.count(t, `SELECT count(*) FROM `+table); n != want {
			t.Errorf("rows in %s = %d, want %d", table, n, want)
		}
	}
}

func TestInstall_ConcurrentSameSkillName(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()

	// Two different plugins that both bring a skill named "shared".
	mk := func(name string) domain.Install {
		return domain.Install{Name: name, Version: "1", Skills: []domain.InstallSkill{
			{Name: "shared", Description: "d", Type: "custom", Source: "imported_file", Body: "b", Enabled: true, VersionNote: "n"},
		}}
	}
	start := make(chan struct{})
	errs := make([]error, 2)
	var wg sync.WaitGroup
	for i, name := range []string{"one", "two"} {
		wg.Go(func() {
			<-start
			_, errs[i] = e.store.Install(ctx, e.ws, mk(name))
		})
	}
	close(start)
	wg.Wait()

	var ok, conflicts int
	for _, err := range errs {
		var ce *domain.ConflictError
		switch {
		case err == nil:
			ok++
		case errors.As(err, &ce) && cmp.Equal(ce.Conflicts.Skills, []string{"shared"}):
			conflicts++
		default:
			t.Errorf("Install() error = %v, want nil or a skill *domain.ConflictError", err)
		}
	}
	if ok != 1 || conflicts != 1 {
		t.Fatalf("results = %d ok, %d conflicts, want exactly 1 and 1", ok, conflicts)
	}
	if n := e.count(t, `SELECT count(*) FROM installed_plugins`); n != 1 {
		t.Errorf("plugins = %d, want 1 (the loser's row rolled back)", n)
	}
}

// TestInstall_MidwayFailureLeavesNothing makes the last statement of an install (the learnings)
// fail with a trigger, after the plugin, skills, agents, links and eval cases are written.
func TestInstall_MidwayFailureLeavesNothing(t *testing.T) {
	e := newEnv(t)
	e.exec(t, `CREATE FUNCTION fail_memory() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'boom'; END $$`)
	e.exec(t, `CREATE TRIGGER fail_memory BEFORE INSERT ON memory FOR EACH ROW EXECUTE FUNCTION fail_memory()`)

	_, err := e.store.Install(context.Background(), e.ws, plan("acme"))
	if err == nil {
		t.Fatal("Install() error = nil, want the trigger's failure")
	}
	var ce *domain.ConflictError
	if errors.As(err, &ce) {
		t.Fatalf("Install() error = %v, want a plain failure, not a conflict", err)
	}
	for _, table := range []string{"installed_plugins", "agents", "agent_versions", "agent_skills", "skills", "skill_versions", "eval_cases", "memory"} {
		if n := e.count(t, `SELECT count(*) FROM `+table); n != 0 {
			t.Errorf("rows in %s after a failed install = %d, want 0 (EC6)", table, n)
		}
	}

	// The same plan installs once the failure is gone: nothing was left to clash with.
	e.exec(t, `DROP TRIGGER fail_memory ON memory`)
	if _, err := e.store.Install(context.Background(), e.ws, plan("acme")); err != nil {
		t.Errorf("Install() after the failure error = %v, want nil", err)
	}
}

func TestInstall_NULInJSONIsReplaced(t *testing.T) {
	e := newEnv(t)
	in := plan("acme")
	in.Agents[1].OutputSchema = json.RawMessage(`{"description":"a\u0000b"}`)
	in.EvalCases[0].InputFiles = json.RawMessage(`["x\u0000y"]`)
	in.Skills[0].EvidenceFiles = []string{"a\x00b"}
	in.Learnings[0].Content = "c\x00d"

	if _, err := e.store.Install(context.Background(), e.ws, in); err != nil {
		t.Fatalf("Install() error = %v, want the NULs replaced", err)
	}
	if got := e.scalar(t, `SELECT output_schema->>'description' FROM agents WHERE name = 'acme-idle'`); got != "a�b" {
		t.Errorf("output_schema description = %q, want U+FFFD for the NUL", got)
	}
	if got := e.scalar(t, `SELECT content FROM memory WHERE content LIKE 'c%'`); got != "c�d" {
		t.Errorf("learning content = %q, want U+FFFD for the NUL", got)
	}
}

func TestInstall_ManyRowsBatched(t *testing.T) {
	e := newEnv(t)
	in := domain.Install{Name: "big", Version: "1"}
	for i := range 1200 {
		in.Learnings = append(in.Learnings, domain.InstallLearning{Scope: "global", Kind: "fact", Content: "l" + string(rune('a'+i%26))})
	}
	start := time.Now()
	got, err := e.store.Install(context.Background(), e.ws, in)
	if err != nil {
		t.Fatalf("Install(1200 learnings) error = %v", err)
	}
	if got.Plugin.Counts.Learnings != 1200 {
		t.Errorf("learnings = %d, want 1200", got.Plugin.Counts.Learnings)
	}
	t.Logf("installed 1200 learnings in %v", time.Since(start))
}

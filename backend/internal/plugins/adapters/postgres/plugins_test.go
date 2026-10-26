package postgres_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/plugins/domain"
)

func TestList_NewestFirstWithCounts(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()

	if got, err := e.store.List(ctx, e.ws); err != nil || len(got) != 0 {
		t.Fatalf("List() on an empty workspace = %v, %v, want no plugins", got, err)
	}

	first, err := e.store.Install(ctx, e.ws, plan("first"))
	if err != nil {
		t.Fatalf("Install(first) error = %v", err)
	}
	second, err := e.store.Install(ctx, e.ws, domain.Install{Name: "second", Version: "1", Learnings: []domain.InstallLearning{
		{Scope: "global", Kind: "fact", Content: "x"},
	}})
	if err != nil {
		t.Fatalf("Install(second) error = %v", err)
	}
	if _, err := e.store.Install(ctx, e.other, plan("elsewhere")); err != nil {
		t.Fatalf("Install(elsewhere) error = %v", err)
	}

	// What the user adds around the plugin is not the plugin's: a user agent, skill, case, learning.
	ua := e.userAgent(t, e.ws, "mine", true)
	e.evalCase(t, e.ws, "agent", ua, "mine case")
	e.userSkill(t, e.ws, "my-skill", true)
	e.learning(t, e.ws, "", "mine")
	// An eval case added later to a plugin agent belongs to the plugin.
	reviewer := e.scalar(t, `SELECT id FROM agents WHERE plugin_id = ? AND name = 'first-reviewer'`, first.Plugin.ID)
	e.evalCase(t, e.ws, "agent", reviewer, "added later")

	got, err := e.store.List(ctx, e.ws)
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	ids := make([]string, len(got))
	for i, p := range got {
		ids[i] = p.Name
	}
	if diff := cmp.Diff([]string{"second", "first"}, ids); diff != "" {
		t.Fatalf("List() order mismatch (-want +got):\n%s", diff)
	}
	if want := (domain.PluginCounts{Learnings: 1}); got[0].Counts != want || got[0].ID != second.Plugin.ID {
		t.Errorf("List()[0] = %+v, want the second plugin with counts %+v", got[0], want)
	}
	if want := (domain.PluginCounts{Agents: 2, Skills: 3, EvalCases: 3, Learnings: 2}); got[1].Counts != want {
		t.Errorf("List()[1].Counts = %+v, want %+v", got[1].Counts, want)
	}
	if got[1].Enabled || got[1].InstalledAt.IsZero() || got[1].Source == nil || *got[1].Source != "first.zip" {
		t.Errorf("List()[1] = %+v, want a disabled plugin with installed_at and source", got[1])
	}
}

func TestSetEnabled(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	installed, err := e.store.Install(ctx, e.ws, plan("acme"))
	if err != nil {
		t.Fatalf("Install() error = %v", err)
	}
	id := installed.Plugin.ID

	on, err := e.store.SetEnabled(ctx, e.ws, id, true)
	if err != nil || !on.Enabled || on.Counts != installed.Plugin.Counts || on.Name != "acme" {
		t.Fatalf("SetEnabled(true) = %+v, %v, want the enabled plugin with its counts", on, err)
	}
	off, err := e.store.SetEnabled(ctx, e.ws, id, false)
	if err != nil || off.Enabled {
		t.Fatalf("SetEnabled(false) = %+v, %v, want the disabled plugin", off, err)
	}
	// The flag is the plugin's alone: what it owns is not touched.
	if n := e.count(t, `SELECT count(*) FROM agents WHERE enabled`); n != 1 {
		t.Errorf("enabled agents = %d, want 1 (the plan's own flags)", n)
	}

	for name, id := range map[string]string{
		"unknown":            "00000000-0000-4000-8000-000000000000",
		"malformed":          "not-a-uuid",
		"empty":              "",
		"sql-ish":            "x' OR '1'='1",
		"id of a workspace":  e.ws,
		"upper-case garbage": strings.ToUpper("zzzzzzzz-zzzz-zzzz-zzzz-zzzzzzzzzzzz"),
	} {
		if _, err := e.store.SetEnabled(ctx, e.ws, id, true); !errors.Is(err, domain.ErrNotFound) {
			t.Errorf("SetEnabled(%s) error = %v, want ErrNotFound", name, err)
		}
	}
	if _, err := e.store.SetEnabled(ctx, e.other, id, true); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("SetEnabled() from another workspace error = %v, want ErrNotFound", err)
	}
	if p, _ := e.store.SetEnabled(ctx, e.ws, id, false); p.Enabled {
		t.Error("the other workspace's SetEnabled changed the plugin")
	}
}

func TestConflicts(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	if _, err := e.store.Install(ctx, e.ws, plan("acme")); err != nil {
		t.Fatalf("Install() error = %v", err)
	}
	e.userSkill(t, e.ws, "Mine", true)
	e.userSkill(t, e.other, "elsewhere", true)

	tests := []struct {
		name   string
		plugin string
		skills []string
		want   domain.Conflicts
	}{
		{"nothing taken", "fresh", []string{"a", "b"}, domain.Conflicts{Skills: []string{}}},
		{"plugin name taken", "acme", nil, domain.Conflicts{Plugin: true, Skills: []string{}}},
		{"plugin name is case sensitive", "ACME", nil, domain.Conflicts{Skills: []string{}}},
		{"skill names keep the request order", "fresh", []string{"acme-sec", "zzz", "Mine", "acme-style"},
			domain.Conflicts{Skills: []string{"acme-sec", "Mine", "acme-style"}}},
		{"skill names are case sensitive", "fresh", []string{"mine", "MINE"}, domain.Conflicts{Skills: []string{}}},
		{"another workspace's skill is no conflict", "fresh", []string{"elsewhere"}, domain.Conflicts{Skills: []string{}}},
		{"a name listed twice is reported once", "fresh", []string{"Mine", "Mine"}, domain.Conflicts{Skills: []string{"Mine"}}},
		{"a NUL name cannot clash and cannot fail", "a\x00b", []string{"a\x00b"}, domain.Conflicts{Skills: []string{}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := e.store.Conflicts(ctx, e.ws, tc.plugin, tc.skills)
			if err != nil {
				t.Fatalf("Conflicts(%q, %q) error = %v", tc.plugin, tc.skills, err)
			}
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("Conflicts(%q, %q) mismatch (-want +got):\n%s", tc.plugin, tc.skills, diff)
			}
		})
	}
}

// removalFixture installs "acme" and its neighbours: a second plugin, a user agent that links one
// of acme's skills, a CI installation and a run of an acme agent.
type removalFixture struct {
	*env
	plugin, neighbour string
	userAgent         string
	secSkill          string
	reviewer          string
	run               string
}

func newRemovalFixture(t *testing.T) *removalFixture {
	t.Helper()
	e := newEnv(t)
	ctx := context.Background()
	acme, err := e.store.Install(ctx, e.ws, plan("acme"))
	if err != nil {
		t.Fatalf("Install(acme) error = %v", err)
	}
	neighbour, err := e.store.Install(ctx, e.ws, plan("keep"))
	if err != nil {
		t.Fatalf("Install(keep) error = %v", err)
	}
	f := &removalFixture{env: e, plugin: acme.Plugin.ID, neighbour: neighbour.Plugin.ID}
	f.reviewer = e.scalar(t, `SELECT id FROM agents WHERE plugin_id = ? AND name = 'acme-reviewer'`, f.plugin)
	f.secSkill = e.scalar(t, `SELECT id FROM skills WHERE plugin_id = ? AND name = 'acme-sec'`, f.plugin)
	f.userAgent = e.userAgent(t, e.ws, "mine", true)
	e.link(t, f.userAgent, f.secSkill, 0)
	e.evalCase(t, e.ws, "agent", f.userAgent, "mine case")
	e.evalCase(t, e.ws, "agent", f.reviewer, "added later")
	e.learning(t, e.ws, "", "mine")
	e.exec(t, `INSERT INTO ci_installations (agent_id, repo, target_type, provider) VALUES (?, 'o/r', 'gha', 'openrouter')`, f.reviewer)
	e.exec(t, `INSERT INTO ci_installations (agent_id, repo, target_type, provider) VALUES (?, 'o/r2', 'gha', 'openrouter')`, f.reviewer)
	e.exec(t, `INSERT INTO ci_installations (agent_id, repo, target_type, provider)
		VALUES ((SELECT id FROM agents WHERE plugin_id = ? AND name = 'keep-reviewer'), 'o/r', 'gha', 'openrouter')`, f.neighbour)
	f.run = e.scalar(t, `INSERT INTO agent_runs (workspace_id, agent_id, status) VALUES (?, ?, 'done') RETURNING id`, e.ws, f.reviewer)
	return f
}

func TestRemove(t *testing.T) {
	f := newRemovalFixture(t)
	ctx := context.Background()
	var asked []string

	got, err := f.store.Remove(ctx, f.ws, f.plugin, func(name string) error {
		asked = append(asked, name)
		return nil
	})
	if err != nil {
		t.Fatalf("Remove() error = %v", err)
	}
	// 2 agents; 3 skills; the 2 plan cases + "added later"; 2 learnings; 2 CI installations of acme agents.
	want := domain.Removed{Agents: 2, Skills: 3, EvalCases: 3, Learnings: 2, CIInstallations: 2}
	if got != want {
		t.Errorf("Remove() = %+v, want %+v", got, want)
	}
	if diff := cmp.Diff([]string{"acme"}, asked); diff != "" {
		t.Errorf("check calls mismatch (-want +got):\n%s", diff)
	}

	for _, q := range []struct {
		what, sql string
		want      int
	}{
		{"the plugin", `SELECT count(*) FROM installed_plugins WHERE id = '` + f.plugin + `'`, 0},
		{"agents of the plugin", `SELECT count(*) FROM agents WHERE name LIKE 'acme-%'`, 0},
		{"skills of the plugin", `SELECT count(*) FROM skills WHERE name LIKE 'acme-%'`, 0},
		{"versions of its agents", `SELECT count(*) FROM agent_versions WHERE agent_id = '` + f.reviewer + `'`, 0},
		{"links of its agents", `SELECT count(*) FROM agent_skills WHERE agent_id = '` + f.reviewer + `'`, 0},
		{"versions of its skills", `SELECT count(*) FROM skill_versions WHERE skill_id = '` + f.secSkill + `'`, 0},
		{"its CI installations", `SELECT count(*) FROM ci_installations WHERE repo IN ('o/r2')`, 0},
		{"its learnings", `SELECT count(*) FROM memory WHERE plugin_id = '` + f.plugin + `'`, 0},
		// What is not the plugin's stays.
		{"the neighbour plugin", `SELECT count(*) FROM installed_plugins WHERE id = '` + f.neighbour + `'`, 1},
		{"the neighbour's agents", `SELECT count(*) FROM agents WHERE plugin_id = '` + f.neighbour + `'`, 2},
		{"the neighbour's skills", `SELECT count(*) FROM skills WHERE plugin_id = '` + f.neighbour + `'`, 3},
		{"the neighbour's cases", `SELECT count(*) FROM eval_cases WHERE name IN ('agent case', 'skill case') AND owner_id IN (SELECT id FROM agents UNION SELECT id FROM skills)`, 2},
		{"the neighbour's CI installation", `SELECT count(*) FROM ci_installations`, 1},
		{"the user's agent (EC8)", `SELECT count(*) FROM agents WHERE id = '` + f.userAgent + `'`, 1},
		{"the user's link to the removed skill (EC8)", `SELECT count(*) FROM agent_skills WHERE agent_id = '` + f.userAgent + `'`, 0},
		{"the user's eval case", `SELECT count(*) FROM eval_cases WHERE name = 'mine case'`, 1},
		{"the user's learning", `SELECT count(*) FROM memory WHERE plugin_id IS NULL`, 1},
		{"the run record (EC7)", `SELECT count(*) FROM agent_runs WHERE id = '` + f.run + `' AND agent_id IS NULL`, 1},
	} {
		if n := f.count(t, q.sql); n != q.want {
			t.Errorf("%s: %d rows, want %d", q.what, n, q.want)
		}
	}
	if n := f.count(t, `SELECT count(*) FROM eval_cases`); n != 3 {
		t.Errorf("eval cases left = %d, want 3 (the neighbour's 2 and the user's)", n)
	}

	// A removed plugin is gone for a second removal and the name is free again.
	if _, err := f.store.Remove(ctx, f.ws, f.plugin, func(string) error { return nil }); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("second Remove() error = %v, want ErrNotFound", err)
	}
	if _, err := f.store.Install(ctx, f.ws, plan("acme")); err != nil {
		t.Errorf("Install() after the removal error = %v, want the name and skill names free", err)
	}
}

var errMismatch = errors.New("confirmation mismatch")

func TestRemove_CheckFailureLeavesEverything(t *testing.T) {
	f := newRemovalFixture(t)
	snapshot := func() []int {
		var out []int
		for _, table := range []string{"installed_plugins", "agents", "agent_versions", "agent_skills", "skills", "skill_versions", "eval_cases", "memory", "ci_installations", "agent_runs"} {
			out = append(out, f.count(t, `SELECT count(*) FROM `+table))
		}
		return out
	}
	before := snapshot()

	_, err := f.store.Remove(context.Background(), f.ws, f.plugin, func(name string) error { return errMismatch })
	if !errors.Is(err, errMismatch) || err.Error() != errMismatch.Error() {
		t.Errorf("Remove() error = %v, want the check's error unchanged", err)
	}
	if diff := cmp.Diff(before, snapshot()); diff != "" {
		t.Errorf("row counts changed by a refused removal (-before +after):\n%s", diff)
	}
}

func TestRemove_NotFound(t *testing.T) {
	f := newRemovalFixture(t)
	called := false
	check := func(string) error { called = true; return nil }
	ctx := context.Background()

	for name, tc := range map[string]struct{ ws, id string }{
		"unknown":                {f.ws, "00000000-0000-4000-8000-000000000000"},
		"malformed":              {f.ws, "nope"},
		"empty":                  {f.ws, ""},
		"another workspace":      {f.other, f.plugin},
		"a different table id":   {f.ws, f.reviewer},
		"the other workspace id": {f.ws, f.other},
	} {
		if _, err := f.store.Remove(ctx, tc.ws, tc.id, check); !errors.Is(err, domain.ErrNotFound) {
			t.Errorf("Remove(%s) error = %v, want ErrNotFound", name, err)
		}
	}
	if called {
		t.Error("check was called for a plugin that does not exist")
	}
	if n := f.count(t, `SELECT count(*) FROM installed_plugins`); n != 2 {
		t.Errorf("plugins = %d, want 2", n)
	}
}

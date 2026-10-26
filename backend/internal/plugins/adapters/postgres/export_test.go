package postgres_test

import (
	"context"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/plugins/app"
	"dev-digest-go/backend/internal/plugins/domain"
)

// exportFixture holds, in this creation order:
//
//	agents  ua1 (links us2 then us1), ua2 (disabled, no links), then the plugin's p-reviewer, p-idle
//	skills  us1, us2, us3 (disabled), then the plugin's p-style, p-sec, p-docs
//
// us3 is linked by nobody. Eval cases: one of ua1, one of us3, one of ua2. Learnings: one with a
// repository, one without.
type exportFixture struct {
	*env
	ua1, ua2, us1, us2, us3 string
}

func newExportFixture(t *testing.T) *exportFixture {
	t.Helper()
	e := newEnv(t)
	f := &exportFixture{env: e}
	f.ua1 = e.userAgent(t, e.ws, "ua1", true)
	f.ua2 = e.userAgent(t, e.ws, "ua2", false)
	f.us1 = e.userSkill(t, e.ws, "us1", true)
	f.us2 = e.userSkill(t, e.ws, "us2", true)
	f.us3 = e.userSkill(t, e.ws, "us3", false)
	e.link(t, f.ua1, f.us2, 0)
	e.link(t, f.ua1, f.us1, 1)
	e.evalCase(t, e.ws, "agent", f.ua1, "case of ua1")
	e.evalCase(t, e.ws, "skill", f.us3, "case of us3")
	e.evalCase(t, e.ws, "agent", f.ua2, "case of ua2")
	if _, err := e.store.Install(context.Background(), e.ws, plan("p")); err != nil {
		t.Fatalf("Install(p) error = %v", err)
	}
	// Another workspace's records never show up.
	e.userAgent(t, e.other, "alien", true)
	e.userSkill(t, e.other, "alien-skill", true)
	return f
}

func agentNames(d domain.ExportData) []string {
	out := []string{}
	for _, a := range d.Agents {
		out = append(out, a.Name)
	}
	return out
}

func skillNames(d domain.ExportData) []string {
	out := []string{}
	for _, s := range d.Skills {
		out = append(out, s.Name)
	}
	return out
}

func caseNames(d domain.ExportData) []string {
	out := []string{}
	for _, c := range d.EvalCases {
		out = append(out, c.Name)
	}
	return out
}

func TestReadExport_Selection(t *testing.T) {
	f := newExportFixture(t)
	ctx := context.Background()
	all := app.ExportSelection{}

	tests := []struct {
		name       string
		sel        app.ExportSelection
		wantAgents []string
		wantSkills []string
		wantCases  []string
	}{
		{"nothing selected exports everything", all,
			[]string{"ua1", "ua2", "p-reviewer", "p-idle"}, []string{"us1", "us2", "us3", "p-style", "p-sec", "p-docs"}, nil},
		{"agents bring their linked skills", app.ExportSelection{AgentIDs: []string{f.ua1}},
			[]string{"ua1"}, []string{"us1", "us2"}, nil},
		{"an agent without links brings no skill", app.ExportSelection{AgentIDs: []string{f.ua2}},
			[]string{"ua2"}, []string{}, nil},
		{"skills alone bring no agent", app.ExportSelection{SkillIDs: []string{f.us3}},
			[]string{}, []string{"us3"}, nil},
		{"both give the union", app.ExportSelection{AgentIDs: []string{f.ua2}, SkillIDs: []string{f.us3}},
			[]string{"ua2"}, []string{"us3"}, nil},
		{"a skill selected and linked is exported once", app.ExportSelection{AgentIDs: []string{f.ua1}, SkillIDs: []string{f.us1, f.us3}},
			[]string{"ua1"}, []string{"us1", "us2", "us3"}, nil},
		{"the same id twice is one record", app.ExportSelection{AgentIDs: []string{f.ua2, f.ua2}},
			[]string{"ua2"}, []string{}, nil},
		{"an upper-case id names the same record", app.ExportSelection{AgentIDs: []string{strings.ToUpper(f.ua2)}},
			[]string{"ua2"}, []string{}, nil},
		{"agents in creation order, not request order", app.ExportSelection{AgentIDs: []string{f.ua2, f.ua1}},
			[]string{"ua1", "ua2"}, []string{"us1", "us2"}, nil},
		{"eval cases of the exported owners", app.ExportSelection{AgentIDs: []string{f.ua1}, SkillIDs: []string{f.us3}, EvalCases: true},
			[]string{"ua1"}, []string{"us1", "us2", "us3"}, []string{"case of ua1", "case of us3"}},
		{"eval cases are not read unless asked", app.ExportSelection{SkillIDs: []string{f.us3}},
			[]string{}, []string{"us3"}, nil},
		{"eval cases of nothing selected are all of them", app.ExportSelection{EvalCases: true},
			[]string{"ua1", "ua2", "p-reviewer", "p-idle"}, []string{"us1", "us2", "us3", "p-style", "p-sec", "p-docs"},
			[]string{"case of ua1", "case of us3", "case of ua2", "agent case", "skill case"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			data, missing, err := f.store.ReadExport(ctx, f.ws, tc.sel)
			if err != nil {
				t.Fatalf("ReadExport(%+v) error = %v", tc.sel, err)
			}
			if len(missing) != 0 {
				t.Errorf("ReadExport(%+v) missing = %v, want none", tc.sel, missing)
			}
			if diff := cmp.Diff(tc.wantAgents, agentNames(data)); diff != "" {
				t.Errorf("ReadExport(%+v) agents mismatch (-want +got):\n%s", tc.sel, diff)
			}
			if diff := cmp.Diff(tc.wantSkills, skillNames(data)); diff != "" {
				t.Errorf("ReadExport(%+v) skills mismatch (-want +got):\n%s", tc.sel, diff)
			}
			wantCases := tc.wantCases
			if wantCases == nil {
				wantCases = []string{}
			}
			if diff := cmp.Diff(wantCases, caseNames(data)); diff != "" {
				t.Errorf("ReadExport(%+v) eval cases mismatch (-want +got):\n%s", tc.sel, diff)
			}
			if len(data.Learnings) != 0 {
				t.Errorf("ReadExport(%+v) learnings = %v, want none (not asked for)", tc.sel, data.Learnings)
			}
		})
	}
}

func TestReadExport_RecordFields(t *testing.T) {
	f := newExportFixture(t)
	data, _, err := f.store.ReadExport(context.Background(), f.ws, app.ExportSelection{EvalCases: true})
	if err != nil {
		t.Fatalf("ReadExport() error = %v", err)
	}
	byName := map[string]domain.AgentRecord{}
	for _, a := range data.Agents {
		byName[a.Name] = a
	}
	// Link order, not name order.
	if diff := cmp.Diff([]string{"us2", "us1"}, byName["ua1"].SkillNames); diff != "" {
		t.Errorf("ua1 SkillNames mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff([]string{"p-style", "p-sec"}, byName["p-reviewer"].SkillNames); diff != "" {
		t.Errorf("p-reviewer SkillNames mismatch (-want +got):\n%s", diff)
	}
	// EC10: own flags, plugin-owned or not.
	if byName["ua2"].Enabled || !byName["ua1"].Enabled || byName["p-idle"].Enabled || !byName["p-reviewer"].Enabled {
		t.Errorf("enabled flags = ua1 %v ua2 %v p-reviewer %v p-idle %v, want true false true false",
			byName["ua1"].Enabled, byName["ua2"].Enabled, byName["p-reviewer"].Enabled, byName["p-idle"].Enabled)
	}
	if got := string(byName["p-idle"].OutputSchema); got != `{"type": "object"}` {
		t.Errorf("p-idle OutputSchema = %q, want the stored schema", got)
	}
	if byName["ua1"].OutputSchema != nil {
		t.Errorf("ua1 OutputSchema = %q, want nil for NULL", byName["ua1"].OutputSchema)
	}
	if a := byName["p-reviewer"]; a.Strategy != "map-reduce" || a.CIFailOn != "warning" || !a.RepoIntel {
		t.Errorf("p-reviewer = %+v, want strategy map-reduce, ci_fail_on warning, repo_intel", a)
	}

	skills := map[string]domain.SkillRecord{}
	for _, s := range data.Skills {
		skills[s.Name] = s
	}
	if skills["us3"].Enabled || !skills["us1"].Enabled || skills["p-docs"].Enabled {
		t.Errorf("skill flags us3 %v us1 %v p-docs %v, want false true false", skills["us3"].Enabled, skills["us1"].Enabled, skills["p-docs"].Enabled)
	}
	if diff := cmp.Diff([]string{"a.md", "b.md"}, skills["p-docs"].EvidenceFiles); diff != "" {
		t.Errorf("p-docs EvidenceFiles mismatch (-want +got):\n%s", diff)
	}
	if skills["us1"].EvidenceFiles != nil || skills["us1"].Body != "body of us1" || skills["p-sec"].Source != "imported_file" {
		t.Errorf("skills us1 / p-sec = %+v / %+v, want nil evidence and the stored body and source", skills["us1"], skills["p-sec"])
	}

	// An eval case refers to its owner by an id of the data.
	ids := map[string]string{}
	for _, a := range data.Agents {
		ids[a.ID] = "agent:" + a.Name
	}
	for _, s := range data.Skills {
		ids[s.ID] = "skill:" + s.Name
	}
	for _, c := range data.EvalCases {
		if c.Name == "agent case" {
			if got := ids[c.OwnerID]; got != "agent:p-reviewer" {
				t.Errorf("agent case owner = %q, want agent:p-reviewer", got)
			}
			if c.InputDiff == nil || *c.InputDiff != "diff --git" || string(c.InputFiles) != `["a.go"]` ||
				string(c.InputMeta) != `{"pr_title": "t"}` || string(c.ExpectedOutput) != `{"kind": "must_find"}` || c.Notes == nil || *c.Notes != "n" {
				t.Errorf("agent case = %+v, want the stored fields", c)
			}
		}
		if c.Name == "skill case" && (ids[c.OwnerID] != "skill:p-sec" || c.InputFiles != nil || c.Notes != nil) {
			t.Errorf("skill case = %+v, want owner skill:p-sec and NULL fields", c)
		}
	}
}

func TestReadExport_Missing(t *testing.T) {
	f := newExportFixture(t)
	ctx := context.Background()
	unknown := "00000000-0000-4000-8000-0000000000aa"
	alienAgent := f.scalar(t, `SELECT id FROM agents WHERE workspace_id = ?`, f.other)
	alienSkill := f.scalar(t, `SELECT id FROM skills WHERE workspace_id = ?`, f.other)

	tests := []struct {
		name string
		sel  app.ExportSelection
		want []string
	}{
		{"unknown agent", app.ExportSelection{AgentIDs: []string{unknown}}, []string{unknown}},
		{"unknown skill", app.ExportSelection{SkillIDs: []string{unknown}}, []string{unknown}},
		{"malformed ids are missing, not an error", app.ExportSelection{AgentIDs: []string{"nope", ""}, SkillIDs: []string{"x' OR '1'='1"}},
			[]string{"nope", "", "x' OR '1'='1"}},
		{"another workspace's records", app.ExportSelection{AgentIDs: []string{alienAgent}, SkillIDs: []string{alienSkill}},
			[]string{alienAgent, alienSkill}},
		{"a skill id given as an agent id", app.ExportSelection{AgentIDs: []string{f.us1}}, []string{f.us1}},
		{"an agent id given as a skill id", app.ExportSelection{SkillIDs: []string{f.ua1}}, []string{f.ua1}},
		{"existing ids do not hide missing ones; agents first, request order, once",
			app.ExportSelection{AgentIDs: []string{f.ua1, unknown, "bad", unknown}, SkillIDs: []string{f.us1, alienSkill}},
			[]string{unknown, "bad", alienSkill}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tc.sel.EvalCases, tc.sel.Learnings = true, true
			data, missing, err := f.store.ReadExport(ctx, f.ws, tc.sel)
			if err != nil {
				t.Fatalf("ReadExport(%+v) error = %v", tc.sel, err)
			}
			if diff := cmp.Diff(tc.want, missing); diff != "" {
				t.Errorf("ReadExport(%+v) missing mismatch (-want +got):\n%s", tc.sel, diff)
			}
			if len(data.Agents)+len(data.Skills)+len(data.EvalCases)+len(data.Learnings) != 0 {
				t.Errorf("ReadExport(%+v) data = %+v, want empty with missing ids", tc.sel, data)
			}
		})
	}
}

func TestReadExport_Learnings(t *testing.T) {
	f := newExportFixture(t)
	ctx := context.Background()

	// EC11: nothing to export is an empty list, not an error.
	data, missing, err := f.store.ReadExport(ctx, f.ws, app.ExportSelection{Learnings: true})
	if err != nil || len(missing) != 0 {
		t.Fatalf("ReadExport(Learnings) on a workspace of 2 plugin learnings: missing %v, error %v", missing, err)
	}
	if got := len(data.Learnings); got != 2 {
		t.Fatalf("learnings = %d, want the plugin's 2 (plugin-owned ones are exported too)", got)
	}

	repo := f.repo(t, f.ws, "acme/api")
	f.learning(t, f.ws, repo, "about the repo")
	f.learning(t, f.ws, "", "global thought")
	f.learning(t, f.other, "", "someone else's")

	data, _, err = f.store.ReadExport(ctx, f.ws, app.ExportSelection{SkillIDs: []string{f.us1}, Learnings: true})
	if err != nil {
		t.Fatalf("ReadExport() error = %v", err)
	}
	var got []string
	for _, l := range data.Learnings {
		repoName := "-"
		if l.Repo != nil {
			repoName = *l.Repo
		}
		got = append(got, l.Content+"|"+repoName)
	}
	want := []string{"first learning|-", "second learning|-", "about the repo|acme/api", "global thought|-"}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("learnings mismatch (-want +got):\n%s", diff)
	}
	if l := data.Learnings[0]; l.Scope != "global" || l.Kind != "fact" || l.Confidence == nil || *l.Confidence != 0.75 {
		t.Errorf("first learning = %+v, want scope global, kind fact, confidence 0.75", l)
	}
}

func TestReadExport_NoLearnings(t *testing.T) {
	e := newEnv(t)
	data, missing, err := e.store.ReadExport(context.Background(), e.ws, app.ExportSelection{Learnings: true, EvalCases: true})
	if err != nil || len(missing) != 0 {
		t.Fatalf("ReadExport() on an empty workspace: missing %v, error %v", missing, err)
	}
	if len(data.Learnings) != 0 || len(data.Agents) != 0 || len(data.Skills) != 0 || len(data.EvalCases) != 0 {
		t.Errorf("ReadExport() = %+v, want empty data (EC11)", data)
	}
}

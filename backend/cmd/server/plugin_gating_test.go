package main

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
)

// Component tests of the plugin run gating (Settings D, plan S25, spec FR13, AC13, AC14, AC16, EC7,
// EC8, EC9, decision D17): a disabled plugin's agents and skills are not used by a new run, through
// all seven run entry points, and a removal keeps the user's own agents. The plugin is installed
// through the real POST /plugins/import route (a bundle .json), so the tests also see the rows the
// install writes; the toggle and the removal go through PATCH and DELETE /plugins/:id.
//
// Helpers are prefixed pg (the Settings D tests of settings_d_test.go use sd).

const (
	pgPlugin     = "gate-plugin"
	pgAgentName  = "Gate Agent"
	pgAgent2Name = "Gate Agent Two"
	pgSkillName  = "gate-skill"
	// pgPluginBody and pgUserBody are the skill bodies the prompts and the stored configurations show.
	pgPluginBody = "PG-PLUGIN-SKILL-BODY"
	pgUserBody   = "PG-USER-SKILL-BODY"
	pgUserSkill  = "user-skill"
)

// pgFix is an installed plugin (disabled, as an install leaves it) with its agents and skill, plus
// the user's own agent and skill (agent u links both skills).
type pgFix struct {
	plugin string
	p, p2  string // the plugin's agents: "Gate Agent" links ps, "Gate Agent Two" links nothing
	ps     string // the plugin's skill "gate-skill"
	u, us  string // the user's agent and skill
}

func pgAgentDoc(name string, skills ...string) map[string]any {
	if skills == nil {
		skills = []string{}
	}
	return map[string]any{
		"name": name, "description": "", "provider": "openrouter", "model": "deepseek/deepseek-v4-flash",
		"system_prompt": "Review the diff.", "output_schema": nil, "enabled": true, "skills": skills,
		"strategy": "single-pass", "ci_fail_on": "critical", "repo_intel": true,
	}
}

func pgSkillDoc(name, body string) map[string]any {
	return map[string]any{
		"name": name, "description": "d", "type": "custom", "source": "manual", "body": body,
		"enabled": true, "evidence_files": nil,
	}
}

func pgCaseDoc(name, kind, owner string) map[string]any {
	return map[string]any{
		"name": name, "owner_kind": kind, "owner_ref": owner, "input_diff": evDiff(evOther, 10), "input_files": nil,
		"input_meta":      map[string]any{"pr_title": "pg", "pr_description": nil, "repo": nil, "pr_number": nil, "head_sha": nil},
		"expected_output": map[string]any{"kind": "must_find", "file": evOther, "start_line": 1, "end_line": 2, "severity": nil, "category": nil, "title": nil},
		"notes":           nil,
	}
}

// pgBundle is a plugin of 2 agents, 3 skills, 3 eval cases (one per agent, one on gate-skill) and 2
// learnings.
func pgBundle(name string) string {
	doc := map[string]any{
		"manifest": map[string]any{
			"name": name, "version": "1.0.0", "format": "devdigest-plugin/v1", "kind": "plugin",
			"exported_at": "2026-10-26T10:00:00.000Z", "description": nil,
			"counts": map[string]any{"agents": 2, "skills": 3, "eval_cases": 3, "conventions": 0, "learnings": 2},
		},
		"agents": []any{pgAgentDoc(pgAgentName, pgSkillName), pgAgentDoc(pgAgent2Name)},
		"skills": []any{pgSkillDoc(pgSkillName, pgPluginBody), pgSkillDoc("gate-skill-two", "second"), pgSkillDoc("gate-skill-three", "third")},
		"eval_cases": []any{
			pgCaseDoc("case one", "agent", pgAgentName), pgCaseDoc("case two", "agent", pgAgent2Name), pgCaseDoc("case three", "skill", pgSkillName),
		},
		"conventions": []any{},
		"learnings": []any{
			map[string]any{"scope": "global", "kind": "learning", "content": "first", "confidence": 0.5, "repo": nil},
			map[string]any{"scope": "global", "kind": "fact", "content": "second", "confidence": nil, "repo": nil},
		},
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		panic(err)
	}
	return string(raw)
}

// pgInstall installs the default bundle through POST /plugins/import (a JSON body) and returns the
// plugin and its owned rows. The plugin starts disabled (FR2).
func pgInstall(t testing.TB, s *stack) pgFix {
	t.Helper()
	resp := s.do(http.MethodPost, "/plugins/import", pgBundle(pgPlugin))
	if resp.Status != http.StatusCreated {
		t.Fatalf("POST /plugins/import = %d %s, want 201", resp.Status, resp.Body)
	}
	installed, _ := resp.Obj(t)["installed"].(map[string]any)
	f := pgFix{plugin: installed["id"].(string)}
	if installed["enabled"] != false {
		t.Fatalf("installed plugin enabled = %v, want false", installed["enabled"])
	}
	owned := func(table, name string) string {
		return scalar[string](t, s.db.DB, `SELECT id::text FROM `+table+` WHERE plugin_id = ? AND name = ?`, f.plugin, name)
	}
	f.p, f.p2, f.ps = owned("agents", pgAgentName), owned("agents", pgAgent2Name), owned("skills", pgSkillName)
	return f
}

// pgUser adds the user's own agent and skill; the agent links the plugin's skill first, then its own.
func pgUser(t testing.TB, s *stack, f pgFix) pgFix {
	t.Helper()
	f.us = newSkill(t, s, pgUserSkill, pgUserBody)
	f.u = newAgent(t, s, map[string]any{"name": "Pg User Agent"})
	if resp := s.post("/agents/"+f.u+"/skills", map[string]any{"skill_ids": []string{f.ps, f.us}}); resp.Status != http.StatusOK {
		t.Fatalf("link skills of the user agent = %d %s, want 200", resp.Status, resp.Body)
	}
	return f
}

// pgQuiesceSeed disables the seeded agents so that an all-agents review starts only what the test made.
func pgQuiesceSeed(t testing.TB, s *stack) {
	t.Helper()
	if err := s.db.DB.Exec(`UPDATE agents SET enabled = false`).Error; err != nil {
		t.Fatal(err)
	}
}

// pgSetEnabled enables or disables the plugin through PATCH /plugins/:id.
func pgSetEnabled(t testing.TB, s *stack, plugin string, enabled bool) {
	t.Helper()
	resp := s.do(http.MethodPatch, "/plugins/"+plugin, map[string]any{"enabled": enabled})
	if resp.Status != http.StatusOK {
		t.Fatalf("PATCH /plugins/%s enabled=%v = %d %s, want 200", plugin, enabled, resp.Status, resp.Body)
	}
}

// pgDisabledMsg is the 409 message of an agent or skill of a disabled plugin.
func pgDisabledMsg(kind, name string) string {
	return kind + ` "` + name + `" belongs to the disabled plugin "` + pgPlugin + `"`
}

// pgRefused expects the 409 plugin_disabled with that exact message.
func pgRefused(t testing.TB, what string, r response, kind, name string) {
	t.Helper()
	evExpect(t, what, r, http.StatusConflict, "plugin_disabled", pgDisabledMsg(kind, name))
}

// pgRunCounts are the numbers of review runs and multi-agent runs (the seed owns some).
func pgRunCounts(t testing.TB, s *stack) (runs, multi int) {
	t.Helper()
	return count(t, s.db.DB, `SELECT count(*) FROM agent_runs`), count(t, s.db.DB, `SELECT count(*) FROM multi_agent_runs`)
}

// pgNoNewRuns fails if the number of review runs or multi-agent runs differs from the baseline.
func pgNoNewRuns(t testing.TB, s *stack, what string, runs, multi int) {
	t.Helper()
	if gotRuns, gotMulti := pgRunCounts(t, s); gotRuns != runs || gotMulti != multi {
		t.Errorf("after %s: agent runs %d, multi-agent runs %d, want %d and %d (no run created)", what, gotRuns, gotMulti, runs, multi)
	}
}

// pgRunByAgent maps each run id to the agent id it belongs to.
func pgRunByAgent(t testing.TB, s *stack, runs []string) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, run := range runs {
		out[scalar[string](t, s.db.DB, `SELECT coalesce(agent_id::text, '') FROM agent_runs WHERE id = ?`, run)] = run
	}
	return out
}

// pgRunSkills are the names of the skills a review run recorded, in their run order.
func pgRunSkills(t testing.TB, s *stack, run string) []string {
	t.Helper()
	var names []string
	err := s.db.DB.Raw(`SELECT sk.name FROM agent_run_skills r JOIN skills sk ON sk.id = r.skill_id WHERE r.run_id = ? ORDER BY r."order"`, run).Scan(&names).Error
	if err != nil {
		t.Fatal(err)
	}
	return names
}

// pgPromptsWith is the number of model calls whose messages hold text.
func pgPromptsWith(ad *scripted, text string) int {
	n := 0
	for i := 0; i < ad.calls(); i++ {
		for _, m := range ad.attempt(i).Messages {
			if strings.Contains(m.Content, text) {
				n++
				break
			}
		}
	}
	return n
}

// pgSorted returns a sorted copy.
func pgSorted(v []string) []string {
	out := slices.Clone(v)
	slices.Sort(out)
	return out
}

// pgConfigSkills are the skill names frozen in the configuration of an eval suite run.
func pgConfigSkills(t testing.TB, s *stack, run string) []string {
	t.Helper()
	var config struct {
		Skills []struct{ Name string } `json:"skills"`
	}
	if err := json.Unmarshal([]byte(scalar[string](t, s.db.DB, `SELECT config::text FROM eval_suite_runs WHERE id = ?`, run)), &config); err != nil {
		t.Fatal(err)
	}
	names := []string{}
	for _, sk := range config.Skills {
		names = append(names, sk.Name)
	}
	return names
}

// pgSuiteRuns is the number of eval suite runs.
func pgSuiteRuns(t testing.TB, s *stack) int {
	t.Helper()
	return count(t, s.db.DB, `SELECT count(*) FROM eval_suite_runs`)
}

// pgStartedID decodes the id of a started eval run.
func pgStartedID(t testing.TB, r response) string {
	t.Helper()
	if r.Status != http.StatusAccepted {
		t.Fatalf("eval run start = %d %s, want 202", r.Status, r.Body)
	}
	return r.Obj(t)["id"].(string)
}

// ---- entry 1: review start, all:true (FR13a, AC13, EC8) ----

// With the plugin disabled, an all-agents review starts only the user's agent and that run leaves
// the plugin's skill out (the run's skills and its prompt); enabled, the plugin's agents run and
// the skill is back.
func TestPluginGating_ReviewAllSkipsPluginAgentsAndSkills(t *testing.T) { // entry 1, AC13, EC8, FR13a/b
	ad := mockScript()
	s := startStack(t, spec{llmAdapters: ad.forAll()})
	pgQuiesceSeed(t, s)
	f := pgUser(t, s, pgInstall(t, s))
	pr := pr482(t, s)

	runs, multi := startOK(t, s, pr, map[string]any{"all": true})
	if multi != nil {
		t.Errorf("disabled plugin: multi_agent_run_id = %v, want null", multi)
	}
	byAgent := pgRunByAgent(t, s, runs)
	if len(runs) != 1 || byAgent[f.u] == "" {
		t.Fatalf("disabled plugin: all:true started agents %v, want only the user agent %s", byAgent, f.u)
	}
	waitStatus(t, s, runs[0], "done")
	if diff := cmp.Diff([]string{pgUserSkill}, pgRunSkills(t, s, runs[0])); diff != "" {
		t.Errorf("run skills of the user agent while the plugin is disabled (-want +got):\n%s", diff)
	}
	if n := pgPromptsWith(ad, pgPluginBody); n != 0 {
		t.Errorf("model calls carrying the plugin skill body = %d, want 0", n)
	}
	if n := pgPromptsWith(ad, pgUserBody); n == 0 {
		t.Errorf("model calls carrying the user skill body = 0, want at least 1 (the user's own skill stays)")
	}

	pgSetEnabled(t, s, f.plugin, true)
	runs, multi = startOK(t, s, pr, map[string]any{"all": true})
	if multi != nil {
		t.Errorf("enabled plugin: multi_agent_run_id = %v, want null (all:true creates no parent run)", multi)
	}
	byAgent = pgRunByAgent(t, s, runs)
	for _, a := range []string{f.u, f.p, f.p2} {
		if byAgent[a] == "" {
			t.Errorf("enabled plugin: no run for agent %s in %v", a, byAgent)
		}
	}
	if len(runs) != 3 {
		t.Errorf("enabled plugin: all:true started %d runs, want 3 (user agent + 2 plugin agents)", len(runs))
	}
	for _, run := range runs {
		waitStatus(t, s, run, "done")
	}
	if diff := cmp.Diff([]string{pgSkillName, pgUserSkill}, pgSorted(pgRunSkills(t, s, byAgent[f.u]))); diff != "" {
		t.Errorf("run skills of the user agent once the plugin is enabled (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff([]string{pgSkillName}, pgRunSkills(t, s, byAgent[f.p])); diff != "" {
		t.Errorf("run skills of the plugin's agent (-want +got):\n%s", diff)
	}
	if n := pgPromptsWith(ad, pgPluginBody); n == 0 {
		t.Errorf("model calls carrying the plugin skill body after enabling = 0, want at least 1")
	}
}

// ---- entry 2: review start, named agents (FR13a, EC9) ----

func TestPluginGating_ReviewNamedAgentsRefused(t *testing.T) { // entry 2, AC13, EC9, FR13a
	ad := mockScript()
	s := startStack(t, spec{llmAdapters: ad.forAll()})
	f := pgUser(t, s, pgInstall(t, s))
	pr := pr482(t, s)
	baseRuns, baseMulti := pgRunCounts(t, s)

	for _, tc := range []struct {
		name string
		body map[string]any
		want string // the agent the message names
	}{
		{"agentId", map[string]any{"agentId": f.p}, pgAgentName},
		{"agentIds with one agent", map[string]any{"agentIds": []string{f.p}}, pgAgentName},
		{"agentIds mixing an enabled agent and a plugin agent (EC9)", map[string]any{"agentIds": []string{f.u, f.p}}, pgAgentName},
		{"multi-agent batch of two plugin agents names the first in request order", map[string]any{"agentIds": []string{f.p2, f.p}}, pgAgent2Name},
		{"batch of three, the plugin agent last", map[string]any{"agentIds": []string{f.u, f.u, f.p2}}, pgAgent2Name},
	} {
		pgRefused(t, tc.name, startReview(t, s, pr, tc.body), "Agent", tc.want)
		pgNoNewRuns(t, s, tc.name, baseRuns, baseMulti)
	}
	if r := startReview(t, s, pr, map[string]any{"agentIds": []string{f.p, unknownID}}); r.Status != http.StatusNotFound {
		t.Errorf("a plugin agent with an unknown agent = %d %s, want the agent 404 first", r.Status, r.Body)
	}
	pgNoNewRuns(t, s, "the 404", baseRuns, baseMulti)
	if n := ad.calls(); n != 0 {
		t.Errorf("model calls after the refusals = %d, want 0", n)
	}

	// The user's agent alone is not affected, enabled plugin or not.
	runs, _ := startOK(t, s, pr, map[string]any{"agentId": f.u})
	waitStatus(t, s, runs[0], "done")

	pgSetEnabled(t, s, f.plugin, true)
	runs, multi := startOK(t, s, pr, map[string]any{"agentIds": []string{f.u, f.p}})
	if len(runs) != 2 || multi == nil {
		t.Errorf("enabled plugin: the mixed list started %d runs, multi %v, want 2 runs under a parent", len(runs), multi)
	}
	one, _ := startOK(t, s, pr, map[string]any{"agentId": f.p})
	for _, run := range append(runs, one...) {
		waitStatus(t, s, run, "done")
	}
}

// ---- entry 3: automatic reviews (FR13a, O3) ----

func TestPluginGating_AutoReviewsSkipPluginAgents(t *testing.T) { // entry 3, AC13, FR13a
	h := arStart(t, arOpts{token: true})
	f := pgInstall(t, h.s)
	u := h.agent("Pg Auto User")
	h.setInterval()
	h.setAuto(map[string]any{"enabled": true, "agent_ids": []string{u, f.p, f.p2}})
	h.advance(2 * time.Hour)
	after := h.t0.Add(30 * time.Minute)

	h.gh.put("r", arPull{number: 1, head: arHead(1, 1), created: after, updated: h.clock.Now().Add(-10 * time.Minute)})
	h.addRepo("r")
	h.waitRecords(1)
	h.requireRecords(arWant(1, 1, "new_pr", "started", 1))
	h.runsDone()
	agentsOf := func(number int) []string {
		var ids []string
		if err := h.db.Raw(`SELECT agent_id::text FROM agent_runs WHERE pr_id = ?`, h.pullID(number)).Scan(&ids).Error; err != nil {
			t.Fatal(err)
		}
		return pgSorted(ids)
	}
	if diff := cmp.Diff([]string{u}, agentsOf(1)); diff != "" {
		t.Errorf("agents of the auto runs on PR 1 while the plugin is disabled (-want +got):\n%s", diff)
	}

	pgSetEnabled(t, h.s, f.plugin, true)
	h.gh.put("r", arPull{number: 2, head: arHead(2, 1), created: after.Add(time.Minute), updated: h.clock.Now()})
	h.cycle(time.Minute)
	h.waitRecords(2)
	h.requireRecords(arWant(1, 1, "new_pr", "started", 1), arWant(2, 1, "new_pr", "started", 3))
	h.runsDone()
	if diff := cmp.Diff(pgSorted([]string{u, f.p, f.p2}), agentsOf(2)); diff != "" {
		t.Errorf("agents of the auto runs on PR 2 once the plugin is enabled (-want +got):\n%s", diff)
	}
}

// ---- entry 4: agent eval start (FR13c) ----

func TestPluginGating_AgentEvalStartRefused(t *testing.T) { // entry 4, AC13, FR13c
	s := startStack(t, spec{llmAdapters: mockScript().forAll()})
	f := pgInstall(t, s)

	pgRefused(t, "eval start of a plugin agent", evStart(s, f.p), "Agent", pgAgentName)
	pgRefused(t, "eval start of the other plugin agent", evStart(s, f.p2), "Agent", pgAgent2Name)
	evExpect(t, "unknown agent keeps its 404", evStart(s, unknownID), http.StatusNotFound, "not_found", "Agent not found")
	if n := pgSuiteRuns(t, s); n != 0 {
		t.Errorf("eval suite runs after the refusals = %d, want 0", n)
	}

	pgSetEnabled(t, s, f.plugin, true)
	evAwait(t, s, pgStartedID(t, evStart(s, f.p)))
}

// ---- entry 5: skill eval start (FR13c, O2) ----

func TestPluginGating_SkillEvalStartRefused(t *testing.T) { // entry 5, AC13, FR13c
	s := startStack(t, spec{llmAdapters: mockScript().forAll()})
	f := pgUser(t, s, pgInstall(t, s))
	evSuiteCases(t, s, "skill", f.us, 1)
	start := func(skill, host string) response {
		return s.post("/skills/"+skill+"/eval-runs", map[string]any{"agent_id": host})
	}

	pgRefused(t, "the plugin's skill on the user's agent", start(f.ps, f.u), "Skill", pgSkillName)
	pgRefused(t, "the plugin's skill on a plugin agent names the skill first", start(f.ps, f.p), "Skill", pgSkillName)
	pgRefused(t, "the user's skill on a plugin host agent", start(f.us, f.p), "Agent", pgAgentName)
	if n := pgSuiteRuns(t, s); n != 0 {
		t.Errorf("eval suite runs after the refusals = %d, want 0", n)
	}
	// Control: the user's skill on the user's agent is not gated.
	evAwait(t, s, pgStartedID(t, start(f.us, f.u)))

	pgSetEnabled(t, s, f.plugin, true)
	evAwait(t, s, pgStartedID(t, start(f.ps, f.u)))
	evAwait(t, s, pgStartedID(t, start(f.us, f.p)))
}

// ---- entry 6: eval run-all (FR13c) ----

func TestPluginGating_EvalRunAllOmitsPluginAgents(t *testing.T) { // entry 6, AC13, FR13c
	s := startStack(t, spec{llmAdapters: mockScript().forAll()})
	f := pgUser(t, s, pgInstall(t, s))
	evSuiteCases(t, s, "agent", f.u, 1)

	runAll := func() (started, skipped []string, runs []string) {
		t.Helper()
		resp := s.post("/eval-runs/run-all", nil)
		if resp.Status != http.StatusAccepted {
			t.Fatalf("POST /eval-runs/run-all = %d %s, want 202", resp.Status, resp.Body)
		}
		var got struct {
			Started []struct {
				ID      string `json:"id"`
				AgentID string `json:"agent_id"`
			}
			Skipped []struct {
				AgentID string `json:"agent_id"`
			}
		}
		if err := json.Unmarshal(resp.Body, &got); err != nil {
			t.Fatal(err)
		}
		for _, st := range got.Started {
			started, runs = append(started, st.AgentID), append(runs, st.ID)
		}
		for _, sk := range got.Skipped {
			skipped = append(skipped, sk.AgentID)
		}
		return started, skipped, runs
	}

	started, skipped, runs := runAll()
	if !slices.Contains(started, f.u) {
		t.Errorf("disabled plugin: run-all started %v, want the user agent %s among them", started, f.u)
	}
	for _, a := range []string{f.p, f.p2} {
		if slices.Contains(started, a) || slices.Contains(skipped, a) {
			t.Errorf("disabled plugin: run-all started %v skipped %v, want plugin agent %s in neither (omitted)", started, skipped, a)
		}
	}
	for _, run := range runs {
		evAwait(t, s, run)
	}

	pgSetEnabled(t, s, f.plugin, true)
	started, _, runs = runAll()
	for _, a := range []string{f.u, f.p, f.p2} {
		if !slices.Contains(started, a) {
			t.Errorf("enabled plugin: run-all started %v, want agent %s", started, a)
		}
	}
	for _, run := range runs {
		evAwait(t, s, run)
	}
}

// ---- EC8: an agent eval run leaves the plugin's skills out ----

func TestPluginGating_AgentEvalRunLeavesOutPluginSkills(t *testing.T) { // EC8, FR13b
	s := startStack(t, spec{llmAdapters: mockScript().forAll()})
	f := pgUser(t, s, pgInstall(t, s))
	evSuiteCases(t, s, "agent", f.u, 1)

	run := pgStartedID(t, evStart(s, f.u))
	evAwait(t, s, run)
	if diff := cmp.Diff([]string{pgUserSkill}, pgConfigSkills(t, s, run)); diff != "" {
		t.Errorf("skills frozen in the eval run while the plugin is disabled (-want +got):\n%s", diff)
	}

	pgSetEnabled(t, s, f.plugin, true)
	run = pgStartedID(t, evStart(s, f.u))
	evAwait(t, s, run)
	if diff := cmp.Diff([]string{pgSkillName, pgUserSkill}, pgConfigSkills(t, s, run)); diff != "" {
		t.Errorf("skills frozen in the eval run once the plugin is enabled, link order (-want +got):\n%s", diff)
	}
}

// ---- entry 7: CI export (FR13d) ----

func TestPluginGating_CIExportRefusedWithoutGitHubCall(t *testing.T) { // entry 7, AC13, EC8, FR13d
	s, _ := ciStack(t, ciSpec())
	f := pgUser(t, s, pgInstall(t, s))
	skillPaths := func(out ciExportResult) []string {
		var paths []string
		for _, file := range out.Files {
			if strings.HasPrefix(file.Path, ".devdigest/skills/") {
				paths = append(paths, strings.TrimSuffix(strings.TrimPrefix(file.Path, ".devdigest/skills/"), ".md"))
			}
		}
		return paths
	}

	for _, action := range []string{"open_pr", "files"} {
		resp := s.post("/agents/"+f.p+"/export-ci", ciExportBody(ciRepoFull, action, nil))
		pgRefused(t, "CI export "+action+" of a plugin agent", resp, "Agent", pgAgentName)
	}
	if reqs := s.gh.requests(); len(reqs) != 0 {
		t.Errorf("GitHub requests after the refusals = %v, want none", ciSeq(reqs))
	}
	if n := count(t, s.db.DB, `SELECT count(*) FROM ci_installations`); n != 0 {
		t.Errorf("ci_installations after the refusals = %d, want 0", n)
	}
	// EC8/FR13d: the user's agent exports without the plugin's skill.
	out := ciExport(t, s, f.u, ciExportBody(ciRepoFull, "files", nil), http.StatusOK)
	if diff := cmp.Diff([]string{pgUserSkill}, skillPaths(out)); diff != "" {
		t.Errorf("skill files of the user agent's export while the plugin is disabled (-want +got):\n%s", diff)
	}

	pgSetEnabled(t, s, f.plugin, true)
	out = ciExport(t, s, f.p, ciExportBody(ciRepoFull, "files", nil), http.StatusOK)
	if diff := cmp.Diff([]string{pgSkillName}, skillPaths(out)); diff != "" {
		t.Errorf("skill files of the plugin agent's export once enabled (-want +got):\n%s", diff)
	}
	out = ciExport(t, s, f.u, ciExportBody(ciRepoFull, "files", nil), http.StatusOK)
	if diff := cmp.Diff([]string{pgSkillName, pgUserSkill}, skillPaths(out)); diff != "" {
		t.Errorf("skill files of the user agent's export once enabled (-want +got):\n%s", diff)
	}
}

// ---- AC16, EC8: remove ----

func TestPluginGating_RemoveKeepsUserAgentWithoutTheLink(t *testing.T) { // AC16, EC8, FR12
	s := startStack(t, spec{})
	f := pgUser(t, s, pgInstall(t, s))
	ciInstall(t, s, f.p, ciRepoFull, "gate-agent", 1, time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC))
	// Counts of the plugin row as GET /plugins shows them.
	listCounts := func() map[string]any {
		t.Helper()
		var plugins []map[string]any
		for _, p := range s.get("/plugins").Obj(t)["plugins"].([]any) {
			plugins = append(plugins, p.(map[string]any))
		}
		if len(plugins) != 1 {
			t.Fatalf("GET /plugins lists %d plugins, want 1", len(plugins))
		}
		return plugins[0]["counts"].(map[string]any)
	}
	wantCounts := map[string]any{"agents": float64(2), "skills": float64(3), "eval_cases": float64(3), "learnings": float64(2)}
	if diff := cmp.Diff(wantCounts, listCounts()); diff != "" {
		t.Fatalf("plugin counts before the removal (-want +got):\n%s", diff)
	}

	wrong := s.do(http.MethodDelete, "/plugins/"+f.plugin, map[string]any{"confirm": "Gate-Plugin"})
	evExpect(t, "wrong confirmation", wrong, http.StatusUnprocessableEntity, "confirmation_mismatch", "")
	if diff := cmp.Diff(wantCounts, listCounts()); diff != "" {
		t.Errorf("plugin counts after the refused removal (-want +got):\n%s", diff)
	}
	if n := count(t, s.db.DB, `SELECT count(*) FROM ci_installations`); n != 1 {
		t.Errorf("ci_installations after the refused removal = %d, want 1", n)
	}

	resp := s.do(http.MethodDelete, "/plugins/"+f.plugin, map[string]any{"confirm": pgPlugin})
	if resp.Status != http.StatusOK {
		t.Fatalf("DELETE /plugins/:id = %d %s, want 200", resp.Status, resp.Body)
	}
	wantRemoved := map[string]any{"agents": float64(2), "skills": float64(3), "eval_cases": float64(3), "learnings": float64(2), "ci_installations": float64(1)}
	if diff := cmp.Diff(wantRemoved, resp.Obj(t)["removed"]); diff != "" {
		t.Errorf("removed counts (-want +got):\n%s", diff)
	}

	for what, q := range map[string]string{
		"plugin rows":               `SELECT count(*) FROM installed_plugins`,
		"agents with a plugin":      `SELECT count(*) FROM agents WHERE plugin_id IS NOT NULL`,
		"skills with a plugin":      `SELECT count(*) FROM skills WHERE plugin_id IS NOT NULL`,
		"learnings with a plugin":   `SELECT count(*) FROM memory WHERE plugin_id IS NOT NULL`,
		"eval cases of the plugin":  `SELECT count(*) FROM eval_cases WHERE name LIKE 'case %'`,
		"ci installations":          `SELECT count(*) FROM ci_installations`,
		"links to the plugin skill": `SELECT count(*) FROM agent_skills WHERE skill_id = '` + f.ps + `'`,
	} {
		if n := count(t, s.db.DB, q); n != 0 {
			t.Errorf("%s after the removal = %d, want 0", what, n)
		}
	}
	// The user's agent stays, with its own skill only.
	agent := s.get("/agents/" + f.u)
	if agent.Status != http.StatusOK || agent.Obj(t)["plugin"] != nil {
		t.Fatalf("GET /agents/:id of the user agent = %d %s, want 200 with plugin null", agent.Status, agent.Body)
	}
	var linked []string
	if err := s.db.DB.Raw(`SELECT skill_id::text FROM agent_skills WHERE agent_id = ?`, f.u).Scan(&linked).Error; err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff([]string{f.us}, linked); diff != "" {
		t.Errorf("skills linked to the user agent after the removal (-want +got):\n%s", diff)
	}
	if got := s.get("/skills/" + f.ps).Status; got != http.StatusNotFound {
		t.Errorf("GET /skills/<plugin skill> after the removal = %d, want 404", got)
	}
	if got := s.get("/skills/" + f.us).Status; got != http.StatusOK {
		t.Errorf("GET /skills/<user skill> after the removal = %d, want 200", got)
	}
}

// ---- FR13 last sentence: a started run finishes when the plugin is disabled ----

func TestPluginGating_DisabledMidRunFinishes(t *testing.T) { // FR13, AC14 (disable half)
	g := newGate()
	t.Cleanup(g.Open)
	s := startStack(t, spec{llmAdapters: g.forAll()})
	f := pgInstall(t, s)
	pgSetEnabled(t, s, f.plugin, true)

	runs, _ := startOK(t, s, pr482(t, s), map[string]any{"agentId": f.p})
	eventually(t, 30*time.Second, "the model call", func() bool { return g.calls() == 1 })
	pgSetEnabled(t, s, f.plugin, false)
	if got := runStatus(t, s, runs[0]); got != "running" {
		t.Fatalf("run status after the plugin was disabled = %q, want still running", got)
	}
	g.Open()
	if got := waitStatus(t, s, runs[0], "done", "failed"); got != "done" {
		t.Fatalf("run status = %q, want done: disabling the plugin must not stop a started run", got)
	}
	if got := scalar[string](t, s.db.DB, `SELECT coalesce(agent_id::text, '') FROM agent_runs WHERE id = ?`, runs[0]); got != f.p {
		t.Errorf("run agent = %q, want the plugin agent %s (still installed)", got, f.p)
	}
	if n := count(t, s.db.DB, `SELECT count(*) FROM reviews WHERE run_id = ? AND agent_id = ?`, runs[0], f.p); n != 1 {
		t.Errorf("reviews of the run = %d, want 1", n)
	}
}

// ---- EC7, AC14: remove while a run of a plugin agent is running ----

func TestPluginGating_RemoveWhileRunningKeepsTheRecords(t *testing.T) { // EC7, AC14
	g := newGate()
	t.Cleanup(g.Open)
	s := startStack(t, spec{llmAdapters: g.forAll()})
	f := pgInstall(t, s)
	pgSetEnabled(t, s, f.plugin, true)

	runs, _ := startOK(t, s, pr482(t, s), map[string]any{"agentId": f.p})
	eventually(t, 30*time.Second, "the model call", func() bool { return g.calls() == 1 })
	pgSetEnabled(t, s, f.plugin, false)
	resp := s.do(http.MethodDelete, "/plugins/"+f.plugin, map[string]any{"confirm": pgPlugin})
	if resp.Status != http.StatusOK {
		t.Fatalf("DELETE /plugins/:id with a run in flight = %d %s, want 200", resp.Status, resp.Body)
	}
	if n := count(t, s.db.DB, `SELECT count(*) FROM agents WHERE id = ?`, f.p); n != 0 {
		t.Fatalf("plugin agent rows after the removal = %d, want 0", n)
	}

	g.Open()
	got := waitStatus(t, s, runs[0], "done", "failed")
	if got != "done" {
		t.Fatalf("run status = %q (error %q), want done: the run finishes although its agent was removed",
			got, scalar[string](t, s.db.DB, `SELECT coalesce(error, '') FROM agent_runs WHERE id = ?`, runs[0]))
	}
	if n := count(t, s.db.DB, `SELECT count(*) FROM agent_runs WHERE id = ? AND agent_id IS NULL`, runs[0]); n != 1 {
		t.Errorf("run records with no agent = %d, want 1 (the record stays, agent NULL)", n)
	}
	if n := count(t, s.db.DB, `SELECT count(*) FROM reviews WHERE run_id = ? AND agent_id IS NULL`, runs[0]); n != 1 {
		t.Errorf("review records of the run with no agent = %d, want 1", n)
	}
	if n := count(t, s.db.DB, `SELECT count(*) FROM run_traces WHERE run_id = ?`, runs[0]); n != 1 {
		t.Errorf("trace records of the run = %d, want 1", n)
	}
}

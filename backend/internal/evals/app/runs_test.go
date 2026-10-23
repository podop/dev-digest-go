package app_test

import (
	"errors"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/evals/app"
	"dev-digest-go/backend/internal/evals/domain"
	"dev-digest-go/backend/internal/platform/apperr"
)

type runEnv struct {
	svc       *app.RunService
	cases     *fakeCases
	runs      *fakeRuns
	snapshots *fakeSnapshots
	launcher  *fakeLauncher
	logs      func() []map[string]any
}

func agentSnap(id string, version int) domain.AgentSnapshot {
	return domain.AgentSnapshot{
		AgentID: id, Version: version, Name: "Agent " + id,
		Config: domain.Config{
			Provider: "openrouter", Model: "m-1", SystemPrompt: "You are a reviewer.", Strategy: "auto",
			Skills: []domain.SkillSnapshot{{ID: "sk-1", Name: "api-safety", Body: "body", Version: 2}},
		},
	}
}

func newRunEnv(t *testing.T) *runEnv {
	t.Helper()
	log, logs := logCapture(t)
	env := &runEnv{
		cases: newFakeCases(), runs: newFakeRuns(), launcher: &fakeLauncher{}, logs: logs,
		snapshots: &fakeSnapshots{
			agents: map[string]domain.AgentSnapshot{"a-1": agentSnap("a-1", 3)},
			skills: map[string]domain.SkillSnapshot{"s-1": {ID: "s-1", Name: "skill one", Description: "d", Body: "b", Version: 5}},
			order:  []string{"a-1"},
		},
	}
	env.cases.addOwner(domain.OwnerAgent, "a-1")
	env.cases.addOwner(domain.OwnerSkill, "s-1")
	env.svc = app.NewRunService(app.RunDeps{
		Cases: env.cases, Runs: env.runs, Snapshots: env.snapshots, Launcher: env.launcher, Now: fixedClock(), Log: log,
	})
	return env
}

func (e *runEnv) addCases(kind domain.OwnerKind, owner string, ids ...string) {
	for _, id := range ids {
		e.cases.add(domain.Case{ID: id, OwnerKind: kind, OwnerID: owner})
	}
}

func TestStartAgent(t *testing.T) {
	env := newRunEnv(t)
	env.addCases(domain.OwnerAgent, "a-1", "c1", "c2", "c3")
	run, err := env.svc.StartAgent(t.Context(), ws, "a-1")
	if err != nil {
		t.Fatalf("StartAgent() error = %v", err)
	}
	want := domain.NewRun{
		WorkspaceID: ws, OwnerKind: domain.OwnerAgent, AgentID: "a-1", AgentVersion: 3, AgentName: "Agent a-1",
		Config: agentSnap("a-1", 3).Config, CaseIDs: []string{"c1", "c2", "c3"},
	}
	if len(env.runs.created) != 1 {
		t.Fatalf("created runs = %d, want 1", len(env.runs.created))
	}
	if diff := cmp.Diff(want, env.runs.created[0]); diff != "" {
		t.Errorf("CreateRun input mismatch (-want +got):\n%s", diff)
	}
	if run.Status != domain.RunRunning || run.CasesTotal != 3 {
		t.Errorf("StartAgent() = status %q total %d, want running/3", run.Status, run.CasesTotal)
	}
	if len(env.launcher.runs) != 1 || env.launcher.runs[0].ID != run.ID {
		t.Errorf("launched = %v, want the returned run", env.launcher.runs)
	}
}

func TestStartAgent_Errors(t *testing.T) {
	t.Run("unknown agent", func(t *testing.T) {
		env := newRunEnv(t)
		_, err := env.svc.StartAgent(t.Context(), ws, "nope")
		wantApp(t, err, apperr.KindNotFound, "not_found", "Agent not found")
	})
	t.Run("no cases", func(t *testing.T) {
		env := newRunEnv(t)
		_, err := env.svc.StartAgent(t.Context(), ws, "a-1")
		wantApp(t, err, apperr.KindValidation, "no_eval_cases", "This agent has no eval cases to run")
		if len(env.runs.created) != 0 || len(env.launcher.runs) != 0 {
			t.Errorf("created/launched = %d/%d, want 0/0", len(env.runs.created), len(env.launcher.runs))
		}
	})
	t.Run("run active", func(t *testing.T) {
		env := newRunEnv(t)
		env.addCases(domain.OwnerAgent, "a-1", "c1")
		if _, err := env.svc.StartAgent(t.Context(), ws, "a-1"); err != nil {
			t.Fatalf("first StartAgent() error = %v", err)
		}
		_, err := env.svc.StartAgent(t.Context(), ws, "a-1")
		wantApp(t, err, apperr.KindConflict, "eval_run_active", "An eval run is already in progress for this agent")
		if len(env.launcher.runs) != 1 {
			t.Errorf("launched = %d, want 1 (the refused start launches nothing)", len(env.launcher.runs))
		}
	})
	t.Run("agent deleted before the insert", func(t *testing.T) {
		env := newRunEnv(t)
		env.addCases(domain.OwnerAgent, "a-1", "c1")
		env.runs.createErr = domain.ErrNotFound
		_, err := env.svc.StartAgent(t.Context(), ws, "a-1")
		wantApp(t, err, apperr.KindNotFound, "not_found", "Agent not found")
		if len(env.launcher.runs) != 0 {
			t.Errorf("launched = %d, want 0", len(env.launcher.runs))
		}
	})
	t.Run("store failure", func(t *testing.T) {
		env := newRunEnv(t)
		env.cases.err = errBoom
		_, err := env.svc.StartAgent(t.Context(), ws, "a-1")
		if !errors.Is(err, errBoom) {
			t.Errorf("StartAgent() error = %v, want it to wrap the store error", err)
		}
	})
}

func TestStartSkill(t *testing.T) {
	env := newRunEnv(t)
	env.addCases(domain.OwnerSkill, "s-1", "c1")
	run, err := env.svc.StartSkill(t.Context(), ws, "s-1", "a-1")
	if err != nil {
		t.Fatalf("StartSkill() error = %v", err)
	}
	if run.OwnerKind != domain.OwnerSkill || run.SkillID == nil || *run.SkillID != "s-1" || run.SkillVersion == nil || *run.SkillVersion != 5 {
		t.Errorf("StartSkill() run = %+v, want a skill run of s-1 v5", run)
	}
	if run.AgentID != "a-1" || run.AgentVersion != 3 || run.AgentName != "Agent a-1" {
		t.Errorf("StartSkill() host = %s v%d %q, want the host agent snapshot", run.AgentID, run.AgentVersion, run.AgentName)
	}
	wantSkills := []domain.SkillSnapshot{{ID: "s-1", Name: "skill one", Description: "d", Body: "b", Version: 5}}
	if diff := cmp.Diff(wantSkills, run.Config.Skills); diff != "" {
		t.Errorf("run skills mismatch (-want +got):\n%s", diff)
	}
	if got := env.snapshots.agents["a-1"].Config.Skills; len(got) != 1 || got[0].ID != "sk-1" {
		t.Errorf("host snapshot skills = %v, want them untouched", got)
	}
	if run.Config.Model != "m-1" || run.Config.SystemPrompt != "You are a reviewer." {
		t.Errorf("run config = %+v, want the host's model and prompt", run.Config)
	}
	if len(env.launcher.runs) != 1 {
		t.Errorf("launched = %d, want 1", len(env.launcher.runs))
	}
}

func TestStartSkill_Errors(t *testing.T) {
	t.Run("unknown skill", func(t *testing.T) {
		env := newRunEnv(t)
		_, err := env.svc.StartSkill(t.Context(), ws, "nope", "a-1")
		wantApp(t, err, apperr.KindNotFound, "not_found", "Skill not found")
	})
	t.Run("unknown host agent", func(t *testing.T) {
		env := newRunEnv(t)
		env.addCases(domain.OwnerSkill, "s-1", "c1")
		_, err := env.svc.StartSkill(t.Context(), ws, "s-1", "nope")
		wantApp(t, err, apperr.KindValidation, "host_agent_not_found", "The host agent was not found in this workspace")
	})
	t.Run("no cases", func(t *testing.T) {
		env := newRunEnv(t)
		_, err := env.svc.StartSkill(t.Context(), ws, "s-1", "a-1")
		wantApp(t, err, apperr.KindValidation, "no_eval_cases", "This skill has no eval cases to run")
	})
	t.Run("run active", func(t *testing.T) {
		env := newRunEnv(t)
		env.addCases(domain.OwnerSkill, "s-1", "c1")
		if _, err := env.svc.StartSkill(t.Context(), ws, "s-1", "a-1"); err != nil {
			t.Fatalf("first StartSkill() error = %v", err)
		}
		_, err := env.svc.StartSkill(t.Context(), ws, "s-1", "a-1")
		wantApp(t, err, apperr.KindConflict, "eval_run_active", "An eval run is already in progress for this skill")
	})
	t.Run("owner deleted before the insert", func(t *testing.T) {
		env := newRunEnv(t)
		env.addCases(domain.OwnerSkill, "s-1", "c1")
		env.runs.createErr = domain.ErrNotFound
		_, err := env.svc.StartSkill(t.Context(), ws, "s-1", "a-1")
		wantApp(t, err, apperr.KindNotFound, "not_found", "Skill not found")
	})
	t.Run("a skill run and an agent run of the same host both start", func(t *testing.T) {
		env := newRunEnv(t)
		env.addCases(domain.OwnerSkill, "s-1", "c1")
		env.addCases(domain.OwnerAgent, "a-1", "c2")
		if _, err := env.svc.StartAgent(t.Context(), ws, "a-1"); err != nil {
			t.Fatalf("StartAgent() error = %v", err)
		}
		if _, err := env.svc.StartSkill(t.Context(), ws, "s-1", "a-1"); err != nil {
			t.Errorf("StartSkill() error = %v, want it to start next to the agent's run", err)
		}
	})
}

func TestRunAll(t *testing.T) {
	env := newRunEnv(t)
	for _, id := range []string{"a-2", "a-3", "a-4"} {
		env.snapshots.agents[id] = agentSnap(id, 1)
	}
	env.snapshots.order = []string{"a-1", "a-2", "a-3", "a-4"}
	env.addCases(domain.OwnerAgent, "a-1", "c1")
	env.addCases(domain.OwnerAgent, "a-3", "c3")
	env.addCases(domain.OwnerAgent, "a-4", "c4")
	env.runs.active[ownerKey{domain.OwnerAgent, "a-4"}] = true

	res, err := env.svc.RunAll(t.Context(), ws)
	if err != nil {
		t.Fatalf("RunAll() error = %v", err)
	}
	var started []string
	for _, r := range res.Started {
		started = append(started, r.AgentID)
	}
	if diff := cmp.Diff([]string{"a-1", "a-3"}, started); diff != "" {
		t.Errorf("RunAll() started mismatch (-want +got):\n%s", diff)
	}
	wantSkips := []domain.Skip{{AgentID: "a-2", Reason: domain.SkipNoCases}, {AgentID: "a-4", Reason: domain.SkipRunActive}}
	if diff := cmp.Diff(wantSkips, res.Skipped); diff != "" {
		t.Errorf("RunAll() skipped mismatch (-want +got):\n%s", diff)
	}
	if len(env.launcher.runs) != 2 {
		t.Errorf("launched = %d, want 2", len(env.launcher.runs))
	}
}

func TestRunAll_NoAgents(t *testing.T) {
	env := newRunEnv(t)
	env.snapshots.order = nil
	res, err := env.svc.RunAll(t.Context(), ws)
	if err != nil || res.Started == nil || res.Skipped == nil || len(res.Started)+len(res.Skipped) != 0 {
		t.Errorf("RunAll() = %+v, %v, want empty non-nil lists", res, err)
	}
}

func TestRunAll_VanishedAgentFailsTheCallButKeepsEarlierRuns(t *testing.T) {
	env := newRunEnv(t)
	env.snapshots.agents["a-2"] = agentSnap("a-2", 1)
	env.snapshots.agents["a-3"] = agentSnap("a-3", 1)
	env.snapshots.order = []string{"a-1", "a-2", "a-3"}
	env.snapshots.vanished = map[string]bool{"a-2": true}
	env.addCases(domain.OwnerAgent, "a-1", "c1")
	env.addCases(domain.OwnerAgent, "a-3", "c3")
	_, err := env.svc.RunAll(t.Context(), ws)
	wantApp(t, err, apperr.KindNotFound, "not_found", "Agent not found")
	if len(env.launcher.runs) != 1 || env.launcher.runs[0].AgentID != "a-1" {
		t.Errorf("launched = %v, want only a-1's run (a-3 is never reached)", env.launcher.runs)
	}
}

func TestListAgentRuns(t *testing.T) {
	d7 := epoch.Add(-7 * 24 * time.Hour)
	d90 := epoch.Add(-90 * 24 * time.Hour)
	tests := []struct {
		name   string
		period domain.Period
		since  *time.Time
	}{
		{"no period", domain.PeriodNone, nil},
		{"all", domain.PeriodAll, nil},
		{"7d", domain.Period7d, &d7},
		{"90d", domain.Period90d, &d90},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			env := newRunEnv(t)
			if _, err := env.svc.ListAgentRuns(t.Context(), ws, "a-1", 20, tc.period); err != nil {
				t.Fatalf("ListAgentRuns() error = %v", err)
			}
			want := []finishedQuery{{agent: "a-1", limit: 20, since: tc.since}}
			if diff := cmp.Diff(want, env.runs.queries, cmp.AllowUnexported(finishedQuery{})); diff != "" {
				t.Errorf("store query mismatch (-want +got):\n%s", diff)
			}
		})
	}
	t.Run("unknown agent", func(t *testing.T) {
		env := newRunEnv(t)
		_, err := env.svc.ListAgentRuns(t.Context(), ws, "nope", 20, domain.PeriodNone)
		wantApp(t, err, apperr.KindNotFound, "not_found", "Agent not found")
	})
	t.Run("empty list is not nil", func(t *testing.T) {
		env := newRunEnv(t)
		got, err := env.svc.ListAgentRuns(t.Context(), ws, "a-1", 20, domain.PeriodNone)
		if err != nil || got == nil {
			t.Errorf("ListAgentRuns() = %v, %v, want an empty non-nil list", got, err)
		}
	})
}

func TestListSkillRuns(t *testing.T) {
	env := newRunEnv(t)
	env.runs.skillRuns = []domain.Run{{ID: "r1"}}
	got, err := env.svc.ListSkillRuns(t.Context(), ws, "s-1", 20)
	if err != nil || len(got) != 1 || got[0].ID != "r1" {
		t.Errorf("ListSkillRuns() = %v, %v, want run r1", got, err)
	}
	_, err = env.svc.ListSkillRuns(t.Context(), ws, "nope", 20)
	wantApp(t, err, apperr.KindNotFound, "not_found", "Skill not found")
}

func TestDetail(t *testing.T) {
	env := newRunEnv(t)
	env.runs.runs["r1"] = domain.Run{ID: "r1"}
	env.runs.results["r1"] = []domain.CaseResult{{CaseID: "c1", Status: domain.CasePassed}}
	got, err := env.svc.Detail(t.Context(), ws, "r1")
	if err != nil || got.Run.ID != "r1" || len(got.Results) != 1 {
		t.Errorf("Detail() = %+v, %v, want run r1 with one result", got, err)
	}
	env.runs.runs["r2"] = domain.Run{ID: "r2"}
	got, err = env.svc.Detail(t.Context(), ws, "r2")
	if err != nil || got.Results == nil {
		t.Errorf("Detail(no results) = %+v, %v, want an empty non-nil results list", got, err)
	}
	_, err = env.svc.Detail(t.Context(), ws, "nope")
	wantApp(t, err, apperr.KindNotFound, "not_found", "Eval run not found")
}

func finishedRun(id, agent string, recall float64) domain.Run {
	return domain.Run{
		ID: id, OwnerKind: domain.OwnerAgent, AgentID: agent, Status: domain.RunCompleted,
		Config: domain.Config{Provider: "openrouter", Model: "m-1", SystemPrompt: "p"}, Metrics: domain.Metrics{Recall: &recall},
	}
}

func TestCompare(t *testing.T) {
	env := newRunEnv(t)
	env.runs.runs["ra"] = finishedRun("ra", "a-1", 0.75)
	env.runs.runs["rb"] = finishedRun("rb", "a-1", 0.8)
	env.runs.results["ra"] = []domain.CaseResult{{CaseID: "c1", CaseName: "One", Status: domain.CasePassed}}
	env.runs.results["rb"] = []domain.CaseResult{{CaseID: "c1", CaseName: "One", Status: domain.CaseFailed}}
	got, err := env.svc.Compare(t.Context(), ws, "ra", "rb")
	if err != nil {
		t.Fatalf("Compare() error = %v", err)
	}
	if got.Delta.Recall == nil || *got.Delta.Recall != 0.05 {
		t.Errorf("Compare() recall delta = %v, want 0.05", got.Delta.Recall)
	}
	wantCases := []domain.CompareCase{{CaseID: "c1", CaseName: "One", A: domain.CasePassed, B: domain.CaseFailed, Flipped: true}}
	if diff := cmp.Diff(wantCases, got.Cases); diff != "" {
		t.Errorf("Compare() cases mismatch (-want +got):\n%s", diff)
	}
}

func TestCompare_Errors(t *testing.T) {
	skill := "s-1"
	running := finishedRun("rr", "a-1", 0.5)
	running.Status = domain.RunRunning
	otherAgent := finishedRun("ro", "a-2", 0.5)
	skillRun := finishedRun("rs", "a-1", 0.5)
	skillRun.OwnerKind, skillRun.SkillID = domain.OwnerSkill, &skill
	tests := []struct {
		name    string
		a, b    string
		kind    apperr.Kind
		code    string
		message string
	}{
		{"missing a", "nope", "rb", apperr.KindNotFound, "not_found", "Eval run not found"},
		{"missing b", "ra", "nope", apperr.KindNotFound, "not_found", "Eval run not found"},
		{"owner kinds", "ra", "rs", apperr.KindValidation, "runs_not_comparable", "Runs of different owner kinds cannot be compared"},
		{"agents", "ra", "ro", apperr.KindValidation, "runs_not_comparable", "Runs of different agents cannot be compared"},
		{"running", "ra", "rr", apperr.KindValidation, "runs_not_comparable", "A run that has not finished cannot be compared"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			env := newRunEnv(t)
			for _, r := range []domain.Run{finishedRun("ra", "a-1", 0.5), finishedRun("rb", "a-1", 0.5), running, otherAgent, skillRun} {
				env.runs.runs[r.ID] = r
			}
			_, err := env.svc.Compare(t.Context(), ws, tc.a, tc.b)
			wantApp(t, err, tc.kind, tc.code, tc.message)
		})
	}
}

func TestOverview(t *testing.T) {
	env := newRunEnv(t)
	env.runs.dashboard = []domain.AgentRef{{ID: "a-1", Name: "Alpha", Model: "m-1"}, {ID: "a-2", Name: "Beta", Model: "m-2"}}
	env.cases.counts = map[ownerKey]int{{domain.OwnerAgent, "a-1"}: 4}
	env.runs.finished["a-1"] = []domain.Run{finishedRun("r3", "a-1", 0.9), finishedRun("r2", "a-1", 0.8), finishedRun("r1", "a-1", 0.7)}
	env.runs.recent = []domain.RecentRun{{Run: finishedRun("r3", "a-1", 0.9), AgentName: "Alpha"}}

	got, err := env.svc.Overview(t.Context(), ws)
	if err != nil {
		t.Fatalf("Overview() error = %v", err)
	}
	if len(got.Agents) != 2 {
		t.Fatalf("Overview() agents = %d, want 2", len(got.Agents))
	}
	a1, a2 := got.Agents[0], got.Agents[1]
	if a1.CasesTotal != 4 || a1.Latest == nil || a1.Latest.ID != "r3" || len(a1.Sparkline) != 3 || a1.Sparkline[0].RunID != "r1" {
		t.Errorf("Overview() a-1 = %+v, want 4 cases, latest r3, sparkline r1..r3 oldest first", a1)
	}
	if a2.CasesTotal != 0 || a2.Latest != nil || a2.Sparkline == nil || len(a2.Sparkline) != 0 {
		t.Errorf("Overview() a-2 = %+v, want 0 cases, no latest, an empty sparkline", a2)
	}
	if len(got.RecentRuns) != 1 {
		t.Errorf("Overview() recent = %d, want 1", len(got.RecentRuns))
	}
	var sparkLimit, recentLimit int
	for _, q := range env.runs.queries {
		switch q.agent {
		case "a-1":
			sparkLimit = q.limit
		case "recent":
			recentLimit = q.limit
		}
	}
	if sparkLimit != 10 || recentLimit != 20 {
		t.Errorf("read limits = %d/%d, want 10/20", sparkLimit, recentLimit)
	}
}

func TestOverview_Nothing(t *testing.T) {
	env := newRunEnv(t)
	got, err := env.svc.Overview(t.Context(), ws)
	if err != nil || got.Agents == nil || got.RecentRuns == nil || len(got.Agents)+len(got.RecentRuns) != 0 {
		t.Errorf("Overview() = %+v, %v, want empty non-nil lists", got, err)
	}
}

func recalls(runs []domain.Run) []string {
	out := []string{}
	for _, r := range runs {
		out = append(out, r.ID)
	}
	return out
}

func TestAgentDashboard_NoPeriod(t *testing.T) {
	env := newRunEnv(t)
	env.addCases(domain.OwnerAgent, "a-1", "c1", "c2")
	env.runs.finished["a-1"] = []domain.Run{finishedRun("r3", "a-1", 0.60), finishedRun("r2", "a-1", 0.80), finishedRun("r1", "a-1", 0.9)}
	got, err := env.svc.AgentDashboard(t.Context(), ws, "a-1", domain.PeriodNone)
	if err != nil {
		t.Fatalf("AgentDashboard() error = %v", err)
	}
	if got.CasesTotal != 2 || got.Latest.ID != "r3" || got.Previous.ID != "r2" {
		t.Errorf("AgentDashboard() = total %d latest %v previous %v, want 2/r3/r2", got.CasesTotal, got.Latest, got.Previous)
	}
	if diff := cmp.Diff([]string{"r1", "r2", "r3"}, recalls(got.Trend)); diff != "" {
		t.Errorf("AgentDashboard() trend mismatch (-want +got):\n%s", diff)
	}
	if len(got.Alert) != 1 || got.Alert[0].Metric != domain.MetricRecall || got.Alert[0].Delta != -0.2 {
		t.Errorf("AgentDashboard() alert = %v, want recall -0.2", got.Alert)
	}
	if want := []finishedQuery{{"a-1", 20, nil}}; !cmp.Equal(want, env.runs.queries, cmp.AllowUnexported(finishedQuery{})) {
		t.Errorf("store queries = %v, want one read of the newest 20", env.runs.queries)
	}
}

func TestAgentDashboard_Period(t *testing.T) {
	env := newRunEnv(t)
	env.runs.finished["a-1"] = []domain.Run{finishedRun("r2", "a-1", 0.9), finishedRun("r1", "a-1", 0.9)}
	got, err := env.svc.AgentDashboard(t.Context(), ws, "a-1", domain.Period30d)
	if err != nil {
		t.Fatalf("AgentDashboard() error = %v", err)
	}
	if got.Latest.ID != "r2" || got.Previous.ID != "r1" || got.Alert != nil {
		t.Errorf("AgentDashboard() = latest %v previous %v alert %v, want r2/r1/none", got.Latest, got.Previous, got.Alert)
	}
	since := epoch.Add(-30 * 24 * time.Hour)
	want := []finishedQuery{{"a-1", 2, nil}, {"a-1", 100, &since}}
	if diff := cmp.Diff(want, env.runs.queries, cmp.AllowUnexported(finishedQuery{})); diff != "" {
		t.Errorf("store queries mismatch (-want +got):\n%s", diff)
	}
}

func TestAgentDashboard_PeriodAllHasNoBound(t *testing.T) {
	env := newRunEnv(t)
	if _, err := env.svc.AgentDashboard(t.Context(), ws, "a-1", domain.PeriodAll); err != nil {
		t.Fatalf("AgentDashboard() error = %v", err)
	}
	want := []finishedQuery{{"a-1", 2, nil}, {"a-1", 100, nil}}
	if diff := cmp.Diff(want, env.runs.queries, cmp.AllowUnexported(finishedQuery{})); diff != "" {
		t.Errorf("store queries mismatch (-want +got):\n%s", diff)
	}
}

func TestAgentDashboard_EmptyAndUnknown(t *testing.T) {
	env := newRunEnv(t)
	got, err := env.svc.AgentDashboard(t.Context(), ws, "a-1", domain.PeriodNone)
	if err != nil || got.Latest != nil || got.Previous != nil || got.Alert != nil || got.Trend == nil || len(got.Trend) != 0 {
		t.Errorf("AgentDashboard(no runs) = %+v, %v, want no latest/previous/alert and an empty trend", got, err)
	}
	_, err = env.svc.AgentDashboard(t.Context(), ws, "nope", domain.PeriodNone)
	wantApp(t, err, apperr.KindNotFound, "not_found", "Agent not found")
}

func TestFailInterrupted(t *testing.T) {
	env := newRunEnv(t)
	env.runs.reaped = 2
	n, err := env.svc.FailInterrupted(t.Context())
	if err != nil || n != 2 {
		t.Fatalf("FailInterrupted() = %d, %v, want 2, nil", n, err)
	}
	if logs := env.logs(); len(logs) != 1 || logs[0]["msg"] != "eval runs reaped" || logs[0]["count"] != float64(2) {
		t.Errorf("log lines = %v, want one 'eval runs reaped' with count 2", logs)
	}
	env.runs.reapErr = errBoom
	if _, err := env.svc.FailInterrupted(t.Context()); !errors.Is(err, errBoom) {
		t.Errorf("FailInterrupted() error = %v, want it to wrap the store error", err)
	}
}

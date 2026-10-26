package postgres_test

import (
	"errors"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	"dev-digest-go/backend/internal/evals/domain"
)

func TestNonUUIDIdsAreNotFoundNotACastError(t *testing.T) {
	e := newEnv(t)
	ctx := t.Context()
	if _, err := e.store.GetRun(ctx, e.ws, "not-a-uuid"); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("GetRun(non-UUID) error = %v; want ErrNotFound", err)
	}
	if _, err := e.store.GetCase(ctx, e.ws, "not-a-uuid"); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("GetCase(non-UUID) error = %v; want ErrNotFound", err)
	}
	if _, err := e.store.GetCaseView(ctx, e.ws, "not-a-uuid"); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("GetCaseView(non-UUID) error = %v; want ErrNotFound", err)
	}
	if res, err := e.store.RunResults(ctx, e.ws, "not-a-uuid"); err != nil || len(res) != 0 {
		t.Errorf("RunResults(non-UUID) = %v, %v; want empty, nil", res, err)
	}
	if runs, err := e.store.ListAgentRuns(ctx, e.ws, "not-a-uuid", 5, nil); err != nil || len(runs) != 0 {
		t.Errorf("ListAgentRuns(non-UUID) = %v, %v; want empty, nil", runs, err)
	}
	if ok, err := e.store.FinishRun(ctx, e.ws, "not-a-uuid", domain.RunFinish{Status: domain.RunFailed}); err != nil || ok {
		t.Errorf("FinishRun(non-UUID) = %v, %v; want false, nil", ok, err)
	}
	if err := e.store.DropRunCase(ctx, e.ws, "not-a-uuid"); err != nil {
		t.Errorf("DropRunCase(non-UUID) error = %v; want nil", err)
	}
	if err := e.store.RecordResult(ctx, e.ws, domain.NewResult{RunID: "not-a-uuid", CaseID: noSuchID, Status: domain.CasePassed}); !errors.Is(err, domain.ErrRowGone) {
		t.Errorf("RecordResult(non-UUID run) error = %v; want ErrRowGone", err)
	}
}

func TestCreateRun_RoundTripsTheFrozenConfig(t *testing.T) {
	e := newEnv(t)
	agent := e.agent(t, e.ws, "a")
	skill := e.skill(t, e.ws, "s", true)
	c := e.newCase(t, e.ws, domain.OwnerAgent, agent, "c")
	cfg := domain.Config{
		Provider: "openrouter", Model: "m/x", SystemPrompt: "line\nsys \"q\" <b>  ", Strategy: "map-reduce",
		Skills: []domain.SkillSnapshot{{ID: skill, Name: "s", Description: "d", Body: "b", Version: 4}},
	}
	got, err := e.store.CreateRun(t.Context(), domain.NewRun{
		WorkspaceID: e.ws, OwnerKind: domain.OwnerSkill, SkillID: &skill, SkillVersion: ip(2),
		AgentID: agent, AgentVersion: 5, AgentName: "frozen name", Config: cfg, CaseIDs: []string{c.ID, noSuchID},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := domain.Run{
		ID: got.ID, WorkspaceID: e.ws, OwnerKind: domain.OwnerSkill, SkillID: &skill, SkillVersion: ip(2),
		AgentID: agent, AgentVersion: 5, AgentName: "frozen name", Config: cfg, CaseIDs: []string{c.ID, noSuchID},
		Status: domain.RunRunning, StartedAt: got.StartedAt, CasesTotal: 2,
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("CreateRun() mismatch (-want +got):\n%s", diff)
	}
	if got.ID == "" || got.StartedAt.IsZero() {
		t.Errorf("CreateRun() = %+v; want an id and a start time", got)
	}
	read, err := e.store.GetRun(t.Context(), e.ws, got.ID)
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(got, read, cmpopts.EquateApproxTime(time.Millisecond)); diff != "" {
		t.Errorf("GetRun() differs from the created run, Config included (-created +read):\n%s", diff)
	}
	if _, err := e.store.GetRun(t.Context(), e.workspace(t), got.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("GetRun(other workspace) error = %v; want ErrNotFound", err)
	}
}

func TestCreateRun_HostileConfigIsStorable(t *testing.T) {
	e := newEnv(t)
	agent := e.agent(t, e.ws, "a")
	got, err := e.store.CreateRun(t.Context(), domain.NewRun{
		WorkspaceID: e.ws, OwnerKind: domain.OwnerAgent, AgentID: agent, AgentVersion: 1, AgentName: "n\x00",
		Config: domain.Config{
			Provider: "p", Model: "m", SystemPrompt: "sys\x00tem",
			Skills: []domain.SkillSnapshot{{ID: noSuchID, Name: "s", Body: "bo\x00dy"}},
		},
	})
	if err != nil {
		t.Fatalf("CreateRun(NUL) error = %v; want it stored", err)
	}
	if got.AgentName != "n�" || got.Config.SystemPrompt != "sys�tem" || got.Config.Skills[0].Body != "bo�dy" || got.CaseIDs == nil {
		t.Errorf("CreateRun(NUL) = %+v; want U+0000 replaced by U+FFFD and CaseIDs non-nil", got)
	}
}

func TestCreateRun_OneRunningRunPerOwner(t *testing.T) {
	e := newEnv(t)
	agent, other := e.agent(t, e.ws, "a"), e.agent(t, e.ws, "b")
	skill := e.skill(t, e.ws, "s", true)
	first := e.newRun(t, e.ws, agent)

	_, err := e.store.CreateRun(t.Context(), domain.NewRun{WorkspaceID: e.ws, OwnerKind: domain.OwnerAgent, AgentID: agent, AgentVersion: 3})
	if !errors.Is(err, domain.ErrRunActive) {
		t.Fatalf("CreateRun(second running run of the agent) error = %v; want ErrRunActive", err)
	}
	e.newRun(t, e.ws, other) // another agent is independent
	// A skill run hosted by the busy agent does not collide with the agent's own run.
	skillRun := domain.NewRun{
		WorkspaceID: e.ws, OwnerKind: domain.OwnerSkill, SkillID: &skill, SkillVersion: ip(1), AgentID: agent, AgentVersion: 3,
	}
	if _, err := e.store.CreateRun(t.Context(), skillRun); err != nil {
		t.Fatalf("CreateRun(skill run on the busy host agent) error = %v; want nil", err)
	}
	if _, err := e.store.CreateRun(t.Context(), skillRun); !errors.Is(err, domain.ErrRunActive) {
		t.Errorf("CreateRun(second running run of the skill) error = %v; want ErrRunActive", err)
	}
	e.finish(t, first, domain.RunCompleted)
	if _, err := e.store.CreateRun(t.Context(), domain.NewRun{WorkspaceID: e.ws, OwnerKind: domain.OwnerAgent, AgentID: agent, AgentVersion: 3}); err != nil {
		t.Errorf("CreateRun(after the first finished) error = %v; want nil", err)
	}
	// A deleted owner (foreign key) is not found, not active.
	_, err = e.store.CreateRun(t.Context(), domain.NewRun{WorkspaceID: e.ws, OwnerKind: domain.OwnerAgent, AgentID: noSuchID, AgentVersion: 1})
	if !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("CreateRun(unknown agent) error = %v; want ErrNotFound", err)
	}
}

func TestRecordResult_CountersAndOrder(t *testing.T) {
	e := newEnv(t)
	agent := e.agent(t, e.ws, "a")
	c1 := e.newCase(t, e.ws, domain.OwnerAgent, agent, "c1")
	c2 := e.newCase(t, e.ws, domain.OwnerAgent, agent, "c2")
	c3 := e.newCase(t, e.ws, domain.OwnerAgent, agent, "c3")
	run := e.newRun(t, e.ws, agent, c1.ID, c2.ID, c3.ID)

	findings := []domain.ResultFinding{{File: "a.go", StartLine: 1, EndLine: 2, Severity: sp("CRITICAL"), Title: sp("t\x00"), Match: domain.MatchHit}}
	for _, r := range []domain.NewResult{
		{RunID: run.ID, CaseID: c1.ID, Status: domain.CasePassed, Findings: findings, DroppedCount: 2, HitCount: 1, DurationMS: 40, CostUSD: fp(0.0123456)},
		{RunID: run.ID, CaseID: c2.ID, Status: domain.CaseFailed, UnlabeledCount: 3, DurationMS: 5},
		{RunID: run.ID, CaseID: c3.ID, Status: domain.CaseError, Error: sp("bad\x00")},
	} {
		if err := e.store.RecordResult(t.Context(), e.ws, r); err != nil {
			t.Fatalf("RecordResult(%s): %v", r.Status, err)
		}
	}
	got, err := e.store.GetRun(t.Context(), e.ws, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.CasesFinished != 3 || got.CasesPassed != 1 || got.CasesErrored != 1 || got.CasesTotal != 3 {
		t.Errorf("counters finished/passed/errored/total = %d/%d/%d/%d; want 3/1/1/3", got.CasesFinished, got.CasesPassed, got.CasesErrored, got.CasesTotal)
	}
	if p := e.scalar(t, `SELECT pass::text FROM eval_runs WHERE case_id = ?`, c1.ID); p != "true" {
		t.Errorf("pass of the passed result = %s; want true", p)
	}

	results, err := e.store.RunResults(t.Context(), e.ws, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	wantFindings := []domain.ResultFinding{{File: "a.go", StartLine: 1, EndLine: 2, Severity: sp("CRITICAL"), Title: sp("t�"), Match: domain.MatchHit}}
	want := []domain.CaseResult{
		{CaseID: c1.ID, CaseName: "c1", Expectation: c1.Expectation, Status: domain.CasePassed, Findings: wantFindings, DroppedCount: 2, DurationMS: 40, CostUSD: fp(0.012346)},
		{CaseID: c2.ID, CaseName: "c2", Expectation: c2.Expectation, Status: domain.CaseFailed, Findings: []domain.ResultFinding{}, DurationMS: 5},
		{CaseID: c3.ID, CaseName: "c3", Expectation: c3.Expectation, Status: domain.CaseError, Findings: []domain.ResultFinding{}, Error: sp("bad�")},
	}
	if diff := cmp.Diff(want, results, cmpopts.EquateApprox(0, 1e-9)); diff != "" {
		t.Errorf("RunResults() mismatch (-want +got):\n%s", diff)
	}
	// Results of another workspace's view of the run are not readable.
	if res, err := e.store.RunResults(t.Context(), e.workspace(t), run.ID); err != nil || len(res) != 0 {
		t.Errorf("RunResults(other workspace) = %v, %v; want empty", res, err)
	}
}

func TestRunResults_LegacyRowWithNullStatusAndDuration(t *testing.T) {
	e := newEnv(t)
	agent := e.agent(t, e.ws, "a")
	c := e.newCase(t, e.ws, domain.OwnerAgent, agent, "c")
	run := e.newRun(t, e.ws, agent, c.ID)
	e.exec(t, `INSERT INTO eval_runs (case_id, run_id) VALUES (?, ?)`, c.ID, run.ID)
	res, err := e.store.RunResults(t.Context(), e.ws, run.ID)
	if err != nil || len(res) != 1 {
		t.Fatalf("RunResults() = %v, %v; want one row", res, err)
	}
	if res[0].Status != domain.CaseError || res[0].DurationMS != 0 || res[0].Findings == nil || len(res[0].Findings) != 0 || res[0].CostUSD != nil || res[0].Error != nil {
		t.Errorf("RunResults(NULL status) = %+v; want error status, 0 duration, no findings", res[0])
	}
}

func TestRecordResult_RowGone(t *testing.T) {
	e := newEnv(t)
	agent := e.agent(t, e.ws, "a")
	c1 := e.newCase(t, e.ws, domain.OwnerAgent, agent, "c1")
	c2 := e.newCase(t, e.ws, domain.OwnerAgent, agent, "c2")
	run := e.newRun(t, e.ws, agent, c1.ID, c2.ID)
	assertCounters := func(label string, finished int) {
		t.Helper()
		got, err := e.store.GetRun(t.Context(), e.ws, run.ID)
		if err != nil || got.CasesFinished != finished {
			t.Errorf("%s: cases_finished = %d, %v; want %d", label, got.CasesFinished, err, finished)
		}
	}

	e.exec(t, `DELETE FROM eval_cases WHERE id = ?`, c1.ID)
	err := e.store.RecordResult(t.Context(), e.ws, domain.NewResult{RunID: run.ID, CaseID: c1.ID, Status: domain.CasePassed})
	if !errors.Is(err, domain.ErrRowGone) {
		t.Errorf("RecordResult(deleted case) error = %v; want ErrRowGone", err)
	}
	assertCounters("after the deleted case", 0)

	err = e.store.RecordResult(t.Context(), e.workspace(t), domain.NewResult{RunID: run.ID, CaseID: c2.ID, Status: domain.CasePassed})
	if !errors.Is(err, domain.ErrRowGone) {
		t.Errorf("RecordResult(other workspace) error = %v; want ErrRowGone", err)
	}
	if n := e.scalar(t, `SELECT count(*)::text FROM eval_runs WHERE run_id = ?`, run.ID); n != "0" {
		t.Errorf("results after a refused record = %s; want 0", n)
	}

	if err := e.store.RecordResult(t.Context(), e.ws, domain.NewResult{RunID: run.ID, CaseID: c2.ID, Status: domain.CasePassed}); err != nil {
		t.Fatal(err)
	}
	// A second result of the same case is a plain error, not "row gone".
	err = e.store.RecordResult(t.Context(), e.ws, domain.NewResult{RunID: run.ID, CaseID: c2.ID, Status: domain.CasePassed})
	if err == nil || errors.Is(err, domain.ErrRowGone) {
		t.Errorf("RecordResult(duplicate) error = %v; want a non-ErrRowGone error", err)
	}
	assertCounters("after the duplicate", 1)

	e.exec(t, `DELETE FROM eval_suite_runs WHERE id = ?`, run.ID)
	err = e.store.RecordResult(t.Context(), e.ws, domain.NewResult{RunID: run.ID, CaseID: c2.ID, Status: domain.CasePassed})
	if !errors.Is(err, domain.ErrRowGone) {
		t.Errorf("RecordResult(deleted run) error = %v; want ErrRowGone", err)
	}
}

func TestDropRunCase_NeverBelowZero(t *testing.T) {
	e := newEnv(t)
	agent := e.agent(t, e.ws, "a")
	c := e.newCase(t, e.ws, domain.OwnerAgent, agent, "c")
	run := e.newRun(t, e.ws, agent, c.ID)
	for i, want := range []int{0, 0} {
		if err := e.store.DropRunCase(t.Context(), e.ws, run.ID); err != nil {
			t.Fatal(err)
		}
		if got, _ := e.store.GetRun(t.Context(), e.ws, run.ID); got.CasesTotal != want {
			t.Errorf("cases_total after drop %d = %d; want %d", i+1, got.CasesTotal, want)
		}
	}
	// Another workspace changes nothing.
	run2 := e.newRun(t, e.ws, e.agent(t, e.ws, "b"), c.ID)
	if err := e.store.DropRunCase(t.Context(), e.workspace(t), run2.ID); err != nil {
		t.Fatal(err)
	}
	if got, _ := e.store.GetRun(t.Context(), e.ws, run2.ID); got.CasesTotal != 1 {
		t.Errorf("cases_total after a foreign drop = %d; want 1", got.CasesTotal)
	}
}

func TestFinishRun_OnlyWhileRunning(t *testing.T) {
	e := newEnv(t)
	agent := e.agent(t, e.ws, "a")
	run := e.newRun(t, e.ws, agent)
	at := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	fin := domain.RunFinish{
		Status: domain.RunCompleted, FinishedAt: at, DurationMS: 1234, CasesPassed: 2, CasesErrored: 1,
		Metrics: domain.Metrics{Recall: fp(0.5), Precision: fp(1), CitationAccuracy: nil, UnlabeledFindings: 4, CostUSD: fp(0.25)},
	}
	if ok, err := e.store.FinishRun(t.Context(), e.workspace(t), run.ID, fin); err != nil || ok {
		t.Errorf("FinishRun(other workspace) = %v, %v; want false, nil", ok, err)
	}
	if ok, err := e.store.FinishRun(t.Context(), e.ws, run.ID, fin); err != nil || !ok {
		t.Fatalf("FinishRun() = %v, %v; want true, nil", ok, err)
	}
	got, err := e.store.GetRun(t.Context(), e.ws, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != domain.RunCompleted || got.FinishedAt == nil || !got.FinishedAt.Equal(at) || *got.DurationMS != 1234 ||
		got.CasesPassed != 2 || got.CasesErrored != 1 || got.Metrics.UnlabeledFindings != 4 || *got.Metrics.Recall != 0.5 ||
		*got.Metrics.Precision != 1 || got.Metrics.CitationAccuracy != nil || *got.Metrics.CostUSD != 0.25 || got.Error != nil {
		t.Errorf("GetRun() after FinishRun = %+v; want the final state", got)
	}
	// A finished (or reaped) run is not written again.
	fin.Status, fin.Error = domain.RunFailed, sp("late")
	if ok, err := e.store.FinishRun(t.Context(), e.ws, run.ID, fin); err != nil || ok {
		t.Errorf("FinishRun(second) = %v, %v; want false, nil", ok, err)
	}
	if got, _ := e.store.GetRun(t.Context(), e.ws, run.ID); got.Status != domain.RunCompleted {
		t.Errorf("status after a refused second finish = %s; want completed", got.Status)
	}
}

func TestFinishRun_FailedWithAnError(t *testing.T) {
	e := newEnv(t)
	run := e.newRun(t, e.ws, e.agent(t, e.ws, "a"))
	ok, err := e.store.FinishRun(t.Context(), e.ws, run.ID, domain.RunFinish{
		Status: domain.RunFailed, Error: sp("boom\x00"), FinishedAt: time.Now(), DurationMS: 1,
	})
	if err != nil || !ok {
		t.Fatalf("FinishRun() = %v, %v", ok, err)
	}
	got, _ := e.store.GetRun(t.Context(), e.ws, run.ID)
	if got.Status != domain.RunFailed || got.Error == nil || *got.Error != "boom�" {
		t.Errorf("run after a failed finish = %+v; want failed with the cleaned error", got)
	}
}

func TestFailInterrupted(t *testing.T) {
	e := newEnv(t)
	other := e.workspace(t)
	a1, a2 := e.agent(t, e.ws, "a1"), e.agent(t, other, "a2")
	skill := e.skill(t, e.ws, "s", true)
	running1, running2 := e.newRun(t, e.ws, a1), e.newRun(t, other, a2)
	_, err := e.store.CreateRun(t.Context(), domain.NewRun{WorkspaceID: e.ws, OwnerKind: domain.OwnerSkill, SkillID: &skill, SkillVersion: ip(1), AgentID: a1, AgentVersion: 1})
	if err != nil {
		t.Fatal(err)
	}
	done := e.newRun(t, e.ws, e.agent(t, e.ws, "a3"))
	e.finish(t, done, domain.RunCompleted)

	n, err := e.store.FailInterrupted(t.Context())
	if err != nil || n != 3 {
		t.Fatalf("FailInterrupted() = %d, %v; want 3, nil", n, err)
	}
	for _, r := range []domain.Run{running1, running2} {
		got, err := e.store.GetRun(t.Context(), r.WorkspaceID, r.ID)
		if err != nil || got.Status != domain.RunFailed || got.Error == nil || *got.Error != "interrupted" || got.FinishedAt == nil {
			t.Errorf("run %s after the reap = %+v, %v; want failed/interrupted with finished_at", r.ID, got, err)
		}
	}
	if got, _ := e.store.GetRun(t.Context(), e.ws, done.ID); got.Status != domain.RunCompleted || got.Error != nil {
		t.Errorf("finished run after the reap = %+v; want untouched", got)
	}
	if n, err := e.store.FailInterrupted(t.Context()); err != nil || n != 0 {
		t.Errorf("FailInterrupted(again) = %d, %v; want 0, nil", n, err)
	}
}

func TestRunLists(t *testing.T) {
	e := newEnv(t)
	agent := e.agent(t, e.ws, "a")
	skill := e.skill(t, e.ws, "s", true)
	base := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)

	var runs []domain.Run // oldest first
	for i := range 4 {
		r := e.newRun(t, e.ws, agent)
		e.exec(t, `UPDATE eval_suite_runs SET started_at = ? WHERE id = ?`, base.Add(time.Duration(i)*time.Hour), r.ID)
		if i < 3 {
			status := domain.RunCompleted
			if i == 1 {
				status = domain.RunFailed
			}
			e.finish(t, r, status)
		}
		runs = append(runs, r) // runs[3] stays running
	}
	skillRun, err := e.store.CreateRun(t.Context(), domain.NewRun{
		WorkspaceID: e.ws, OwnerKind: domain.OwnerSkill, SkillID: &skill, SkillVersion: ip(1), AgentID: agent, AgentVersion: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	e.exec(t, `UPDATE eval_suite_runs SET started_at = ? WHERE id = ?`, base.Add(10*time.Hour), skillRun.ID)
	ids := func(rs []domain.Run) []string {
		out := []string{}
		for _, r := range rs {
			out = append(out, r.ID)
		}
		return out
	}

	got, err := e.store.ListAgentRuns(t.Context(), e.ws, agent, 10, nil)
	if want := []string{runs[3].ID, runs[2].ID, runs[1].ID, runs[0].ID}; err != nil || cmp.Diff(want, ids(got)) != "" {
		t.Errorf("ListAgentRuns() = %v, %v; want newest first without the hosted skill run: %v", ids(got), err, want)
	}
	if got[0].Config.Model != "m" {
		t.Errorf("ListAgentRuns()[0].Config.Model = %q; want the frozen config loaded", got[0].Config.Model)
	}
	got, _ = e.store.ListAgentRuns(t.Context(), e.ws, agent, 2, nil)
	if want := []string{runs[3].ID, runs[2].ID}; cmp.Diff(want, ids(got)) != "" {
		t.Errorf("ListAgentRuns(limit 2) = %v; want %v", ids(got), want)
	}
	since := base.Add(2 * time.Hour)
	got, _ = e.store.ListAgentRuns(t.Context(), e.ws, agent, 10, &since)
	if want := []string{runs[3].ID, runs[2].ID}; cmp.Diff(want, ids(got)) != "" {
		t.Errorf("ListAgentRuns(since) = %v; want started_at >= since: %v", ids(got), want)
	}
	got, err = e.store.ListSkillRuns(t.Context(), e.ws, skill, 10)
	if want := []string{skillRun.ID}; err != nil || cmp.Diff(want, ids(got)) != "" {
		t.Errorf("ListSkillRuns() = %v, %v; want %v", ids(got), err, want)
	}
	got, err = e.store.FinishedAgentRuns(t.Context(), e.ws, agent, 10, nil)
	if want := []string{runs[2].ID, runs[1].ID, runs[0].ID}; err != nil || cmp.Diff(want, ids(got)) != "" {
		t.Errorf("FinishedAgentRuns() = %v, %v; want only completed and failed: %v", ids(got), err, want)
	}
	got, _ = e.store.FinishedAgentRuns(t.Context(), e.ws, agent, 10, &since)
	if want := []string{runs[2].ID}; cmp.Diff(want, ids(got)) != "" {
		t.Errorf("FinishedAgentRuns(since) = %v; want %v", ids(got), want)
	}
	if got, _ := e.store.ListAgentRuns(t.Context(), e.workspace(t), agent, 10, nil); len(got) != 0 {
		t.Errorf("ListAgentRuns(other workspace) = %d runs; want 0", len(got))
	}
}

func TestDashboardAgentsAndRecentRuns(t *testing.T) {
	e := newEnv(t)
	withCase, withRun := e.agent(t, e.ws, "b-with-case"), e.agent(t, e.ws, "a-with-run")
	e.agent(t, e.ws, "idle") // no case, no run: not listed
	skillOnly := e.agent(t, e.ws, "c-skill-host")
	e.agent(t, e.workspace(t), "elsewhere")
	skill := e.skill(t, e.ws, "s", true)
	e.newCase(t, e.ws, domain.OwnerAgent, withCase, "c")
	e.newCase(t, e.ws, domain.OwnerSkill, skill, "skill case") // a skill case does not make its host an eval agent
	run := e.newRun(t, e.ws, withRun)
	_, err := e.store.CreateRun(t.Context(), domain.NewRun{
		WorkspaceID: e.ws, OwnerKind: domain.OwnerSkill, SkillID: &skill, SkillVersion: ip(1), AgentID: skillOnly, AgentVersion: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	e.exec(t, `UPDATE agents SET name = 'renamed' WHERE id = ?`, withRun)

	agents, err := e.store.DashboardAgents(t.Context(), e.ws)
	want := []domain.AgentRef{{ID: withCase, Name: "b-with-case", Model: "gpt-x"}, {ID: withRun, Name: "renamed", Model: "gpt-x"}}
	if err != nil || cmp.Diff(want, agents) != "" {
		t.Errorf("DashboardAgents() = %v, %v; want agents with a case or an agent-owned run by name: %v", agents, err, want)
	}

	recent, err := e.store.RecentAgentRuns(t.Context(), e.ws, 20)
	if err != nil || len(recent) != 1 {
		t.Fatalf("RecentAgentRuns() = %d runs, %v; want 1 (skill runs excluded)", len(recent), err)
	}
	if recent[0].Run.ID != run.ID || recent[0].AgentName != "renamed" || recent[0].Run.AgentName != "frozen" || recent[0].Run.Config.Model != "m" {
		t.Errorf("RecentAgentRuns()[0] = %+v; want the run with the agent's current name (renamed) next to the frozen one", recent[0])
	}
	e.finish(t, run, domain.RunCompleted)
	second := e.newRun(t, e.ws, withRun)
	recent, _ = e.store.RecentAgentRuns(t.Context(), e.ws, 1)
	if len(recent) != 1 || recent[0].Run.ID != second.ID {
		t.Errorf("RecentAgentRuns(limit 1) = %+v; want the newest run (any status)", recent)
	}
}

// FinishedRunsByAgent is FinishedAgentRuns for many agents at once: same runs, same order, same
// per-agent limit, only the overview columns filled in.
func TestFinishedRunsByAgent(t *testing.T) {
	e := newEnv(t)
	busy, other, idle := e.agent(t, e.ws, "busy"), e.agent(t, e.ws, "other"), e.agent(t, e.ws, "idle")
	skill := e.skill(t, e.ws, "s", true)
	base := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)

	hour := 0
	add := func(agent string, status domain.RunStatus) domain.Run {
		r := e.newRun(t, e.ws, agent)
		e.exec(t, `UPDATE eval_suite_runs SET started_at = ? WHERE id = ?`, base.Add(time.Duration(hour)*time.Hour), r.ID)
		hour++
		if status != domain.RunRunning {
			e.finish(t, r, status)
		}
		return r
	}
	for range 5 {
		add(busy, domain.RunCompleted)
		add(other, domain.RunFailed)
	}
	add(busy, domain.RunRunning) // newest, but not finished
	if _, err := e.store.CreateRun(t.Context(), domain.NewRun{
		WorkspaceID: e.ws, OwnerKind: domain.OwnerSkill, SkillID: &skill, SkillVersion: ip(1), AgentID: busy, AgentVersion: 1,
	}); err != nil {
		t.Fatal(err)
	}

	// The batch carries only the overview fields, so the single-agent answer is trimmed to them.
	trim := func(rs []domain.Run) []domain.Run {
		out := make([]domain.Run, len(rs))
		for i, r := range rs {
			r.Config = domain.Config{Model: r.Config.Model}
			r.CaseIDs = []string{}
			out[i] = r
		}
		return out
	}
	for _, limit := range []int{3, 10} {
		got, err := e.store.FinishedRunsByAgent(t.Context(), e.ws, []string{busy, other, idle, "not-a-uuid"}, limit)
		if err != nil {
			t.Fatalf("FinishedRunsByAgent(limit %d) error = %v", limit, err)
		}
		want := map[string][]domain.Run{}
		for _, agent := range []string{busy, other} {
			list, err := e.store.FinishedAgentRuns(t.Context(), e.ws, agent, limit, nil)
			if err != nil || len(list) == 0 {
				t.Fatalf("FinishedAgentRuns(%s) = %d runs, %v", agent, len(list), err)
			}
			want[agent] = trim(list)
		}
		if diff := cmp.Diff(want, got); diff != "" {
			t.Errorf("FinishedRunsByAgent(limit %d) mismatch (-want +got):\n%s", limit, diff)
		}
		if got[busy][0].Config.Model != "m" {
			t.Errorf("FinishedRunsByAgent(limit %d)[busy][0].Config.Model = %q, want m", limit, got[busy][0].Config.Model)
		}
	}
	if got, err := e.store.FinishedRunsByAgent(t.Context(), e.workspace(t), []string{busy}, 10); err != nil || len(got) != 0 {
		t.Errorf("FinishedRunsByAgent(other workspace) = %v, %v, want none", got, err)
	}
	if got, err := e.store.FinishedRunsByAgent(t.Context(), e.ws, nil, 10); err != nil || len(got) != 0 {
		t.Errorf("FinishedRunsByAgent(no agents) = %v, %v, want none", got, err)
	}
}

func TestFinishRun_SuiteCostBeyondTheColumnIsNull(t *testing.T) {
	for _, tc := range []struct {
		name string
		cost float64
		want string // cost_usd::text
	}{
		{name: "1e6 is stored as NULL", cost: 1e6, want: ""},
		{name: "the bound is stored as NULL", cost: 999_999.9999995, want: ""},
		{name: "999999.999999 stays", cost: 999_999.999999, want: "999999.999999"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			run := e.newRun(t, e.ws, e.agent(t, e.ws, "a"))
			ok, err := e.store.FinishRun(t.Context(), e.ws, run.ID, domain.RunFinish{
				Status: domain.RunCompleted, FinishedAt: time.Now(), DurationMS: 1, Metrics: domain.Metrics{CostUSD: fp(tc.cost)},
			})
			if err != nil || !ok {
				t.Fatalf("FinishRun(cost %v) = %v, %v; want true, nil", tc.cost, ok, err)
			}
			if st := e.scalar(t, `SELECT status FROM eval_suite_runs WHERE id = ?`, run.ID); st != string(domain.RunCompleted) {
				t.Errorf("status after FinishRun(cost %v) = %s; want completed", tc.cost, st)
			}
			if got := e.scalar(t, `SELECT coalesce(cost_usd::text, '') FROM eval_suite_runs WHERE id = ?`, run.ID); got != tc.want {
				t.Errorf("stored suite cost_usd for %v = %q; want %q", tc.cost, got, tc.want)
			}
		})
	}
}

func TestRecordResult_CaseCostBeyondTheColumnIsNull(t *testing.T) {
	for _, tc := range []struct {
		name string
		cost float64
		want string
	}{
		{name: "the bound is stored as NULL", cost: 999_999.9999995, want: ""},
		{name: "1e6 is stored as NULL", cost: 1e6, want: ""},
		{name: "999999.999999 stays", cost: 999_999.999999, want: "999999.999999"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			agent := e.agent(t, e.ws, "a")
			c := e.newCase(t, e.ws, domain.OwnerAgent, agent, "c")
			run := e.newRun(t, e.ws, agent, c.ID)
			if err := e.store.RecordResult(t.Context(), e.ws, domain.NewResult{RunID: run.ID, CaseID: c.ID, Status: domain.CasePassed, CostUSD: fp(tc.cost)}); err != nil {
				t.Fatalf("RecordResult(cost %v): %v", tc.cost, err)
			}
			if got := e.scalar(t, `SELECT coalesce(cost_usd::text, '') FROM eval_runs WHERE case_id = ?`, c.ID); got != tc.want {
				t.Errorf("stored case cost_usd for %v = %q; want %q", tc.cost, got, tc.want)
			}
		})
	}
}

package postgres_test

import (
	"context"
	"errors"
	"math"
	"strconv"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	reviewer "dev-digest-go/backend/internal/reviewer/domain"
	"dev-digest-go/backend/internal/reviews/domain"
)

const missingID = "00000000-0000-4000-8000-000000000000"

func TestCreateRun_StartsRunningLocal(t *testing.T) {
	f := newFixture(t)
	agent := f.agent(t, f.ws, "Alpha", true)
	multi, err := f.store.CreateMultiAgentRun(t.Context(), f.ws, f.pr)
	if err != nil {
		t.Fatalf("CreateMultiAgentRun() error = %v", err)
	}
	id := f.run(t, agent, &multi)

	if got := f.str(t, `SELECT status FROM agent_runs WHERE id = ?`, id); got != "running" {
		t.Errorf("status = %q, want running", got)
	}
	if got := f.str(t, `SELECT source FROM agent_runs WHERE id = ?`, id); got != "local" {
		t.Errorf("source = %q, want local", got)
	}
	if got := f.str(t, `SELECT multi_agent_run_id::text FROM agent_runs WHERE id = ?`, id); got != multi {
		t.Errorf("multi_agent_run_id = %q, want %q", got, multi)
	}
	if got := f.str(t, `SELECT provider || '/' || model FROM agent_runs WHERE id = ?`, id); got != "openai/gpt" {
		t.Errorf("provider/model = %q", got)
	}
	if n := f.count(t, `SELECT count(*) FROM agent_runs WHERE id = ? AND ran_at IS NOT NULL`, id); n != 1 {
		t.Errorf("ran_at not set")
	}
}

// TestCreateRun_SourceAuto covers FR7: a run created with source auto is stored as such (the
// agent_runs CHECK of migration 0006 allows it) and reads back through the trace of that run.
func TestCreateRun_SourceAuto(t *testing.T) {
	f := newFixture(t)
	agent := f.agent(t, f.ws, "Alpha", true)
	id, err := f.store.CreateRun(t.Context(), domain.NewRun{
		WorkspaceID: f.ws, Source: domain.SourceAuto, AgentID: agent, PRID: f.pr, Provider: "openai", Model: "gpt",
	})
	if err != nil {
		t.Fatalf("CreateRun(auto) error = %v", err)
	}
	if got := f.str(t, `SELECT source FROM agent_runs WHERE id = ?`, id); got != "auto" {
		t.Errorf("source = %q, want auto", got)
	}

	tr := completedTrace()
	tr.Config.Source = domain.SourceAuto
	if err := f.store.SaveTrace(t.Context(), id, tr); err != nil {
		t.Fatalf("SaveTrace(auto) error = %v", err)
	}
	got, err := f.store.FindTrace(t.Context(), f.ws, id)
	if err != nil {
		t.Fatalf("FindTrace() error = %v", err)
	}
	if got.Config.Source != "auto" {
		t.Errorf("FindTrace().Config.Source = %q, want auto", got.Config.Source)
	}
}

func TestCreateRun_AgentGone(t *testing.T) {
	f := newFixture(t)
	_, err := f.store.CreateRun(t.Context(), domain.NewRun{WorkspaceID: f.ws, AgentID: missingID, PRID: f.pr})
	if !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("CreateRun(deleted agent) error = %v, want ErrNotFound", err)
	}
}

func TestRecordRunSkills(t *testing.T) {
	f := newFixture(t)
	agent := f.agent(t, f.ws, "Alpha", true)
	run := f.run(t, agent, nil)
	s1, s2 := f.skill(t, f.ws, "one", true), f.skill(t, f.ws, "two", true)
	skills := []domain.ReviewSkill{{ID: s2, Name: "two", Version: 5}, {ID: s1, Name: "one", Version: 2}}

	if err := f.store.RecordRunSkills(t.Context(), run, skills); err != nil {
		t.Fatalf("RecordRunSkills() error = %v", err)
	}
	if err := f.store.RecordRunSkills(t.Context(), run, skills); err != nil {
		t.Fatalf("RecordRunSkills() again error = %v, want no error (conflicts are skipped)", err)
	}
	if err := f.store.RecordRunSkills(t.Context(), run, nil); err != nil {
		t.Errorf("RecordRunSkills(nil) error = %v", err)
	}
	var got []struct {
		SkillID string
		Version int
		Order   int
	}
	if err := f.db.Raw(`SELECT skill_id, skill_version AS version, "order" FROM agent_run_skills WHERE run_id = ? ORDER BY "order"`, run).Scan(&got).Error; err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].SkillID != s2 || got[0].Version != 5 || got[0].Order != 0 || got[1].SkillID != s1 || got[1].Version != 2 || got[1].Order != 1 {
		t.Errorf("agent_run_skills = %+v, want [{%s 5 0} {%s 2 1}]", got, s2, s1)
	}
	if err := f.store.RecordRunSkills(t.Context(), missingID, skills); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("RecordRunSkills(deleted run) error = %v, want ErrNotFound", err)
	}
}

func TestCompleteRunIfRunning(t *testing.T) {
	f := newFixture(t)
	agent := f.agent(t, f.ws, "Alpha", true)
	run := f.run(t, agent, nil)

	ok, err := f.store.CompleteRunIfRunning(t.Context(), run, completion())
	if err != nil || !ok {
		t.Fatalf("CompleteRunIfRunning() = %v, %v, want true, nil", ok, err)
	}
	got, err := f.store.FindRun(t.Context(), f.ws, run)
	if err != nil {
		t.Fatal(err)
	}
	if want := (domain.RunState{ID: run, Status: domain.StatusDone}); !cmp.Equal(got, want) {
		t.Errorf("FindRun() = %+v, want %+v", got, want)
	}
	if n := f.count(t, `SELECT count(*) FROM agent_runs WHERE id = ? AND duration_ms = 1500 AND tokens_in = 100
		AND tokens_out = 20 AND cost_usd = 0.002135 AND findings_count = 2 AND grounding = '2/2 passed'
		AND score = 61 AND blockers = 1 AND error IS NULL`, run); n != 1 {
		t.Errorf("completion record not stored")
	}

	again, err := f.store.CompleteRunIfRunning(t.Context(), run, completion())
	if err != nil || again {
		t.Errorf("CompleteRunIfRunning(done run) = %v, %v, want false, nil", again, err)
	}
}

// security r1 L4: a cost numeric(12,6) cannot hold must not fail the final write of a run.
func TestCompleteRunIfRunning_UnstorableCostIsStoredUnknown(t *testing.T) {
	for _, tc := range []struct {
		name string
		cost *float64
	}{
		{"1e7", floatp(1e7)},
		{"just over the column", floatp(1_000_000)},
		{"negative", floatp(-1)},
		{"NaN", floatp(math.NaN())},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			run := f.run(t, f.agent(t, f.ws, "Alpha", true), nil)
			c := completion()
			c.CostUSD = tc.cost
			ok, err := f.store.CompleteRunIfRunning(t.Context(), run, c)
			if err != nil || !ok {
				t.Fatalf("CompleteRunIfRunning(cost %v) = %v, %v, want true, nil", *tc.cost, ok, err)
			}
			if n := f.count(t, `SELECT count(*) FROM agent_runs WHERE id = ? AND status = 'done' AND cost_usd IS NULL AND findings_count = 2`, run); n != 1 {
				t.Errorf("run with cost %v not completed with an unknown cost", *tc.cost)
			}
		})
	}
	f := newFixture(t)
	run := f.run(t, f.agent(t, f.ws, "Alpha", true), nil)
	c := completion()
	c.CostUSD = floatp(999_999.999999)
	if ok, err := f.store.CompleteRunIfRunning(t.Context(), run, c); err != nil || !ok {
		t.Fatalf("CompleteRunIfRunning(cost at the column maximum) = %v, %v, want true, nil", ok, err)
	}
	if n := f.count(t, `SELECT count(*) FROM agent_runs WHERE id = ? AND cost_usd = 999999.999999`, run); n != 1 {
		t.Errorf("the column maximum was not stored as is")
	}
}

func TestCompleteRunIfRunning_CancelWins(t *testing.T) {
	f := newFixture(t)
	agent := f.agent(t, f.ws, "Alpha", true)
	run := f.run(t, agent, nil)
	if ok, err := f.store.CancelIfRunning(t.Context(), f.ws, run, domain.CancelledNote); err != nil || !ok {
		t.Fatalf("CancelIfRunning() = %v, %v", ok, err)
	}
	ok, err := f.store.CompleteRunIfRunning(t.Context(), run, completion())
	if err != nil || ok {
		t.Fatalf("CompleteRunIfRunning(cancelled) = %v, %v, want false, nil", ok, err)
	}
	if got := f.str(t, `SELECT status FROM agent_runs WHERE id = ?`, run); got != "cancelled" {
		t.Errorf("status = %q, want cancelled", got)
	}
	if got := f.str(t, `SELECT duration_ms::text FROM agent_runs WHERE id = ?`, run); got != "<nil>" {
		t.Errorf("duration_ms = %q, want the completion record not written", got)
	}
}

// errCancelWon is what the caller of a lost completion returns to roll its transaction back.
var errCancelWon = errors.New("cancel won")

// TestPersist_RollsBackWhenCancelWon is the persist step of a run that was cancelled meanwhile
// (FR4020): the review, its findings and last_reviewed_sha written before the completion go
// with the rolled-back transaction.
func TestPersist_RollsBackWhenCancelWon(t *testing.T) {
	f := newFixture(t)
	agent := f.agent(t, f.ws, "Alpha", true)
	run := f.run(t, agent, nil)
	if ok, err := f.store.CancelIfRunning(t.Context(), f.ws, run, domain.CancelledNote); err != nil || !ok {
		t.Fatalf("CancelIfRunning() = %v, %v", ok, err)
	}

	err := f.tx.InTx(t.Context(), func(ctx context.Context) error {
		id, err := f.store.InsertReview(ctx, domain.NewReview{
			WorkspaceID: f.ws, PRID: f.pr, AgentID: agent, RunID: run, Verdict: reviewer.VerdictComment, Summary: "s", Score: 1, Model: "m",
		})
		if err != nil {
			return err
		}
		if err := f.store.InsertFindings(ctx, id, []reviewer.Finding{finding("a.go", 1, 1, "t")}, nil); err != nil {
			return err
		}
		if err := f.store.MarkReviewed(ctx, f.pr, "a1b2c3d4e5f6"); err != nil {
			return err
		}
		ok, err := f.store.CompleteRunIfRunning(ctx, run, completion())
		if err != nil {
			return err
		}
		if !ok {
			return errCancelWon
		}
		return nil
	})
	if !errors.Is(err, errCancelWon) {
		t.Fatalf("InTx() error = %v, want errCancelWon", err)
	}
	if n := f.count(t, `SELECT count(*) FROM reviews`); n != 0 {
		t.Errorf("reviews = %d, want 0 after the rollback", n)
	}
	if n := f.count(t, `SELECT count(*) FROM findings`); n != 0 {
		t.Errorf("findings = %d, want 0 after the rollback", n)
	}
	if got := f.str(t, `SELECT last_reviewed_sha FROM pull_requests WHERE id = ?`, f.pr); got != "<nil>" {
		t.Errorf("last_reviewed_sha = %q, want NULL after the rollback", got)
	}
}

func TestPersist_CommitsTogether(t *testing.T) {
	f := newFixture(t)
	agent := f.agent(t, f.ws, "Alpha", true)
	run, _ := f.doneReview(t, agent, nil, finding("a.go", 1, 2, "first"))
	err := f.tx.InTx(t.Context(), func(ctx context.Context) error { return f.store.MarkReviewed(ctx, f.pr, "a1b2c3d4e5f6") })
	if err != nil {
		t.Fatal(err)
	}
	if got := f.str(t, `SELECT last_reviewed_sha FROM pull_requests WHERE id = ?`, f.pr); got != "a1b2c3d4e5f6" {
		t.Errorf("last_reviewed_sha = %q", got)
	}
	if got := f.str(t, `SELECT status FROM agent_runs WHERE id = ?`, run); got != "done" {
		t.Errorf("status = %q, want done", got)
	}
	if err := f.store.MarkReviewed(t.Context(), missingID, "x"); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("MarkReviewed(unknown pr) error = %v, want ErrNotFound", err)
	}
}

func TestEndRun(t *testing.T) {
	failedNote := "boom"
	rec := func(note *string) domain.RunCompletion {
		return domain.RunCompletion{DurationMS: 700, TokensIn: 5, TokensOut: 6, CostUSD: floatp(0), Grounding: domain.NoGrounding, Error: note}
	}
	tests := []struct {
		name       string
		setup      string // statement run on the fresh running run; "" = leave running
		status     domain.RunStatus
		note       *string
		wantStatus string
		wantError  string
		wantRecord bool
	}{
		{"running to failed", "", domain.StatusFailed, &failedNote, "failed", "boom", true},
		{"running to cancelled", "", domain.StatusCancelled, strp(domain.CancelledNote), "cancelled", "Cancelled by user", true},
		{"cancelled stays cancelled with its note", `UPDATE agent_runs SET status = 'cancelled', error = 'Cancelled by user' WHERE id = ?`,
			domain.StatusFailed, &failedNote, "cancelled", "Cancelled by user", true},
		{"cancelled without a note gets the default", `UPDATE agent_runs SET status = 'cancelled', error = NULL WHERE id = ?`,
			domain.StatusFailed, &failedNote, "cancelled", "Cancelled by user", true},
		{"cancelled keeps a custom note", `UPDATE agent_runs SET status = 'cancelled', error = 'stopped by shutdown' WHERE id = ?`,
			domain.StatusCancelled, strp(domain.CancelledNote), "cancelled", "stopped by shutdown", true},
		{"done is not changed", `UPDATE agent_runs SET status = 'done', error = NULL WHERE id = ?`, domain.StatusFailed, &failedNote, "done", "<nil>", false},
		{"done is not changed by a cancel ending", `UPDATE agent_runs SET status = 'done', error = NULL WHERE id = ?`, domain.StatusCancelled, strp(domain.CancelledNote), "done", "<nil>", false},
		{"failed keeps status and error, usage is recorded", `UPDATE agent_runs SET status = 'failed', error = 'first' WHERE id = ?`, domain.StatusCancelled, &failedNote, "failed", "first", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			agent := f.agent(t, f.ws, "Alpha", true)
			run := f.run(t, agent, nil)
			if tc.setup != "" {
				f.exec(t, tc.setup, run)
			}
			if err := f.store.EndRun(t.Context(), run, tc.status, rec(tc.note)); err != nil {
				t.Fatalf("EndRun() error = %v", err)
			}
			if got := f.str(t, `SELECT status FROM agent_runs WHERE id = ?`, run); got != tc.wantStatus {
				t.Errorf("status = %q, want %q", got, tc.wantStatus)
			}
			if got := f.str(t, `SELECT error FROM agent_runs WHERE id = ?`, run); got != tc.wantError {
				t.Errorf("error = %q, want %q", got, tc.wantError)
			}
			wrote := f.count(t, `SELECT count(*) FROM agent_runs WHERE id = ? AND duration_ms = 700 AND tokens_in = 5 AND grounding = '0/0 passed'`, run) == 1
			if wrote != tc.wantRecord {
				t.Errorf("record written = %v, want %v", wrote, tc.wantRecord)
			}
		})
	}
}

func TestEndRun_RunGone(t *testing.T) {
	f := newFixture(t)
	err := f.store.EndRun(t.Context(), missingID, domain.StatusFailed, domain.RunCompletion{Grounding: domain.NoGrounding})
	if !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("EndRun(deleted run) error = %v, want ErrNotFound", err)
	}
}

func TestEndRun_StripsUnstorableText(t *testing.T) {
	f := newFixture(t)
	agent := f.agent(t, f.ws, "Alpha", true)
	run := f.run(t, agent, nil)
	note := "bad\x00note\xff"
	if err := f.store.EndRun(t.Context(), run, domain.StatusFailed, domain.RunCompletion{Grounding: domain.NoGrounding, Error: &note}); err != nil {
		t.Fatalf("EndRun(NUL in note) error = %v", err)
	}
	if got, want := f.str(t, `SELECT error FROM agent_runs WHERE id = ?`, run), "badnote�"; got != want {
		t.Errorf("error = %q, want %q", got, want)
	}
}

func TestCancelIfRunning(t *testing.T) {
	f := newFixture(t)
	agent := f.agent(t, f.ws, "Alpha", true)
	run := f.run(t, agent, nil)

	if ok, err := f.store.CancelIfRunning(t.Context(), f.other, run, "x"); err != nil || ok {
		t.Errorf("CancelIfRunning(other workspace) = %v, %v, want false, nil", ok, err)
	}
	if ok, err := f.store.CancelIfRunning(t.Context(), f.ws, run, domain.CancelledNote); err != nil || !ok {
		t.Fatalf("CancelIfRunning() = %v, %v, want true, nil", ok, err)
	}
	if ok, err := f.store.CancelIfRunning(t.Context(), f.ws, run, domain.CancelledNote); err != nil || ok {
		t.Errorf("CancelIfRunning(again) = %v, %v, want false, nil", ok, err)
	}
	if got := f.str(t, `SELECT status || ':' || error FROM agent_runs WHERE id = ?`, run); got != "cancelled:Cancelled by user" {
		t.Errorf("run = %q", got)
	}
}

func TestFailInterrupted(t *testing.T) {
	f := newFixture(t)
	agent := f.agent(t, f.ws, "Alpha", true)
	r1, r2, r3 := f.run(t, agent, nil), f.run(t, agent, nil), f.run(t, agent, nil)
	f.exec(t, `UPDATE agent_runs SET error = 'kept' WHERE id = ?`, r1)
	f.exec(t, `UPDATE agent_runs SET status = 'done' WHERE id = ?`, r3)

	n, err := f.store.FailInterrupted(t.Context())
	if err != nil || n != 2 {
		t.Fatalf("FailInterrupted() = %d, %v, want 2, nil", n, err)
	}
	for id, want := range map[string]string{r1: "failed", r2: "failed", r3: "done"} {
		if got := f.str(t, `SELECT status FROM agent_runs WHERE id = ?`, id); got != want {
			t.Errorf("status of %s = %q, want %q", id, got, want)
		}
	}
	if got := f.str(t, `SELECT error FROM agent_runs WHERE id = ?`, r1); got != "kept" {
		t.Errorf("error = %q, want it untouched", got)
	}
	if got := f.str(t, `SELECT error FROM agent_runs WHERE id = ?`, r2); got != "<nil>" {
		t.Errorf("error = %q, want NULL (untouched)", got)
	}
	if n, err := f.store.FailInterrupted(t.Context()); err != nil || n != 0 {
		t.Errorf("FailInterrupted(again) = %d, %v, want 0, nil", n, err)
	}
}

// TestDeleteRun_RemovesTraceReviewAndFindings is AC4011 at the store: one DELETE removes the
// run with its trace, skills, review and findings.
func TestDeleteRun_RemovesTraceReviewAndFindings(t *testing.T) {
	f := newFixture(t)
	agent := f.agent(t, f.ws, "Alpha", true)
	run, _ := f.doneReview(t, agent, nil, finding("a.go", 1, 1, "x"), finding("b.go", 2, 2, "y"))
	if err := f.store.SaveTrace(t.Context(), run, domain.EndedTrace(domain.EndedTraceInput{Agent: domain.ReviewAgent{Name: "Alpha"}})); err != nil {
		t.Fatal(err)
	}
	skill := f.skill(t, f.ws, "one", true)
	if err := f.store.RecordRunSkills(t.Context(), run, []domain.ReviewSkill{{ID: skill, Version: 1}}); err != nil {
		t.Fatal(err)
	}
	// Another run's rows stay.
	other, _ := f.doneReview(t, agent, nil, finding("c.go", 3, 3, "z"))

	if ok, err := f.store.DeleteRun(t.Context(), f.other, run); err != nil || ok {
		t.Fatalf("DeleteRun(other workspace) = %v, %v, want false, nil", ok, err)
	}
	if n := f.count(t, `SELECT count(*) FROM agent_runs WHERE id = ?`, run); n != 1 {
		t.Fatalf("run deleted through another workspace")
	}
	if ok, err := f.store.DeleteRun(t.Context(), f.ws, run); err != nil || !ok {
		t.Fatalf("DeleteRun() = %v, %v, want true, nil", ok, err)
	}
	for _, table := range []string{"run_traces", "agent_run_skills"} {
		if n := f.count(t, `SELECT count(*) FROM `+table+` WHERE run_id = ?`, run); n != 0 {
			t.Errorf("%s rows of the deleted run = %d, want 0", table, n)
		}
	}
	if n := f.count(t, `SELECT count(*) FROM reviews WHERE run_id = ?`, run); n != 0 {
		t.Errorf("reviews of the deleted run = %d, want 0", n)
	}
	if n := f.count(t, `SELECT count(*) FROM findings`); n != 1 {
		t.Errorf("findings left = %d, want 1 (the other run's)", n)
	}
	if n := f.count(t, `SELECT count(*) FROM reviews WHERE run_id = ?`, other); n != 1 {
		t.Errorf("the other run's review was deleted")
	}
	if ok, err := f.store.DeleteRun(t.Context(), f.ws, run); err != nil || ok {
		t.Errorf("DeleteRun(again) = %v, %v, want false, nil", ok, err)
	}
}

func TestInsertReview_RunDeletedMeanwhile(t *testing.T) {
	f := newFixture(t)
	agent := f.agent(t, f.ws, "Alpha", true)
	_, err := f.store.InsertReview(t.Context(), domain.NewReview{
		WorkspaceID: f.ws, PRID: f.pr, AgentID: agent, RunID: missingID, Verdict: reviewer.VerdictApprove, Summary: "s", Score: 1, Model: "m",
	})
	if !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("InsertReview(deleted run) error = %v, want ErrNotFound", err)
	}
}

// A run whose agent was deleted while it ran stores its review with agent_id NULL (spec D19); the
// run record keeps agent_id NULL through the foreign key's ON DELETE SET NULL.
func TestInsertReview_AgentDeletedMeanwhile(t *testing.T) {
	for _, tc := range []struct {
		name  string
		agent func(f fixture, agent string) string
	}{
		{"deleted before the insert", func(f fixture, agent string) string { return agent }},
		{"never existed", func(f fixture, agent string) string { return missingID }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			agent := f.agent(t, f.ws, "Alpha", true)
			run := f.run(t, agent, nil)
			f.exec(t, `DELETE FROM agents WHERE id = ?`, agent)

			id, err := f.store.InsertReview(t.Context(), domain.NewReview{
				WorkspaceID: f.ws, PRID: f.pr, AgentID: tc.agent(f, agent), RunID: run,
				Verdict: reviewer.VerdictApprove, Summary: "s", Score: 7, Model: "m",
			})
			if err != nil {
				t.Fatalf("InsertReview(deleted agent) error = %v, want nil", err)
			}
			if got := f.str(t, `SELECT agent_id::text FROM reviews WHERE id = ?`, id); got != "<nil>" {
				t.Errorf("reviews.agent_id = %s, want NULL", got)
			}
			if got := f.str(t, `SELECT run_id::text FROM reviews WHERE id = ?`, id); got != run {
				t.Errorf("reviews.run_id = %s, want %s", got, run)
			}
			if got := f.str(t, `SELECT agent_id::text FROM agent_runs WHERE id = ?`, run); got != "<nil>" {
				t.Errorf("agent_runs.agent_id = %s, want NULL", got)
			}
			if err := f.store.InsertFindings(t.Context(), id, []reviewer.Finding{{
				ID: "m1", Severity: reviewer.SeverityCritical, Category: reviewer.CategorySecurity, Title: "t", File: "a.go",
				StartLine: 1, EndLine: 1, Rationale: "r", Confidence: 1,
			}}, nil); err != nil {
				t.Errorf("InsertFindings(review without agent) error = %v", err)
			}
		})
	}
}

// The agent row is locked by the insert: a delete that comes after it clears the new review's
// agent_id through ON DELETE SET NULL, and the review survives.
func TestInsertReview_AgentDeletedAfter(t *testing.T) {
	f := newFixture(t)
	agent := f.agent(t, f.ws, "Alpha", true)
	run := f.run(t, agent, nil)
	id, err := f.store.InsertReview(t.Context(), domain.NewReview{
		WorkspaceID: f.ws, PRID: f.pr, AgentID: agent, RunID: run, Verdict: reviewer.VerdictApprove, Summary: "s", Score: 7, Model: "m",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := f.str(t, `SELECT agent_id::text FROM reviews WHERE id = ?`, id); got != agent {
		t.Fatalf("reviews.agent_id = %s, want %s", got, agent)
	}
	f.exec(t, `DELETE FROM agents WHERE id = ?`, agent)
	if got := f.str(t, `SELECT agent_id::text FROM reviews WHERE id = ?`, id); got != "<nil>" {
		t.Errorf("reviews.agent_id after the delete = %s, want NULL", got)
	}
}

// A cited skill deleted while the run ran (a plugin Remove) leaves skill_id NULL and keeps the
// cited name; a skill that still exists stays linked.
func TestInsertFindings_SkillDeletedMeanwhile(t *testing.T) {
	f := newFixture(t)
	agent := f.agent(t, f.ws, "Alpha", true)
	kept := f.skill(t, f.ws, "kept", true)
	gone := f.skill(t, f.ws, "gone", true)
	run := f.run(t, agent, nil)
	review, err := f.store.InsertReview(t.Context(), domain.NewReview{
		WorkspaceID: f.ws, PRID: f.pr, AgentID: agent, RunID: run, Verdict: reviewer.VerdictApprove, Summary: "s", Score: 7, Model: "m",
	})
	if err != nil {
		t.Fatal(err)
	}
	f.exec(t, `DELETE FROM skills WHERE id = ?`, gone)
	keptName, goneName := "kept", "gone"
	finding := func(skill *string) reviewer.Finding {
		return reviewer.Finding{
			ID: "m", Severity: reviewer.SeveritySuggestion, Category: reviewer.CategoryStyle, Title: *skill, File: "a.go",
			StartLine: 1, EndLine: 1, Rationale: "r", Confidence: 1, Skill: skill,
		}
	}
	err = f.store.InsertFindings(t.Context(), review, []reviewer.Finding{finding(&keptName), finding(&goneName)},
		map[string]string{keptName: kept, goneName: gone})
	if err != nil {
		t.Fatalf("InsertFindings(deleted skill) error = %v, want nil", err)
	}
	if got := f.str(t, `SELECT skill_id::text FROM findings WHERE review_id = ? AND skill_name = 'kept'`, review); got != kept {
		t.Errorf("skill_id of the live skill = %s, want %s", got, kept)
	}
	if got := f.str(t, `SELECT coalesce(skill_id::text, 'null') FROM findings WHERE review_id = ? AND skill_name = 'gone'`, review); got != "null" {
		t.Errorf("skill_id of the deleted skill = %s, want NULL", got)
	}
}

func TestInsertFindings_Mapping(t *testing.T) {
	f := newFixture(t)
	agent := f.agent(t, f.ws, "Alpha", true)
	skill := f.skill(t, f.ws, "no-n-plus-one", true)
	run := f.run(t, agent, nil)
	review, err := f.store.InsertReview(t.Context(), domain.NewReview{
		WorkspaceID: f.ws, PRID: f.pr, AgentID: agent, RunID: run, Verdict: reviewer.VerdictRequestChanges, Summary: "bad\x00sum", Score: 40, Model: "gpt",
	})
	if err != nil {
		t.Fatal(err)
	}
	cited, unknown := "no-n-plus-one", "not-attached"
	scope := true
	findings := []reviewer.Finding{
		{ID: "m1", Severity: reviewer.SeverityCritical, Category: reviewer.CategorySecurity, Title: "t\x00one", File: "a.go",
			StartLine: 3, EndLine: 5, Rationale: "r1", Suggestion: strp("fix"), Confidence: 0.5, Kind: reviewer.KindLethalTrifecta,
			TrifectaComponents: []string{"a", "b"}, Skill: &cited, OutOfScope: &scope},
		{ID: "m2", Severity: reviewer.SeveritySuggestion, Category: reviewer.CategoryStyle, Title: "two", File: "b.go",
			StartLine: 1, EndLine: 1, Rationale: "r2", Confidence: 1, Skill: &unknown},
	}
	if err := f.store.InsertFindings(t.Context(), review, findings, map[string]string{cited: skill}); err != nil {
		t.Fatalf("InsertFindings() error = %v", err)
	}
	if err := f.store.InsertFindings(t.Context(), review, nil, nil); err != nil {
		t.Errorf("InsertFindings(none) error = %v", err)
	}

	got, err := f.store.ReviewsForPull(t.Context(), f.pr)
	if err != nil || len(got) != 1 || len(got[0].Findings) != 2 {
		t.Fatalf("ReviewsForPull() = %+v, %v", got, err)
	}
	if got[0].Summary == nil || *got[0].Summary != "badsum" {
		t.Errorf("summary = %v, want NUL stripped", got[0].Summary)
	}
	one, two := got[0].Findings[0], got[0].Findings[1]
	if one.Title != "tone" || one.Kind != reviewer.KindLethalTrifecta || !cmp.Equal(one.TrifectaComponents, []string{"a", "b"}) {
		t.Errorf("first finding = %+v", one)
	}
	if one.Skill == nil || *one.Skill != cited || !one.OutOfScope || one.Suggestion == nil || *one.Suggestion != "fix" {
		t.Errorf("first finding skill/scope/suggestion = %v/%v/%v", one.Skill, one.OutOfScope, one.Suggestion)
	}
	if two.OutOfScope {
		t.Errorf("second finding OutOfScope = true, want false for a nil flag")
	}
	if two.Kind != reviewer.KindFinding || two.TrifectaComponents != nil || two.Suggestion != nil {
		t.Errorf("second finding = %+v, want kind finding, no components, no suggestion", two)
	}
	if two.Skill == nil || *two.Skill != unknown {
		t.Errorf("second finding skill = %v, want the cited name kept", two.Skill)
	}
	if n := f.count(t, `SELECT count(*) FROM findings WHERE skill_id = ?`, skill); n != 1 {
		t.Errorf("findings linked to the skill = %d, want 1 (the unknown name stays unresolved)", n)
	}
	if n := f.count(t, `SELECT count(*) FROM findings WHERE skill_name = ? AND skill_id IS NULL`, unknown); n != 1 {
		t.Errorf("unresolved skill citation not stored as name only")
	}
}

func TestInsertFindings_KeepsOrderAcrossBatches(t *testing.T) {
	f := newFixture(t)
	agent := f.agent(t, f.ws, "Alpha", true)
	var findings []reviewer.Finding
	for i := 0; i < 1203; i++ {
		findings = append(findings, finding("f.go", i+1, i+1, "t"+string(rune('a'+i%26))))
		findings[i].Rationale = "n" + strconv.Itoa(i)
	}
	_, review := f.doneReview(t, agent, nil, findings...)
	got, err := f.store.ReviewsForPull(t.Context(), f.pr)
	if err != nil || len(got) != 1 || got[0].ID != review || len(got[0].Findings) != len(findings) {
		t.Fatalf("ReviewsForPull() returned %d reviews, err %v", len(got), err)
	}
	for i, fr := range got[0].Findings {
		if fr.StartLine != i+1 {
			t.Fatalf("finding %d has start line %d, want %d (insertion order)", i, fr.StartLine, i+1)
		}
	}
}

func TestDeleteReview(t *testing.T) {
	f := newFixture(t)
	agent := f.agent(t, f.ws, "Alpha", true)
	_, review := f.doneReview(t, agent, nil, finding("a.go", 1, 1, "x"))

	if ok, err := f.store.DeleteReview(t.Context(), f.other, review); err != nil || ok {
		t.Fatalf("DeleteReview(other workspace) = %v, %v, want false, nil", ok, err)
	}
	if ok, err := f.store.DeleteReview(t.Context(), f.ws, review); err != nil || !ok {
		t.Fatalf("DeleteReview() = %v, %v, want true, nil", ok, err)
	}
	if n := f.count(t, `SELECT count(*) FROM findings`); n != 0 {
		t.Errorf("findings left = %d, want 0 (cascade)", n)
	}
	if n := f.count(t, `SELECT count(*) FROM agent_runs`); n != 1 {
		t.Errorf("the run was deleted with its review")
	}
	if ok, err := f.store.DeleteReview(t.Context(), f.ws, review); err != nil || ok {
		t.Errorf("DeleteReview(again) = %v, %v, want false, nil", ok, err)
	}
}

func TestAcceptAndDismissFinding(t *testing.T) {
	f := newFixture(t)
	agent := f.agent(t, f.ws, "Alpha", true)
	_, _ = f.doneReview(t, agent, nil, finding("a.go", 1, 1, "x"))
	var id string
	if err := f.db.Raw(`SELECT id FROM findings`).Scan(&id).Error; err != nil {
		t.Fatal(err)
	}
	evalCase := f.insert(t, `INSERT INTO eval_cases (workspace_id, owner_kind, owner_id, name, source_finding_id)
		VALUES (?, 'agent', ?, 'c', ?) RETURNING id`, f.ws, agent, id)

	at := mustTime(t, "2026-10-22T10:00:00Z")
	acc, err := f.store.AcceptFinding(t.Context(), f.ws, id, at)
	if err != nil {
		t.Fatalf("AcceptFinding() error = %v", err)
	}
	if acc.AcceptedAt == nil || !acc.AcceptedAt.Equal(at) || acc.DismissedAt != nil {
		t.Errorf("accepted finding = accepted %v dismissed %v", acc.AcceptedAt, acc.DismissedAt)
	}
	if acc.EvalCaseID == nil || *acc.EvalCaseID != evalCase || acc.MemoryID != nil {
		t.Errorf("eval case/memory = %v/%v, want %s/nil", acc.EvalCaseID, acc.MemoryID, evalCase)
	}

	later := at.Add(time.Hour)
	dis, err := f.store.DismissFinding(t.Context(), f.ws, id, later)
	if err != nil {
		t.Fatalf("DismissFinding() error = %v", err)
	}
	if dis.DismissedAt == nil || !dis.DismissedAt.Equal(later) || dis.AcceptedAt != nil {
		t.Errorf("dismissed finding = accepted %v dismissed %v, want accept cleared", dis.AcceptedAt, dis.DismissedAt)
	}

	if _, err := f.store.AcceptFinding(t.Context(), f.other, id, at); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("AcceptFinding(other workspace) error = %v, want ErrNotFound", err)
	}
	if _, err := f.store.DismissFinding(t.Context(), f.ws, missingID, at); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("DismissFinding(unknown) error = %v, want ErrNotFound", err)
	}
}

package app_test

import (
	"context"
	"errors"
	"slices"
	"testing"

	"dev-digest-go/backend/internal/evals/app"
	"dev-digest-go/backend/internal/evals/domain"
	"dev-digest-go/backend/internal/platform/apperr"
)

// fakeBudget is a scripted app.Budget that records every Admit call.
type fakeBudget struct {
	err   error
	calls []budgetCall
}

type budgetCall struct{ workspaceID, op string }

func (b *fakeBudget) Admit(_ context.Context, workspaceID, op string) error {
	b.calls = append(b.calls, budgetCall{workspaceID, op})
	return b.err
}

var capReached = apperr.PaymentRequired("Monthly spend cap of $50.00 reached").WithCode("spend_cap_reached")

// withBudget rebuilds the run service of env with b as its budget (nil = off).
func (e *runEnv) withBudget(b app.Budget) {
	e.svc = app.NewRunService(app.RunDeps{
		Cases: e.cases, Runs: e.runs, Snapshots: e.snapshots, Launcher: e.launcher, Budget: b, Now: fixedClock(),
	})
}

// startFn is one of the three start entries, with the budget op it must ask for.
type startFn struct {
	name string
	op   string
	call func(t *testing.T, e *runEnv) error
	prep func(e *runEnv) // gives the entry something to start
}

func startEntries() []startFn {
	return []startFn{
		{"StartAgent", "eval_agent_start",
			func(t *testing.T, e *runEnv) error { _, err := e.svc.StartAgent(t.Context(), ws, "a-1"); return err },
			func(e *runEnv) { e.addCases(domain.OwnerAgent, "a-1", "c1") }},
		{"StartSkill", "eval_skill_start",
			func(t *testing.T, e *runEnv) error {
				_, err := e.svc.StartSkill(t.Context(), ws, "s-1", "a-1")
				return err
			},
			func(e *runEnv) { e.addCases(domain.OwnerSkill, "s-1", "c1") }},
		{"RunAll", "eval_run_all",
			func(t *testing.T, e *runEnv) error { _, err := e.svc.RunAll(t.Context(), ws); return err },
			func(e *runEnv) { e.addCases(domain.OwnerAgent, "a-1", "c1") }},
	}
}

func TestStarts_BudgetAdmitsOnceWithTheOp(t *testing.T) {
	for _, tc := range startEntries() {
		t.Run(tc.name, func(t *testing.T) {
			env := newRunEnv(t)
			tc.prep(env)
			// Run-all with several startable agents still asks once.
			env.snapshots.agents["a-2"] = agentSnap("a-2", 1)
			env.snapshots.order = []string{"a-1", "a-2"}
			env.addCases(domain.OwnerAgent, "a-2", "c2")
			b := &fakeBudget{}
			env.withBudget(b)
			if err := tc.call(t, env); err != nil {
				t.Fatalf("%s() error = %v", tc.name, err)
			}
			if want := []budgetCall{{ws, tc.op}}; !slices.Equal(b.calls, want) {
				t.Errorf("%s() Admit calls = %v, want %v", tc.name, b.calls, want)
			}
			if len(env.runs.created) == 0 {
				t.Errorf("%s() created no run, want at least one", tc.name)
			}
		})
	}
}

func TestStarts_BudgetReachedIs402AndCreatesNothing(t *testing.T) {
	for _, tc := range startEntries() {
		t.Run(tc.name, func(t *testing.T) {
			env := newRunEnv(t)
			tc.prep(env)
			b := &fakeBudget{err: capReached}
			env.withBudget(b)
			err := tc.call(t, env)
			if err != error(capReached) {
				t.Fatalf("%s() error = %v, want the budget's 402 unchanged", tc.name, err)
			}
			wantApp(t, err, apperr.KindPaymentRequired, "spend_cap_reached", "Monthly spend cap of $50.00 reached")
			if len(b.calls) != 1 {
				t.Errorf("%s() Admit calls = %d, want 1", tc.name, len(b.calls))
			}
			if len(env.runs.created) != 0 || len(env.launcher.runs) != 0 {
				t.Errorf("%s() created/launched = %d/%d, want 0/0", tc.name, len(env.runs.created), len(env.launcher.runs))
			}
		})
	}
}

func TestStarts_BudgetFailureIsReturnedAndCreatesNothing(t *testing.T) {
	boom := errors.New("spend read failed")
	for _, tc := range startEntries() {
		t.Run(tc.name, func(t *testing.T) {
			env := newRunEnv(t)
			tc.prep(env)
			env.withBudget(&fakeBudget{err: boom})
			err := tc.call(t, env)
			if !errors.Is(err, boom) {
				t.Fatalf("%s() error = %v, want the budget failure", tc.name, err)
			}
			if _, isApp := apperr.As(err); isApp {
				t.Errorf("%s() error %v is an apperr, want a plain error (500 at the edge)", tc.name, err)
			}
			if len(env.runs.created) != 0 || len(env.launcher.runs) != 0 {
				t.Errorf("%s() created/launched = %d/%d, want 0/0", tc.name, len(env.runs.created), len(env.launcher.runs))
			}
		})
	}
}

func TestStarts_NilBudgetIsOff(t *testing.T) {
	for _, tc := range startEntries() {
		t.Run(tc.name, func(t *testing.T) {
			env := newRunEnv(t) // newRunEnv wires no budget
			tc.prep(env)
			if err := tc.call(t, env); err != nil {
				t.Fatalf("%s() error = %v", tc.name, err)
			}
			if len(env.runs.created) != 1 {
				t.Errorf("%s() created = %d, want 1", tc.name, len(env.runs.created))
			}
		})
	}
}

// 404 and 422 win over 402 (EC9): the budget is not asked.
func TestStarts_NotFoundAndValidationWinOverBudget(t *testing.T) {
	tests := []struct {
		name string
		call func(t *testing.T, e *runEnv) error
		kind apperr.Kind
	}{
		{"agent unknown", func(t *testing.T, e *runEnv) error { _, err := e.svc.StartAgent(t.Context(), ws, "nope"); return err }, apperr.KindNotFound},
		{"agent without cases", func(t *testing.T, e *runEnv) error { _, err := e.svc.StartAgent(t.Context(), ws, "a-1"); return err }, apperr.KindValidation},
		{"skill unknown", func(t *testing.T, e *runEnv) error {
			_, err := e.svc.StartSkill(t.Context(), ws, "nope", "a-1")
			return err
		}, apperr.KindNotFound},
		{"skill host agent unknown", func(t *testing.T, e *runEnv) error {
			_, err := e.svc.StartSkill(t.Context(), ws, "s-1", "nope")
			return err
		}, apperr.KindValidation},
		{"skill without cases", func(t *testing.T, e *runEnv) error {
			_, err := e.svc.StartSkill(t.Context(), ws, "s-1", "a-1")
			return err
		}, apperr.KindValidation},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			env := newRunEnv(t)
			b := &fakeBudget{err: capReached}
			env.withBudget(b)
			err := tc.call(t, env)
			ae, ok := apperr.As(err)
			if !ok || ae.Kind != tc.kind {
				t.Errorf("%s error = %v, want kind %s", tc.name, err, tc.kind)
			}
			if len(b.calls) != 0 {
				t.Errorf("%s Admit calls = %v, want none", tc.name, b.calls)
			}
		})
	}
}

// A running run is a 409 (agent/skill) or a skip (run-all) found only when the row is inserted,
// after the cap check, so a reached cap answers 402 there.
func TestStarts_BudgetReachedWinsOverActiveRun(t *testing.T) {
	env := newRunEnv(t)
	env.addCases(domain.OwnerAgent, "a-1", "c1")
	env.runs.active[ownerKey{domain.OwnerAgent, "a-1"}] = true
	env.withBudget(&fakeBudget{err: capReached})
	_, err := env.svc.StartAgent(t.Context(), ws, "a-1")
	wantApp(t, err, apperr.KindPaymentRequired, "spend_cap_reached", "Monthly spend cap of $50.00 reached")
}

// A vanished agent mid-run-all still aborts with 404 after the single admit.
func TestRunAll_BudgetAdmittedOnceThenVanishedAgentIs404(t *testing.T) {
	env := newRunEnv(t)
	env.snapshots.agents["a-2"] = agentSnap("a-2", 1)
	env.snapshots.order = []string{"a-1", "a-2"}
	env.snapshots.vanished = map[string]bool{"a-2": true}
	env.addCases(domain.OwnerAgent, "a-1", "c1")
	b := &fakeBudget{}
	env.withBudget(b)
	_, err := env.svc.RunAll(t.Context(), ws)
	wantApp(t, err, apperr.KindNotFound, "not_found", "Agent not found")
	if len(b.calls) != 1 {
		t.Errorf("RunAll() Admit calls = %d, want 1", len(b.calls))
	}
}

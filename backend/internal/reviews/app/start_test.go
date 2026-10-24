package app_test

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"dev-digest-go/backend/internal/platform/apperr"
	"dev-digest-go/backend/internal/reviews/app"
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

// withBudget rebuilds the harness service with b as its budget.
func (h *harness) withBudget(b app.Budget) {
	h.svc = app.NewService(app.Deps{
		Store: h.store, Tx: h.tx, Executor: h.exec, Bus: h.bus, Budget: b,
		Now: func() time.Time { return h.now },
	})
}

var capReached = apperr.PaymentRequired("Monthly spend cap of $50.00 reached").WithCode("spend_cap_reached")

func TestStart_BudgetAdmitsOncePerRequest(t *testing.T) {
	tests := []struct {
		name string
		req  app.StartRequest
	}{
		{"agentId", app.StartRequest{AgentID: "a"}},
		{"all", app.StartRequest{All: true}},
		{"agentIds", app.StartRequest{AgentIDs: []string{"a", "b"}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			h.withPull()
			h.withAgent("a", "A", true)
			h.withAgent("b", "B", true)
			b := &fakeBudget{}
			h.withBudget(b)
			if _, err := h.svc.Start(t.Context(), ws, "pr", tc.req); err != nil {
				t.Fatalf("Start(%s) error = %v", tc.name, err)
			}
			want := []budgetCall{{ws, "review_start"}}
			if !slices.Equal(b.calls, want) {
				t.Errorf("Admit calls = %v, want %v", b.calls, want)
			}
			if len(h.exec.batches) != 1 {
				t.Errorf("executor batches = %d, want 1", len(h.exec.batches))
			}
		})
	}
}

func TestStart_BudgetReachedIs402AndCreatesNothing(t *testing.T) {
	h := newHarness(t)
	h.withPull()
	h.withAgent("a", "A", true)
	b := &fakeBudget{err: capReached}
	h.withBudget(b)
	_, err := h.svc.Start(t.Context(), ws, "pr", app.StartRequest{AgentID: "a"})
	if err != error(capReached) {
		t.Fatalf("Start() error = %v, want the budget's 402 unchanged", err)
	}
	wantErr(t, err, apperr.KindPaymentRequired, "spend_cap_reached", "Monthly spend cap of $50.00 reached")
	if len(b.calls) != 1 {
		t.Errorf("Admit calls = %d, want 1", len(b.calls))
	}
	if len(h.j.all()) != 0 || len(h.exec.batches) != 0 {
		t.Errorf("Start() made calls %v and %d batches, want nothing", h.j.all(), len(h.exec.batches))
	}
}

func TestStart_BudgetFailureIsReturnedAndCreatesNothing(t *testing.T) {
	h := newHarness(t)
	h.withPull()
	h.withAgent("a", "A", true)
	boom := errors.New("spend read failed")
	h.withBudget(&fakeBudget{err: boom})
	_, err := h.svc.Start(t.Context(), ws, "pr", app.StartRequest{AgentID: "a"})
	if !errors.Is(err, boom) {
		t.Fatalf("Start() error = %v, want the budget failure", err)
	}
	if _, isApp := apperr.As(err); isApp {
		t.Errorf("Start() error %v is an apperr, want a plain error (500 at the edge)", err)
	}
	if len(h.j.all()) != 0 || len(h.exec.batches) != 0 {
		t.Errorf("Start() made calls %v and %d batches, want nothing", h.j.all(), len(h.exec.batches))
	}
}

func TestStart_NilBudgetIsOff(t *testing.T) {
	h := newHarness(t) // newHarness wires no budget
	h.withPull()
	h.withAgent("a", "A", true)
	got, err := h.svc.Start(t.Context(), ws, "pr", app.StartRequest{AgentID: "a"})
	if err != nil || len(got.Runs) != 1 || len(h.exec.batches) != 1 {
		t.Errorf("Start() = %+v, %v with %d batches, want one started run", got, err, len(h.exec.batches))
	}
}

// 422 and 404 win over 402 (EC9): the budget is not even asked.
func TestStart_ValidationAndNotFoundWinOverBudget(t *testing.T) {
	tests := []struct {
		name string
		prep func(h *harness)
		req  app.StartRequest
		kind apperr.Kind
	}{
		{"invalid request", func(h *harness) { h.withPull() }, app.StartRequest{}, apperr.KindInvalidInput},
		{"unknown agent", func(h *harness) { h.withPull() }, app.StartRequest{AgentID: "ghost"}, apperr.KindNotFound},
		{"unknown pull", func(h *harness) { h.withAgent("a", "A", true) }, app.StartRequest{AgentID: "a"}, apperr.KindNotFound},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			tc.prep(h)
			b := &fakeBudget{err: capReached}
			h.withBudget(b)
			_, err := h.svc.Start(t.Context(), ws, "pr", tc.req)
			ae, ok := apperr.As(err)
			if !ok || ae.Kind != tc.kind {
				t.Errorf("Start(%s) error = %v, want kind %s", tc.name, err, tc.kind)
			}
			if len(b.calls) != 0 {
				t.Errorf("Admit calls = %v, want none before the 422/404 checks", b.calls)
			}
		})
	}
}

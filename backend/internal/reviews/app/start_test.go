package app_test

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"dev-digest-go/backend/internal/platform/apperr"
	"dev-digest-go/backend/internal/reviews/app"
	"dev-digest-go/backend/internal/reviews/domain"
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

func TestStartAuto_RecordsSourceAutoOnRunsAndBatch(t *testing.T) {
	h := newHarness(t)
	h.withPull()
	h.withAgent("a", "Alpha", true)
	h.withAgent("b", "Beta", true)
	b := &fakeBudget{}
	h.withBudget(b)

	got, err := h.svc.StartAuto(t.Context(), ws, "pr", []string{"a", "b"})
	if err != nil {
		t.Fatalf("StartAuto() error = %v", err)
	}
	if len(got.Runs) != 2 || got.MultiAgentRunID == nil {
		t.Errorf("StartAuto() = %+v, want 2 runs under a multi-agent run", got)
	}
	if len(h.store.createdRuns) != 2 {
		t.Fatalf("created runs = %d, want 2", len(h.store.createdRuns))
	}
	for _, r := range h.store.createdRuns {
		if r.Source != domain.SourceAuto {
			t.Errorf("CreateRun(%s).Source = %q, want %q", r.AgentID, r.Source, domain.SourceAuto)
		}
	}
	if len(h.exec.batches) != 1 || h.exec.batches[0].Source != domain.SourceAuto {
		t.Errorf("executor batches = %+v, want one batch with source auto", h.exec.batches)
	}
	if want := []budgetCall{{ws, "review_start"}}; !slices.Equal(b.calls, want) {
		t.Errorf("Admit calls = %v, want %v", b.calls, want)
	}
}

func TestStart_RecordsSourceLocal(t *testing.T) {
	h := newHarness(t)
	h.withPull()
	h.withAgent("a", "Alpha", true)
	if _, err := h.svc.Start(t.Context(), ws, "pr", app.StartRequest{AgentID: "a"}); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if got := h.store.createdRuns[0].Source; got != domain.SourceLocal {
		t.Errorf("CreateRun().Source = %q, want %q", got, domain.SourceLocal)
	}
	if got := h.exec.batches[0].Source; got != domain.SourceLocal {
		t.Errorf("batch.Source = %q, want %q", got, domain.SourceLocal)
	}
}

// TestStartAuto_BudgetReachedIsRecognisable covers the S8 contract: the cap's 402 comes back as an
// *apperr.Error of KindPaymentRequired that errors.As finds, with nothing created.
func TestStartAuto_BudgetReachedIsRecognisable(t *testing.T) {
	h := newHarness(t)
	h.withPull()
	h.withAgent("a", "Alpha", true)
	b := &fakeBudget{err: capReached}
	h.withBudget(b)

	_, err := h.svc.StartAuto(t.Context(), ws, "pr", []string{"a"})
	var ae *apperr.Error
	if !errors.As(err, &ae) || ae.Kind != apperr.KindPaymentRequired {
		t.Fatalf("StartAuto() error = %v, want an apperr of kind payment required", err)
	}
	if len(h.j.all()) != 0 || len(h.exec.batches) != 0 {
		t.Errorf("StartAuto() made calls %v and %d batches, want nothing", h.j.all(), len(h.exec.batches))
	}
}

func TestStartAuto_Refusals(t *testing.T) {
	tests := []struct {
		name string
		ids  []string
		want apperr.Kind
		code string
	}{
		{"no ids", nil, apperr.KindInvalidInput, "invalid_run_request"},
		{"empty ids", []string{}, apperr.KindInvalidInput, "invalid_run_request"},
		{"unknown agent", []string{"missing"}, apperr.KindNotFound, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			h.withPull()
			h.withAgent("a", "Alpha", true)
			b := &fakeBudget{}
			h.withBudget(b)
			_, err := h.svc.StartAuto(t.Context(), ws, "pr", tc.ids)
			var ae *apperr.Error
			if !errors.As(err, &ae) || ae.Kind != tc.want {
				t.Fatalf("StartAuto(%v) error = %v, want kind %v", tc.ids, err, tc.want)
			}
			if len(b.calls) != 0 || len(h.exec.batches) != 0 {
				t.Errorf("StartAuto(%v) admitted %d times and started %d batches, want none", tc.ids, len(b.calls), len(h.exec.batches))
			}
		})
	}
}

// pluginAgent registers an agent owned by the plugin "Acme Pack"; its enabled flag is the plugin's.
func (h *harness) pluginAgent(id, name string, pluginEnabled bool) {
	a := h.withAgent(id, name, true)
	a.Plugin = &domain.PluginRef{ID: "plugin-1", Name: "Acme Pack", Enabled: pluginEnabled}
	h.store.agents[id] = a
}

func wantPluginDisabled(t *testing.T, err error, message string) {
	t.Helper()
	var ae *apperr.Error
	if !errors.As(err, &ae) || ae.Kind != apperr.KindConflict || ae.Code != "plugin_disabled" || ae.Message != message {
		t.Fatalf("error = %v, want 409 plugin_disabled %q", err, message)
	}
}

func TestStart_AgentOfDisabledPluginIs409AndStartsNothing(t *testing.T) {
	const msg = `Agent "Lint" belongs to the disabled plugin "Acme Pack"`
	tests := []struct {
		name string
		req  app.StartRequest
	}{
		{"agentId", app.StartRequest{AgentID: "p"}},
		{"agentIds", app.StartRequest{AgentIDs: []string{"p"}}},
		{"mixed agentIds (EC9)", app.StartRequest{AgentIDs: []string{"a", "p", "b"}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			h.withPull()
			h.withAgent("a", "Alpha", true)
			h.withAgent("b", "Beta", true)
			h.pluginAgent("p", "Lint", false)
			b := &fakeBudget{}
			h.withBudget(b)
			_, err := h.svc.Start(t.Context(), ws, "pr", tc.req)
			wantPluginDisabled(t, err, msg)
			if len(h.j.all()) != 0 || len(h.exec.batches) != 0 || len(b.calls) != 0 {
				t.Errorf("Start() made calls %v, %d batches, %d admits, want none", h.j.all(), len(h.exec.batches), len(b.calls))
			}
		})
	}
}

func TestStart_AgentOfEnabledPluginRuns(t *testing.T) {
	h := newHarness(t)
	h.withPull()
	h.pluginAgent("p", "Lint", true)
	got, err := h.svc.Start(t.Context(), ws, "pr", app.StartRequest{AgentID: "p"})
	if err != nil || len(got.Runs) != 1 || len(h.exec.batches) != 1 {
		t.Errorf("Start() = %+v, %v with %d batches, want one started run", got, err, len(h.exec.batches))
	}
}

func TestStart_AllModeTrustsTheListRead(t *testing.T) {
	h := newHarness(t)
	h.withPull()
	h.withAgent("a", "Alpha", true)
	got, err := h.svc.Start(t.Context(), ws, "pr", app.StartRequest{All: true})
	if err != nil || len(got.Runs) != 1 {
		t.Errorf("Start(all) = %+v, %v, want the one listed agent", got, err)
	}
}

// SCR6: unknown agent 404 first, then plugin 409, then the pull request 404, then the 402.
func TestStart_PluginDisabledOrdering(t *testing.T) {
	t.Run("unknown agent wins over the plugin 409", func(t *testing.T) {
		h := newHarness(t)
		h.withPull()
		h.pluginAgent("p", "Lint", false)
		_, err := h.svc.Start(t.Context(), ws, "pr", app.StartRequest{AgentIDs: []string{"p", "ghost"}})
		if ae, ok := apperr.As(err); !ok || ae.Kind != apperr.KindNotFound {
			t.Errorf("Start(disabled plugin, unknown agent) error = %v, want 404", err)
		}
	})
	t.Run("invalid request wins", func(t *testing.T) {
		h := newHarness(t)
		h.pluginAgent("p", "Lint", false)
		_, err := h.svc.Start(t.Context(), ws, "pr", app.StartRequest{})
		if ae, ok := apperr.As(err); !ok || ae.Kind != apperr.KindInvalidInput {
			t.Errorf("Start(invalid) error = %v, want 422", err)
		}
	})
	t.Run("wins over the pull request 404", func(t *testing.T) {
		h := newHarness(t)
		h.pluginAgent("p", "Lint", false)
		_, err := h.svc.Start(t.Context(), ws, "no-such-pr", app.StartRequest{AgentID: "p"})
		wantPluginDisabled(t, err, `Agent "Lint" belongs to the disabled plugin "Acme Pack"`)
	})
	t.Run("wins over the spend cap", func(t *testing.T) {
		h := newHarness(t)
		h.withPull()
		h.pluginAgent("p", "Lint", false)
		b := &fakeBudget{err: capReached}
		h.withBudget(b)
		_, err := h.svc.Start(t.Context(), ws, "pr", app.StartRequest{AgentID: "p"})
		wantPluginDisabled(t, err, `Agent "Lint" belongs to the disabled plugin "Acme Pack"`)
		if len(b.calls) != 0 {
			t.Errorf("Admit calls = %v, want none", b.calls)
		}
	})
}

func TestStartAuto_AgentOfDisabledPluginIs409AndStartsNothing(t *testing.T) {
	h := newHarness(t)
	h.withPull()
	h.withAgent("a", "Alpha", true)
	h.pluginAgent("p", "Lint", false)
	b := &fakeBudget{}
	h.withBudget(b)
	_, err := h.svc.StartAuto(t.Context(), ws, "pr", []string{"a", "p"})
	wantPluginDisabled(t, err, `Agent "Lint" belongs to the disabled plugin "Acme Pack"`)
	if len(h.j.all()) != 0 || len(h.exec.batches) != 0 || len(b.calls) != 0 {
		t.Errorf("StartAuto() made calls %v, %d batches, %d admits, want none", h.j.all(), len(h.exec.batches), len(b.calls))
	}
}

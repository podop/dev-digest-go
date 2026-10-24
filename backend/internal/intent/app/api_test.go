package app_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"context"
	"dev-digest-go/backend/internal/intent/app"
	"dev-digest-go/backend/internal/intent/domain"
	"dev-digest-go/backend/internal/llm"
	"dev-digest-go/backend/internal/platform/apperr"
)

func wantAppErr(t *testing.T, err error, kind apperr.Kind, code, msg string) {
	t.Helper()
	ae, ok := apperr.As(err)
	if !ok {
		t.Fatalf("error = %v (%T), want an apperr", err, err)
	}
	if ae.Kind != kind || ae.Code != code || ae.Message != msg {
		t.Errorf("error = {%v %q %q}, want {%v %q %q}", ae.Kind, ae.Code, ae.Message, kind, code, msg)
	}
}

// derivedWorld is a world whose pull request has a stored record of the current key.
func derivedWorld(t *testing.T) *world {
	t.Helper()
	w := newWorld(t, app.Options{})
	if r := w.svc.Resolve(t.Context(), w.input(false), nil); !r.Used {
		t.Fatalf("setup derivation = %+v", r)
	}
	return w
}

func TestGet(t *testing.T) {
	w := newWorld(t, app.Options{})

	t.Run("unknown pull request is 404", func(t *testing.T) {
		_, err := w.svc.Get(t.Context(), wsID, "99999999-9999-4999-8999-999999999999")
		wantAppErr(t, err, apperr.KindNotFound, "not_found", "Pull request not found")
	})
	t.Run("a pull request of another workspace is 404", func(t *testing.T) {
		_, err := w.svc.Get(t.Context(), "44444444-4444-4444-8444-444444444444", prID)
		wantAppErr(t, err, apperr.KindNotFound, "not_found", "Pull request not found")
	})
	t.Run("no record", func(t *testing.T) {
		got, err := w.svc.Get(t.Context(), wsID, prID)
		if err != nil || got.Intent != nil || got.Stale {
			t.Errorf("Get() = %+v, %v, want no intent, not stale", got, err)
		}
	})
	t.Run("a store error is an error", func(t *testing.T) {
		w := newWorld(t, app.Options{})
		w.store.getErr = errors.New("db down")
		if _, err := w.svc.Get(t.Context(), wsID, prID); err == nil {
			t.Error("Get() error = nil, want the store's error")
		}
	})
}

func TestGet_Staleness(t *testing.T) {
	tests := []struct {
		name      string
		change    func(w *world)
		wantStale bool
	}{
		{"unchanged", func(*world) {}, false},
		{"title edited", func(w *world) { w.pulls.setPullTitle("Another title") }, true},
		{"body edited", func(w *world) { w.pulls.pull.Body += " more" }, true},
		{"branch renamed", func(w *world) { w.pulls.pull.Branch = "other" }, true},
		{"head moved", func(w *world) { w.pulls.pull.HeadSHA = "fedcba9876543210fedcba9876543210fedcba98" }, true},
		{"feature model changed", func(w *world) { w.settings.setIntentModel("openai", "gpt-4.1") }, true},
		{"ticket mentioned in a new commit", func(w *world) { w.pulls.commits = []domain.Commit{{Message: "fixes #12"}} }, true},
		{"doc linked in the body", func(w *world) { w.pulls.pull.Body += " See docs/plans/x.md" }, true},
		{"commits without references", func(w *world) { w.pulls.commits = []domain.Commit{{Message: "tidy"}} }, false},
		{"changed files", func(w *world) { w.pulls.files = []string{"a.go"} }, false},
		{"settings cannot be read", func(w *world) { w.settings.setErr(errors.New("down")); w.pulls.pull.Title = "x" }, false},
		{"key cannot be computed", func(w *world) { w.pulls.commitsErr = errors.New("down"); w.pulls.pull.Title = "x" }, false},
		{"malformed escape in a new link", func(w *world) {
			w.pulls.pull.Body += " [d](https://github.com/acme/api/blob/main/docs/%E0%A4%A.md)"
		}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w := derivedWorld(t)
			tc.change(w)

			got, err := w.svc.Get(t.Context(), wsID, prID)

			if err != nil || got.Intent == nil || got.Stale != tc.wantStale {
				t.Errorf("Get() = %+v, %v, want a record with stale=%v", got, err, tc.wantStale)
			}
			if n := len(w.llm.calls()); n != 1 {
				t.Errorf("LLM calls = %d, want only the setup call (Get derives nothing)", n)
			}
		})
	}
}

func TestGet_WithoutARepoRowIsNotStale(t *testing.T) {
	w := derivedWorld(t)
	w.pulls.repo = nil

	got, err := w.svc.Get(t.Context(), wsID, prID)

	if err != nil || got.Intent == nil || got.Stale {
		t.Errorf("Get() = %+v, %v, want the record, not stale", got, err)
	}
}

func TestRefresh(t *testing.T) {
	t.Run("derives, ignores a fresh cache and answers not stale", func(t *testing.T) {
		w := derivedWorld(t)

		got, err := w.svc.Refresh(t.Context(), wsID, prID)

		if err != nil || got.Intent == nil || got.Stale || got.Intent.InputHash != baseHash {
			t.Fatalf("Refresh() = %+v, %v, want the record, not stale", got, err)
		}
		if n := len(w.llm.calls()); n != 2 {
			t.Errorf("LLM calls = %d, want 2 (the setup and the forced one)", n)
		}
	})
	t.Run("a thin pull request uses the stored changed paths", func(t *testing.T) {
		w := newWorld(t, app.Options{})
		w.pulls.pull.Body = ""
		w.pulls.files = []string{"src/a.go", "src/b.go"}

		got, err := w.svc.Refresh(t.Context(), wsID, prID)

		if err != nil || got.Intent == nil || got.Intent.DerivedFrom != "inferred" {
			t.Fatalf("Refresh() = %+v, %v, want an inferred record", got, err)
		}
		user := w.llm.calls()[0].Messages[1].Content
		if want := "Changed files:\nsrc/a.go\nsrc/b.go"; !strings.Contains(user, want) {
			t.Errorf("user message lacks %q:\n%s", want, user)
		}
	})
	t.Run("unknown pull request is 404", func(t *testing.T) {
		w := newWorld(t, app.Options{})
		_, err := w.svc.Refresh(t.Context(), wsID, "99999999-9999-4999-8999-999999999999")
		wantAppErr(t, err, apperr.KindNotFound, "not_found", "Pull request not found")
	})
	t.Run("missing repository is 404", func(t *testing.T) {
		w := newWorld(t, app.Options{})
		w.pulls.repo = nil
		_, err := w.svc.Refresh(t.Context(), wsID, prID)
		wantAppErr(t, err, apperr.KindNotFound, "not_found", "Repo not found")
	})
	t.Run("a failed derivation is 502 intent_unavailable and keeps the old row", func(t *testing.T) {
		w := derivedWorld(t)
		before, _ := w.store.row(prID)
		w.llm.setReply(func(context.Context, llm.Request) (llm.Result, error) {
			return llm.Result{}, errors.New("OPENROUTER_API_KEY is not configured")
		})

		_, err := w.svc.Refresh(t.Context(), wsID, prID)

		wantAppErr(t, err, apperr.KindExternalService, "intent_unavailable", "Intent derivation failed: OPENROUTER_API_KEY is not configured")
		if after, _ := w.store.row(prID); after.InputHash != before.InputHash || after.Text != before.Text {
			t.Errorf("stored row changed on a failed refresh: %+v", after)
		}
	})
	t.Run("a malformed escape is 502 URI malformed", func(t *testing.T) {
		w := newWorld(t, app.Options{})
		w.pulls.pull.Body = "[d](https://github.com/acme/api/blob/main/docs/%E0%A4%A.md)"
		_, err := w.svc.Refresh(t.Context(), wsID, prID)
		wantAppErr(t, err, apperr.KindExternalService, "intent_unavailable", "Intent derivation failed: URI malformed")
	})
}

// fakeBudget is the spend cap port: it records every call and answers with err.
type fakeBudget struct {
	err   error
	calls [][2]string // workspace, op
}

func (b *fakeBudget) Admit(_ context.Context, workspaceID, op string) error {
	b.calls = append(b.calls, [2]string{workspaceID, op})
	return b.err
}

// budgetWorld is a world whose pull request has a stored record and whose service has the budget.
func budgetWorld(t *testing.T, b *fakeBudget) *world {
	t.Helper()
	w := derivedWorld(t)
	w.svc = app.NewService(app.Deps{
		Pulls: w.pulls, Settings: w.settings, LLM: w.llm, Docs: w.docs, Tickets: w.tickets, Store: w.store,
		Clock: func() time.Time { return fixedNow }, Budget: b,
	}, app.Options{})
	return w
}

func TestRefresh_Budget(t *testing.T) {
	capReached := func() error {
		return apperr.PaymentRequired("Monthly spend cap reached").WithCode("spend_cap_reached")
	}
	t.Run("admitted once with the op, then the derivation runs", func(t *testing.T) {
		b := &fakeBudget{}
		w := budgetWorld(t, b)
		setup := len(w.llm.calls())

		if _, err := w.svc.Refresh(t.Context(), wsID, prID); err != nil {
			t.Fatalf("Refresh() error = %v", err)
		}

		if want := [][2]string{{wsID, "intent_refresh"}}; len(b.calls) != 1 || b.calls[0] != want[0] {
			t.Errorf("Admit calls = %v, want %v", b.calls, want)
		}
		if n := len(w.llm.calls()) - setup; n != 1 {
			t.Errorf("LLM calls = %d, want 1", n)
		}
	})
	t.Run("cap reached is a 402 and nothing is derived or stored", func(t *testing.T) {
		b := &fakeBudget{err: capReached()}
		w := budgetWorld(t, b)
		setupCalls, setupUpserts := len(w.llm.calls()), w.store.upsertCount()

		_, err := w.svc.Refresh(t.Context(), wsID, prID)

		wantAppErr(t, err, apperr.KindPaymentRequired, "spend_cap_reached", "Monthly spend cap reached")
		if n := len(w.llm.calls()); n != setupCalls {
			t.Errorf("LLM calls = %d, want %d (none after the refusal)", n, setupCalls)
		}
		if n := w.store.upsertCount(); n != setupUpserts {
			t.Errorf("upserts = %d, want %d", n, setupUpserts)
		}
	})
	t.Run("a failed spend read is a plain error and nothing is derived", func(t *testing.T) {
		boom := errors.New("ledger down")
		b := &fakeBudget{err: boom}
		w := budgetWorld(t, b)
		setupCalls := len(w.llm.calls())

		_, err := w.svc.Refresh(t.Context(), wsID, prID)

		if !errors.Is(err, boom) {
			t.Fatalf("Refresh() error = %v, want the budget error unchanged", err)
		}
		if _, isApp := apperr.As(err); isApp {
			t.Errorf("Refresh() error = %v, want a plain error (500 at the edge)", err)
		}
		if n := len(w.llm.calls()); n != setupCalls {
			t.Errorf("LLM calls = %d, want %d", n, setupCalls)
		}
	})
	t.Run("404s win over 402 and skip the check", func(t *testing.T) {
		b := &fakeBudget{err: capReached()}
		w := budgetWorld(t, b)
		_, errPull := w.svc.Refresh(t.Context(), wsID, "99999999-9999-4999-8999-999999999999")
		w.pulls.repo = nil
		_, errRepo := w.svc.Refresh(t.Context(), wsID, prID)

		wantAppErr(t, errPull, apperr.KindNotFound, "not_found", "Pull request not found")
		wantAppErr(t, errRepo, apperr.KindNotFound, "not_found", "Repo not found")
		if len(b.calls) != 0 {
			t.Errorf("Admit called %d times, want 0", len(b.calls))
		}
	})
	t.Run("nil budget leaves Refresh unchanged", func(t *testing.T) {
		w := derivedWorld(t)
		if _, err := w.svc.Refresh(t.Context(), wsID, prID); err != nil {
			t.Fatalf("Refresh() error = %v", err)
		}
	})
	t.Run("the cap does not gate Resolve, the review pre-work and stale derivation", func(t *testing.T) {
		b := &fakeBudget{err: capReached()}
		w := budgetWorld(t, b)

		if r := w.svc.Resolve(t.Context(), w.input(true), nil); !r.Used {
			t.Errorf("Resolve() = %+v, want a derivation despite the cap", r)
		}
		if len(b.calls) != 0 {
			t.Errorf("Admit called %d times by Resolve, want 0", len(b.calls))
		}
	})
}

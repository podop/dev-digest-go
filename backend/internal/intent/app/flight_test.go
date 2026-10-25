package app_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/intent/app"
	"dev-digest-go/backend/internal/llm"
	"dev-digest-go/backend/internal/platform/apperr"
	"dev-digest-go/backend/internal/platform/inflight"
)

// Every test runs in a synctest bubble: synctest.Wait returns when all goroutines are blocked, so
// "the joiner has attached" and "the flight has wound down" are observed without sleeping, and the
// 120 s budget passes instantly.

// resolveAsync runs Resolve on its own goroutine.
func resolveAsync(ctx context.Context, w *world, in app.ResolveInput, c *capture) <-chan app.Result {
	out := make(chan app.Result, 1)
	go func() {
		var sink app.Sink
		if c != nil {
			sink = c.sink
		}
		out <- w.svc.Resolve(ctx, in, sink)
	}()
	return out
}

// outcome returns the result that must be there once the bubble is quiet.
func outcome(t *testing.T, ch <-chan app.Result) app.Result {
	t.Helper()
	synctest.Wait()
	select {
	case r := <-ch:
		return r
	default:
		t.Fatal("Resolve has not returned")
		return app.Result{}
	}
}

func waiting(ch <-chan app.Result) bool {
	synctest.Wait()
	return len(ch) == 0
}

// gatedModel makes the model wait for release (or for its context) and then answer.
func gatedModel(w *world) (release func()) {
	gate := make(chan struct{})
	w.llm.setReply(func(ctx context.Context, _ llm.Request) (llm.Result, error) {
		select {
		case <-gate:
			return answer(defaultAnswer), nil
		case <-ctx.Done():
			return llm.Result{}, ctx.Err()
		}
	})
	return func() { close(gate) }
}

// reasonCtx is a context cancelled with a reason, like the review executor's.
func reasonCtx(t *testing.T) (context.Context, func(reason string)) {
	t.Helper()
	ctx, cancel := context.WithCancelCause(t.Context())
	return ctx, func(reason string) { cancel(errors.New(reason)) }
}

func TestFlight_TwoCallersOfOneKeyShareOneCall(t *testing.T) { // AC5027
	synctest.Test(t, func(t *testing.T) {
		w := newWorld(t, app.Options{})
		release := gatedModel(w)
		var review, other capture

		a := resolveAsync(t.Context(), w, w.input(false), &review)
		synctest.Wait()
		b := resolveAsync(t.Context(), w, w.input(false), &other)
		synctest.Wait()
		if n := w.svc.InFlight(); n != 1 {
			t.Fatalf("InFlight() = %d with two callers of one key, want 1", n)
		}
		release()

		ra, rb := outcome(t, a), outcome(t, b)
		if !ra.Used || ra.CacheHit || rb.Used != ra.Used || diffRecords(ra, rb) != "" {
			t.Errorf("results = %+v and %+v, want the same record from one derivation", ra, rb)
		}
		for name, c := range map[string]*capture{"first": &review, "second": &other} {
			if diff := cmp.Diff(derivedEvents, c.got()); diff != "" {
				t.Errorf("%s caller's events mismatch (-want +got):\n%s", name, diff)
			}
		}
		if n := len(w.llm.calls()); n != 1 {
			t.Errorf("LLM calls = %d, want 1", n)
		}
		if n := w.store.upsertCount(); n != 1 {
			t.Errorf("rows written = %d, want 1", n)
		}
		if n := w.svc.InFlight(); n != 0 {
			t.Errorf("InFlight() = %d after the derivation, want 0", n)
		}
	})
}

func diffRecords(a, b app.Result) string { return cmp.Diff(a.Record, b.Record) }

func TestFlight_ARefreshAndAReviewShareOneCall(t *testing.T) { // EC5007
	synctest.Test(t, func(t *testing.T) {
		w := newWorld(t, app.Options{})
		w.pulls.files = []string{"src/config.ts"}
		release := gatedModel(w)

		review := resolveAsync(t.Context(), w, w.input(false), nil)
		synctest.Wait()
		refreshed := make(chan error, 1)
		var resp app.Response
		go func() {
			var err error
			resp, err = w.svc.Refresh(t.Context(), wsID, prID)
			refreshed <- err
		}()
		synctest.Wait()
		release()

		if r := outcome(t, review); !r.Used {
			t.Fatalf("review Resolve() = %+v, want used", r)
		}
		synctest.Wait()
		if err := <-refreshed; err != nil {
			t.Fatalf("Refresh() error = %v", err)
		}
		if resp.Intent == nil || resp.Intent.InputHash != baseHash || resp.Stale {
			t.Errorf("Refresh() = %+v, want the record of the shared derivation, not stale", resp)
		}
		calls := w.llm.calls()
		if len(calls) != 1 {
			t.Fatalf("LLM calls = %d, want 1", len(calls))
		}
		// The review started the flight, so its kind is charged (spec A7).
		want := &llm.Charge{WorkspaceID: wsID, Kind: llm.KindReviewIntent, RefID: prID}
		if diff := cmp.Diff(want, calls[0].Charge); diff != "" {
			t.Errorf("shared derivation Charge mismatch (-want +got):\n%s", diff)
		}
	})
}

func TestFlight_DifferentKeysDoNotJoin(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w := newWorld(t, app.Options{})
		release := gatedModel(w)
		other := w.input(false)
		other.Pull.Title = "A different title"

		a := resolveAsync(t.Context(), w, w.input(false), nil)
		b := resolveAsync(t.Context(), w, other, nil)
		synctest.Wait()
		if n := w.svc.InFlight(); n != 2 {
			t.Fatalf("InFlight() = %d for two keys, want 2", n)
		}
		release()

		if !outcome(t, a).Used || !outcome(t, b).Used {
			t.Error("a derivation did not finish used")
		}
		if n := len(w.llm.calls()); n != 2 {
			t.Errorf("LLM calls = %d, want 2", n)
		}
	})
}

func TestFlight_OneOfTwoCallersCancelsTheOtherStillGetsTheRecord(t *testing.T) { // EC5006
	synctest.Test(t, func(t *testing.T) {
		w := newWorld(t, app.Options{})
		release := gatedModel(w)
		ctxA, cancelA := reasonCtx(t)
		var ca, cb capture

		a := resolveAsync(ctxA, w, w.input(false), &ca)
		synctest.Wait()
		b := resolveAsync(t.Context(), w, w.input(false), &cb)
		synctest.Wait()
		cancelA("Cancelled by user")

		if ra := outcome(t, a); ra.Used || ra.Warning != "Cancelled by user" {
			t.Errorf("cancelled caller got %+v, want unavailable with its reason", ra)
		}
		if diff := cmp.Diff([]ev{{Kind: "info", Msg: "warning: intent unavailable — Cancelled by user", Data: `{"warning":"intent_unavailable"}`}}, ca.got()); diff != "" {
			t.Errorf("cancelled caller's events mismatch (-want +got):\n%s", diff)
		}
		if !waiting(b) {
			t.Fatal("the other caller returned when only one of two cancelled")
		}
		release()

		if rb := outcome(t, b); !rb.Used {
			t.Errorf("other caller got %+v, want the record", rb)
		}
		if diff := cmp.Diff(derivedEvents, cb.got()); diff != "" {
			t.Errorf("other caller's events mismatch (-want +got):\n%s", diff)
		}
		if n, up := len(w.llm.calls()), w.store.upsertCount(); n != 1 || up != 1 {
			t.Errorf("LLM calls, rows written = %d, %d, want 1, 1", n, up)
		}
	})
}

func TestFlight_WhenEveryCallerCancelsTheDerivationIsAbortedAndNothingIsStored(t *testing.T) { // EC5006
	synctest.Test(t, func(t *testing.T) {
		w := newWorld(t, app.Options{})
		var seen error
		w.llm.setReply(func(ctx context.Context, _ llm.Request) (llm.Result, error) {
			<-ctx.Done()
			seen = context.Cause(ctx)
			return llm.Result{}, ctx.Err()
		})
		ctxA, cancelA := context.WithCancel(t.Context()) // no reason: JavaScript's default abort
		ctxB, cancelB := reasonCtx(t)

		a := resolveAsync(ctxA, w, w.input(false), nil)
		synctest.Wait()
		b := resolveAsync(ctxB, w, w.input(false), nil)
		synctest.Wait()
		cancelA()
		if !waiting(b) || w.svc.InFlight() != 1 {
			t.Fatal("the derivation ended when only one of two callers cancelled")
		}
		cancelB("Cancelled by user")

		if ra := outcome(t, a); ra.Used || ra.Warning != "This operation was aborted" {
			t.Errorf("first canceller got %+v, want the default abort text", ra)
		}
		if rb := outcome(t, b); rb.Used || rb.Warning != "Cancelled by user" {
			t.Errorf("last canceller got %+v, want its own reason", rb)
		}
		if seen == nil || seen.Error() != "Cancelled by user" {
			t.Errorf("the model call ended with %v, want the last caller's reason", seen)
		}
		if up, n := w.store.upsertCount(), w.svc.InFlight(); up != 0 || n != 0 {
			t.Errorf("rows written, InFlight() = %d, %d, want 0, 0", up, n)
		}
	})
}

func TestFlight_ACallerThatCannotCancelKeepsTheDerivationAlive(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w := newWorld(t, app.Options{})
		release := gatedModel(w)
		ctxA, cancelA := context.WithCancel(t.Context())

		a := resolveAsync(ctxA, w, w.input(false), nil)
		synctest.Wait()
		b := resolveAsync(context.WithoutCancel(t.Context()), w, w.input(false), nil)
		synctest.Wait()
		cancelA()

		if ra := outcome(t, a); ra.Used || ra.Warning != "This operation was aborted" {
			t.Errorf("cancelled caller got %+v, want the default abort text", ra)
		}
		release()
		if rb := outcome(t, b); !rb.Used {
			t.Errorf("caller without cancellation got %+v, want the record", rb)
		}
		if n := len(w.llm.calls()); n != 1 {
			t.Errorf("LLM calls = %d, want 1", n)
		}
	})
}

func TestFlight_AContextCancelledBeforeTheCallStartsNothing(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w := newWorld(t, app.Options{})
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		var c capture

		got := w.svc.Resolve(ctx, w.input(false), c.sink)

		if got.Used || got.Warning != "This operation was aborted" {
			t.Errorf("Resolve() = %+v, want unavailable, aborted", got)
		}
		if n := len(w.llm.calls()); n != 0 {
			t.Errorf("LLM calls = %d, want 0", n)
		}
	})
}

func TestFlight_TheBudgetEndsADerivationWithItsOwnMessage(t *testing.T) { // AC5028, EC5005
	synctest.Test(t, func(t *testing.T) {
		w := newWorld(t, app.Options{Budget: 120 * time.Second})
		w.llm.setReply(blockUntilDone)
		var c capture

		a := resolveAsync(t.Context(), w, w.input(false), &c)
		synctest.Wait()
		if n := w.svc.InFlight(); n != 1 {
			t.Fatalf("InFlight() = %d before the budget, want 1", n)
		}
		time.Sleep(119 * time.Second)
		if !waiting(a) {
			t.Fatal("the derivation ended before its budget")
		}
		time.Sleep(time.Second)

		want := "intent derivation exceeded its 120000ms budget"
		if got := outcome(t, a); got.Used || got.Warning != want {
			t.Errorf("Resolve() = %+v, want unavailable: %s", got, want)
		}
		if diff := cmp.Diff([]ev{{Kind: "info", Msg: "warning: intent unavailable — " + want, Data: `{"warning":"intent_unavailable"}`}}, c.got()); diff != "" {
			t.Errorf("events mismatch (-want +got):\n%s", diff)
		}
		if up, n := w.store.upsertCount(), w.svc.InFlight(); up != 0 || n != 0 {
			t.Errorf("rows written, InFlight() = %d, %d, want 0, 0 (the flight entry is gone)", up, n)
		}
	})
}

func TestFlight_TheBudgetShorterThanTheDefaultComesFromOptions(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w := newWorld(t, app.Options{Budget: 5 * time.Second})
		w.llm.setReply(blockUntilDone)

		a := resolveAsync(t.Context(), w, w.input(false), nil)
		synctest.Wait()
		time.Sleep(5 * time.Second)

		if got := outcome(t, a); got.Warning != "intent derivation exceeded its 5000ms budget" {
			t.Errorf("Warning = %q, want the 5000ms budget text", got.Warning)
		}
	})
}

func TestFlight_ATicketReadStuckPastTheBudgetIsCutAndSeenByEveryCaller(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w := newWorld(t, app.Options{})
		w.pulls.pull.Body = "Closes #7 with a description that is long enough to be substantive."
		in := w.input(false)
		in.Pull = *w.pulls.pull
		w.tickets.on = func(ctx context.Context, _ float64) (app.TicketRead, bool) {
			<-ctx.Done()
			return app.TicketRead{Reason: ctx.Err().Error()}, true // a read that ends with its context fails
		}

		a := resolveAsync(t.Context(), w, in, nil)
		synctest.Wait()
		b := resolveAsync(t.Context(), w, in, nil)
		synctest.Wait()
		time.Sleep(120 * time.Second)

		want := "intent derivation exceeded its 120000ms budget"
		for name, ch := range map[string]<-chan app.Result{"first": a, "second": b} {
			if got := outcome(t, ch); got.Used || got.Warning != want {
				t.Errorf("%s caller got %+v, want unavailable: %s", name, got, want)
			}
		}
		if n := len(w.llm.calls()); n != 0 {
			t.Errorf("LLM calls = %d, want 0 (cut before the call)", n)
		}
	})
}

func TestFlight_ASharedFailureReachesEveryCallerAndALaterCallStartsFresh(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w := newWorld(t, app.Options{})
		var calls atomic.Int32
		gate := make(chan struct{})
		w.llm.setReply(func(context.Context, llm.Request) (llm.Result, error) {
			if calls.Add(1) == 1 {
				<-gate
				return llm.Result{}, errors.New("The model call failed")
			}
			return answer(defaultAnswer), nil
		})
		var ca, cb capture

		a := resolveAsync(t.Context(), w, w.input(false), &ca)
		synctest.Wait()
		b := resolveAsync(t.Context(), w, w.input(false), &cb)
		synctest.Wait()
		close(gate)

		wantEvents := []ev{{Kind: "info", Msg: "warning: intent unavailable — The model call failed", Data: `{"warning":"intent_unavailable"}`}}
		for name, r := range map[string]app.Result{"first": outcome(t, a), "second": outcome(t, b)} {
			if r.Used || r.Warning != "The model call failed" {
				t.Errorf("%s caller got %+v, want unavailable with the shared failure", name, r)
			}
		}
		for name, c := range map[string]*capture{"first": &ca, "second": &cb} {
			if diff := cmp.Diff(wantEvents, c.got()); diff != "" {
				t.Errorf("%s caller's events mismatch (-want +got):\n%s", name, diff)
			}
		}

		if c := w.svc.Resolve(t.Context(), w.input(false), nil); !c.Used {
			t.Errorf("a later call got %+v, want a fresh derivation that succeeds", c)
		}
		if n, up := len(w.llm.calls()), w.store.upsertCount(); n != 2 || up != 1 {
			t.Errorf("LLM calls, rows written = %d, %d, want 2, 1", n, up)
		}
		if n := w.svc.InFlight(); n != 0 {
			t.Errorf("InFlight() = %d, want 0", n)
		}
	})
}

func TestFlight_ShutdownCancelsRunningDerivationsAndRefusesNewOnes(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w := newWorld(t, app.Options{})
		w.llm.setReply(blockUntilDone)

		a := resolveAsync(t.Context(), w, w.input(false), nil)
		synctest.Wait()
		if err := w.svc.Shutdown(t.Context()); err != nil {
			t.Fatalf("Shutdown() = %v, want nil once the flight returned", err)
		}

		if got := outcome(t, a); got.Used || got.Warning != "service is shutting down" {
			t.Errorf("running caller got %+v, want unavailable: service is shutting down", got)
		}
		var c capture
		later := w.svc.Resolve(t.Context(), w.input(false), c.sink)
		if later.Used || later.Warning != "service is shutting down" || len(c.got()) != 1 {
			t.Errorf("Resolve() after Shutdown = %+v, events %v, want unavailable with its warning event", later, c.got())
		}
		if n, up := len(w.llm.calls()), w.store.upsertCount(); n != 1 || up != 0 {
			t.Errorf("LLM calls, rows written = %d, %d, want 1, 0", n, up)
		}
		if n := w.svc.InFlight(); n != 0 {
			t.Errorf("InFlight() = %d after Shutdown, want 0", n)
		}
	})
}

func TestFlight_ShutdownReportsADerivationThatDoesNotReturn(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w := newWorld(t, app.Options{})
		stuck := make(chan struct{})
		w.llm.setReply(func(context.Context, llm.Request) (llm.Result, error) {
			<-stuck // a model client that ignores its context
			return llm.Result{}, errors.New("late")
		})
		_ = resolveAsync(t.Context(), w, w.input(false), nil)
		synctest.Wait()
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()

		err := w.svc.Shutdown(ctx)

		if !errors.Is(err, inflight.ErrNotDrained) {
			t.Errorf("Shutdown() = %v, want ErrNotDrained", err)
		}
		close(stuck)
		synctest.Wait()
	})
}

func TestFlight_ARefreshSurvivesTheClientGoingAway(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w := newWorld(t, app.Options{})
		release := gatedModel(w)
		ctx, cancel := context.WithCancel(t.Context())
		type outcome struct {
			resp app.Response
			err  error
		}
		done := make(chan outcome, 1)
		go func() {
			resp, err := w.svc.Refresh(ctx, wsID, prID)
			done <- outcome{resp, err}
		}()
		synctest.Wait()
		cancel() // the client disconnects mid-derivation
		synctest.Wait()
		if n := w.svc.InFlight(); n != 1 {
			t.Fatalf("InFlight() = %d after the client left, want the derivation still running", n)
		}
		release()
		synctest.Wait()

		got := <-done
		if got.err != nil || got.resp.Intent == nil || got.resp.Intent.InputHash != baseHash {
			t.Errorf("Refresh() = %+v, %v, want the derived record", got.resp, got.err)
		}
		if row, ok := w.store.row(prID); !ok || row.InputHash != baseHash {
			t.Errorf("stored row = %+v, %v, want the derivation stored", row, ok)
		}
		if n := w.svc.InFlight(); n != 0 {
			t.Errorf("InFlight() = %d, want 0", n)
		}
	})
}

func TestFlight_ARefreshStillEndsOnTheBudgetAndOnShutdown(t *testing.T) {
	t.Run("budget", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			w := newWorld(t, app.Options{Budget: 120 * time.Second})
			w.llm.setReply(blockUntilDone)
			done := make(chan error, 1)
			go func() { _, err := w.svc.Refresh(t.Context(), wsID, prID); done <- err }()
			synctest.Wait()
			time.Sleep(120 * time.Second)
			synctest.Wait()

			wantAppErr(t, <-done, apperr.KindExternalService, "intent_unavailable",
				"Intent derivation failed: intent derivation exceeded its 120000ms budget")
			if n := w.store.upsertCount(); n != 0 {
				t.Errorf("rows written = %d, want 0", n)
			}
		})
	})
	t.Run("shutdown", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			w := newWorld(t, app.Options{})
			w.llm.setReply(blockUntilDone)
			done := make(chan error, 1)
			go func() { _, err := w.svc.Refresh(t.Context(), wsID, prID); done <- err }()
			synctest.Wait()
			if err := w.svc.Shutdown(t.Context()); err != nil {
				t.Fatalf("Shutdown() = %v", err)
			}
			synctest.Wait()

			wantAppErr(t, <-done, apperr.KindExternalService, "intent_unavailable", "Intent derivation failed: service is shutting down")
		})
	})
}

package inflight_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/platform/inflight"
)

// Every test runs in a synctest bubble: synctest.Wait returns once all goroutines are blocked,
// so "the joiner has attached" and "the flight is wound down" are observed without sleeping.

type result struct {
	val int
	err error
}

type flights = inflight.Flights[int, string]

// callDo runs f.Do on its own goroutine; the outcome arrives on the returned channel.
func callDo(f *flights, ctx context.Context, key string, sink func(string),
	fn func(context.Context, func(string)) (int, error)) <-chan result {
	out := make(chan result, 1)
	go func() {
		v, err := f.Do(ctx, key, sink, fn)
		out <- result{v, err}
	}()
	return out
}

// get returns the outcome that must be there once the bubble is quiet.
func get(t *testing.T, ch <-chan result) result {
	t.Helper()
	synctest.Wait()
	select {
	case r := <-ch:
		return r
	default:
		t.Fatal("caller has not returned")
		return result{}
	}
}

// pending reports whether the caller is still waiting once the bubble is quiet.
func pending(ch <-chan result) bool {
	synctest.Wait()
	return len(ch) == 0
}

// gate is a flight function that blocks until released (or its context ends) and returns val.
type gate struct {
	release chan struct{}
	calls   atomic.Int32
	lastCtx atomic.Pointer[context.Context]
}

func newGate() *gate { return &gate{release: make(chan struct{})} }

func (g *gate) fn(val int) func(context.Context, func(string)) (int, error) {
	return func(ctx context.Context, _ func(string)) (int, error) {
		g.calls.Add(1)
		g.lastCtx.Store(&ctx)
		select {
		case <-g.release:
			return val, nil
		case <-ctx.Done():
			return 0, ctx.Err()
		}
	}
}

func newFlights(budget time.Duration, budgetErr error) *flights {
	return inflight.NewFlights[int, string](inflight.FlightsConfig{Budget: budget, BudgetErr: budgetErr})
}

func TestDo_ConcurrentCallersShareOneRun(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newFlights(0, nil)
		g := newGate()
		a := callDo(f, context.Background(), "pr1:h", nil, g.fn(7))
		synctest.Wait()
		b := callDo(f, context.Background(), "pr1:h", nil, g.fn(99)) // would answer 99 if it ran
		other := newGate()
		c := callDo(f, context.Background(), "pr2:h", nil, other.fn(5))
		synctest.Wait()
		if got := f.Len(); got != 2 {
			t.Fatalf("Len() with two keys running = %d, want 2", got)
		}
		close(g.release)
		close(other.release)

		if got, want := [3]result{get(t, a), get(t, b), get(t, c)}, [3]result{{7, nil}, {7, nil}, {5, nil}}; got != want {
			t.Errorf("Do results (first, joiner, other key) = %+v, want %+v", got, want)
		}
		if n := g.calls.Load(); n != 1 {
			t.Errorf("fn runs for one key = %d, want 1", n)
		}
		if n := f.Len(); n != 0 {
			t.Errorf("Len() after the flights ended = %d, want 0", n)
		}
	})
}

func TestDo_JoinerReceivesEventsFromJoinOn(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newFlights(0, nil)
		step := make(chan struct{})
		fn := func(_ context.Context, emit func(string)) (int, error) {
			emit("one")
			<-step
			emit("two")
			return 1, nil
		}
		var mu sync.Mutex
		var first, second []string
		collect := func(dst *[]string) func(string) {
			return func(e string) { mu.Lock(); *dst = append(*dst, e); mu.Unlock() }
		}
		a := callDo(f, context.Background(), "k", collect(&first), fn)
		synctest.Wait() // "one" is out, the first caller is its only receiver
		b := callDo(f, context.Background(), "k", collect(&second), fn)
		synctest.Wait()
		close(step)
		get(t, a)
		get(t, b)

		mu.Lock()
		defer mu.Unlock()
		if diff := cmp.Diff([]string{"one", "two"}, first); diff != "" {
			t.Errorf("first caller events mismatch (-want +got):\n%s", diff)
		}
		if diff := cmp.Diff([]string{"two"}, second); diff != "" {
			t.Errorf("joiner events mismatch (-want +got):\n%s", diff)
		}
	})
}

func TestDo_OneCancelsWhileTheOtherWaits(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newFlights(0, nil)
		g := newGate()
		ctxA, cancelA := context.WithCancelCause(context.Background())
		ctxB, cancelB := context.WithCancelCause(context.Background())
		defer cancelB(nil)
		errA := errors.New("A went away")
		a := callDo(f, ctxA, "k", nil, g.fn(3))
		synctest.Wait()
		b := callDo(f, ctxB, "k", nil, g.fn(3))
		synctest.Wait()

		cancelA(errA)
		if got := get(t, a); got != (result{0, errA}) {
			t.Errorf("cancelled caller = %+v, want the cancellation cause at once", got)
		}
		if !pending(b) || f.Len() != 1 {
			t.Fatalf("the flight must go on for the remaining caller (pending=%v, Len=%d)", pending(b), f.Len())
		}
		if err := (*g.lastCtx.Load()).Err(); err != nil {
			t.Errorf("flight context after one of two callers cancelled = %v, want live", err)
		}
		close(g.release)
		if got := get(t, b); got != (result{3, nil}) {
			t.Errorf("remaining caller = %+v, want {3 <nil>}", got)
		}
	})
}

func TestDo_AllCallersCancelAbortsTheFlight(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newFlights(0, nil)
		g := newGate()
		ctxA, cancelA := context.WithCancelCause(context.Background())
		ctxB, cancelB := context.WithCancelCause(context.Background())
		errA, errB := errors.New("A went away"), errors.New("B went away")
		a := callDo(f, ctxA, "k", nil, g.fn(3))
		synctest.Wait()
		b := callDo(f, ctxB, "k", nil, g.fn(3))
		synctest.Wait()

		cancelA(errA)
		synctest.Wait() // A has left; B is the last one
		cancelB(errB)
		if got := get(t, a); !errors.Is(got.err, errA) {
			t.Errorf("first caller = %+v, want its own cause", got)
		}
		if got := get(t, b); !errors.Is(got.err, errB) {
			t.Errorf("second caller = %+v, want its own cause", got)
		}
		fctx := *g.lastCtx.Load()
		if cause := context.Cause(fctx); !errors.Is(cause, errB) {
			t.Errorf("flight context cause = %v, want the last caller's (%v)", cause, errB)
		}
		if n := f.Len(); n != 0 {
			t.Errorf("Len() after everyone cancelled = %d, want 0", n)
		}
	})
}

func TestDo_CallerThatCannotCancelKeepsTheFlightAlive(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newFlights(0, nil)
		g := newGate()
		ctxA, cancelA := context.WithCancel(context.Background())
		a := callDo(f, ctxA, "k", nil, g.fn(4))
		synctest.Wait()
		b := callDo(f, context.Background(), "k", nil, g.fn(4)) // Done() == nil
		synctest.Wait()

		cancelA()
		get(t, a)
		if err := (*g.lastCtx.Load()).Err(); err != nil || !pending(b) {
			t.Fatalf("flight ctx err = %v, plain caller pending = %v, want a live flight and a waiting caller", err, pending(b))
		}
		close(g.release)
		if got := get(t, b); got != (result{4, nil}) {
			t.Errorf("plain caller = %+v, want {4 <nil>}", got)
		}
	})
}

func TestDo_JoinAfterAbortWaitsForTheWindDownThenRunsFresh(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newFlights(0, nil)
		linger := make(chan struct{})
		var calls atomic.Int32
		slow := func(ctx context.Context, _ func(string)) (int, error) {
			n := calls.Add(1)
			if n == 1 {
				<-ctx.Done() // aborted, but takes its time to return
				<-linger
				return 0, ctx.Err()
			}
			return 42, nil
		}
		ctxA, cancelA := context.WithCancel(context.Background())
		a := callDo(f, ctxA, "k", nil, slow)
		synctest.Wait()
		cancelA()
		get(t, a)
		if f.Len() != 1 {
			t.Fatalf("Len() while the aborted fn winds down = %d, want 1 (one run per key)", f.Len())
		}
		b := callDo(f, context.Background(), "k", nil, slow)
		if !pending(b) || calls.Load() != 1 {
			t.Fatalf("joiner after abort must wait for the old run (pending=%v, calls=%d)", pending(b), calls.Load())
		}
		close(linger)
		if got := get(t, b); got != (result{42, nil}) {
			t.Errorf("joiner after abort = %+v, want a fresh run's {42 <nil>}", got)
		}
		if n := calls.Load(); n != 2 {
			t.Errorf("fn runs = %d, want 2", n)
		}
	})
}

func TestDo_AlreadyCancelledCallerDoesNotStartAFlight(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newFlights(0, nil)
		errGone := errors.New("gone")
		ctx, cancel := context.WithCancelCause(context.Background())
		cancel(errGone)
		g := newGate()
		if got := get(t, callDo(f, ctx, "k", nil, g.fn(1))); got.err != errGone {
			t.Errorf("Do(cancelled ctx) = %+v, want its cause", got)
		}
		if g.calls.Load() != 0 || f.Len() != 0 {
			t.Errorf("fn runs = %d, Len = %d, want 0, 0", g.calls.Load(), f.Len())
		}
	})
}

func TestDo_FailureIsSharedAndTheEntryIsFree(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newFlights(0, nil)
		boom := errors.New("boom")
		release := make(chan struct{})
		fail := func(context.Context, func(string)) (int, error) { <-release; return 0, boom }
		a := callDo(f, context.Background(), "k", nil, fail)
		synctest.Wait()
		b := callDo(f, context.Background(), "k", nil, fail)
		synctest.Wait()
		close(release)
		for name, ch := range map[string]<-chan result{"first": a, "joiner": b} {
			if got := get(t, ch); got.err != boom {
				t.Errorf("%s caller = %+v, want boom", name, got)
			}
		}
		g := newGate()
		close(g.release)
		if got := get(t, callDo(f, context.Background(), "k", nil, g.fn(8))); got != (result{8, nil}) {
			t.Errorf("Do after a failed flight = %+v, want a fresh run {8 <nil>}", got)
		}
	})
}

var errBudget = errors.New("intent derivation exceeded its 120000ms budget")

func TestDo_BudgetEndsTheFlightWithItsMessage(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newFlights(120*time.Second, errBudget)
		g := newGate() // never released: a model that never answers
		a := callDo(f, context.Background(), "k", nil, g.fn(1))
		synctest.Wait()
		time.Sleep(119 * time.Second)
		if !pending(a) {
			t.Fatal("flight ended before its budget")
		}
		time.Sleep(time.Second)
		got := get(t, a)
		if got.err != errBudget {
			t.Errorf("Do after 120s = %+v, want the budget error (context errors are replaced by the cause)", got)
		}
		if n := f.Len(); n != 0 {
			t.Errorf("Len() after the budget = %d, want 0 (AC5028)", n)
		}
	})
}

func TestDo_FailureBeforeTheBudgetKeepsItsError(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newFlights(120*time.Second, errBudget)
		boom := errors.New("provider said no")
		got := get(t, callDo(f, context.Background(), "k", nil, func(context.Context, func(string)) (int, error) { return 0, boom }))
		if got.err != boom {
			t.Errorf("Do = %+v, want the function's own error", got)
		}
	})
}

func TestDo_PanicIsRaisedOnEveryWaitingCaller(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newFlights(0, nil)
		release := make(chan struct{})
		bang := func(context.Context, func(string)) (int, error) { <-release; panic("bang") }
		recovered := make(chan any, 2)
		call := func() {
			defer func() { recovered <- recover() }()
			_, _ = f.Do(context.Background(), "k", nil, bang)
		}
		go call()
		synctest.Wait()
		go call()
		synctest.Wait()
		close(release)
		synctest.Wait()

		for range 2 {
			v := <-recovered
			pe, ok := v.(*inflight.PanicError)
			if !ok || pe.Value != "bang" || len(pe.Stack) == 0 {
				t.Errorf("recovered %#v, want *PanicError{Value: bang, Stack: …}", v)
			}
		}
		if n := f.Len(); n != 0 {
			t.Errorf("Len() after a panicking flight = %d, want 0", n)
		}
		g := newGate()
		close(g.release)
		if got := get(t, callDo(f, context.Background(), "k", nil, g.fn(2))); got != (result{2, nil}) {
			t.Errorf("Do after a panicked flight = %+v, want a fresh run {2 <nil>}", got)
		}
	})
}

func TestShutdown_CancelsFlightsAndRefusesNewOnes(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newFlights(0, nil)
		g := newGate()
		a := callDo(f, context.Background(), "k", nil, g.fn(1))
		synctest.Wait()

		if err := f.Shutdown(context.Background()); err != nil {
			t.Errorf("Shutdown() = %v, want nil", err)
		}
		if got := get(t, a); !errors.Is(got.err, inflight.ErrShutdown) {
			t.Errorf("running caller after Shutdown = %+v, want ErrShutdown", got)
		}
		if cause := context.Cause(*g.lastCtx.Load()); !errors.Is(cause, inflight.ErrShutdown) {
			t.Errorf("flight context cause = %v, want ErrShutdown", cause)
		}
		before := g.calls.Load()
		if got := get(t, callDo(f, context.Background(), "k2", nil, g.fn(1))); !errors.Is(got.err, inflight.ErrShutdown) || g.calls.Load() != before {
			t.Errorf("Do after Shutdown = %+v (fn runs %d -> %d), want ErrShutdown without running fn", got, before, g.calls.Load())
		}
	})
}

func TestShutdown_ReportsFlightsThatDoNotReturn(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newFlights(0, nil)
		stuck := make(chan struct{})
		deaf := func(context.Context, func(string)) (int, error) { <-stuck; return 0, nil }
		a := callDo(f, context.Background(), "k", nil, deaf)
		synctest.Wait()

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		err := f.Shutdown(ctx)
		if !errors.Is(err, inflight.ErrNotDrained) {
			t.Errorf("Shutdown() with a flight that ignores its context = %v, want ErrNotDrained", err)
		}
		close(stuck)
		get(t, a)
		if err := f.Shutdown(context.Background()); err != nil {
			t.Errorf("second Shutdown() once the flight returned = %v, want nil", err)
		}
	})
}

// A sink must not be called once Do has returned to that caller.
func TestDo_NoSinkCallAfterDoReturned(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newFlights(0, nil)
		step := make(chan struct{})
		var emit func(string)
		ready := make(chan struct{})
		fn := func(_ context.Context, e func(string)) (int, error) {
			emit = e
			close(ready)
			<-step
			return 1, nil
		}
		var late atomic.Bool
		var returned atomic.Bool
		ctxA, cancelA := context.WithCancel(context.Background())
		a := callDo(f, ctxA, "k", func(string) {
			if returned.Load() {
				late.Store(true)
			}
		}, fn)
		b := callDo(f, context.Background(), "k", nil, fn)
		synctest.Wait()
		<-ready
		cancelA()
		get(t, a)
		returned.Store(true)
		emit("after")
		close(step)
		get(t, b)
		if late.Load() {
			t.Error("sink called after its Do returned")
		}
	})
}

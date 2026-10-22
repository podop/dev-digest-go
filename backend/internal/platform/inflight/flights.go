package inflight

import (
	"context"
	"errors"
	"fmt"
	"runtime/debug"
	"sync"
	"time"
)

// ErrBudgetExceeded is the default cancellation cause of a flight that outlived its budget.
var ErrBudgetExceeded = errors.New("in-flight work exceeded its budget")

// FlightsConfig configures a Flights.
type FlightsConfig struct {
	// Budget bounds one flight from the moment it starts; 0 means no budget.
	Budget time.Duration
	// BudgetErr is the cancellation cause, and so the error every caller gets, when Budget
	// elapses. Nil means ErrBudgetExceeded.
	BudgetErr error
}

// PanicError is what Do panics with in every caller that waits for a flight whose function
// panicked. The panic happens on a goroutine Flights owns, where nobody could recover it and
// the process would die; it is caught there and re-raised on the callers (whose request
// recovery handles it) with the stack of the original panic.
type PanicError struct {
	Value any
	Stack []byte
}

func (e *PanicError) Error() string { return fmt.Sprintf("panic in in-flight work: %v", e.Value) }

// Unwrap exposes the panic value when it was an error.
func (e *PanicError) Unwrap() error {
	err, _ := e.Value.(error)
	return err
}

// Flights shares one run of a function per key among concurrent callers (single flight). T is
// the result, E the type of the progress events the function emits to the attached callers.
// The zero value is not usable, use NewFlights.
type Flights[T, E any] struct {
	cfg FlightsConfig

	mu      sync.Mutex
	flights map[string]*flight[T, E]
	stopped bool
	active  drain
}

// NewFlights returns an empty registry.
func NewFlights[T, E any](cfg FlightsConfig) *Flights[T, E] {
	if cfg.BudgetErr == nil {
		cfg.BudgetErr = ErrBudgetExceeded
	}
	return &Flights[T, E]{cfg: cfg, flights: make(map[string]*flight[T, E])}
}

type flight[T, E any] struct {
	ctx    context.Context
	cancel context.CancelCauseFunc
	done   chan struct{}

	// Written by the flight goroutine before done is closed, read by callers after.
	res T
	err error
	pan *PanicError

	// Guarded by Flights.mu.
	subs        map[*sub[E]]struct{}
	cancellable int  // attached callers whose context can end
	plain       int  // attached callers whose context never ends
	abandoned   bool // every caller left and the flight was cancelled for that reason
}

// sub is one attached caller's event sink. mu makes "no sink call after Do returned" hold.
type sub[E any] struct {
	mu          sync.Mutex
	sink        func(E)
	cancellable bool
	detached    bool
}

func (s *sub[E]) deliver(e E) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.detached && s.sink != nil {
		s.sink(e)
	}
}

func (s *sub[E]) detach() {
	s.mu.Lock()
	s.detached = true
	s.mu.Unlock()
}

// Do runs fn for key, or joins the flight already running for key, and returns its result.
//
//   - fn runs on its own goroutine with a context that keeps the values of the first caller's
//     but ends when the budget elapses (cause: BudgetErr), when every attached caller that
//     has a cancellation has cancelled (cause: that caller's cancellation cause), or on
//     Shutdown (cause: ErrShutdown). A caller whose context can never end keeps the flight
//     alive. When fn returns a context error because its context ended, every caller gets the
//     cancellation cause instead, so the budget and cancellation messages reach them intact.
//   - emit sends an event to every attached caller's sink, in call order; a joiner receives the
//     events emitted from then on. Sinks run on the flight's goroutine, one event at a time per
//     caller; a sink must not block for long and a panic in one counts as a panic of fn. sink
//     may be nil. No sink is called after Do has returned to its caller.
//   - a caller whose ctx ends stops waiting at once and gets context.Cause(ctx); the flight
//     goes on while other callers wait. A caller that joins while the flight is being wound
//     down because everyone left waits for it to end and then starts a fresh flight.
//   - a panic in fn is re-raised as *PanicError in every waiting caller.
//   - after Shutdown, Do returns ErrShutdown without running fn.
//
// The entry is removed before Do returns to the callers of a finished flight.
func (f *Flights[T, E]) Do(
	ctx context.Context,
	key string,
	sink func(E),
	fn func(ctx context.Context, emit func(E)) (T, error),
) (T, error) {
	var zero T
	for {
		if err := ctx.Err(); err != nil {
			return zero, context.Cause(ctx)
		}
		f.mu.Lock()
		if f.stopped {
			f.mu.Unlock()
			return zero, ErrShutdown
		}
		fl := f.flights[key]
		if fl != nil && fl.abandoned {
			f.mu.Unlock()
			select {
			case <-fl.done:
				continue
			case <-ctx.Done():
				return zero, context.Cause(ctx)
			}
		}
		if fl == nil {
			fl = f.start(ctx, key, fn)
		}
		s := &sub[E]{sink: sink, cancellable: ctx.Done() != nil}
		fl.subs[s] = struct{}{}
		if s.cancellable {
			fl.cancellable++
		} else {
			fl.plain++
		}
		f.mu.Unlock()
		return f.wait(ctx, fl, s)
	}
}

// start registers and launches a flight. f.mu is held.
func (f *Flights[T, E]) start(ctx context.Context, key string, fn func(context.Context, func(E)) (T, error)) *flight[T, E] {
	fctx, cancel := context.WithCancelCause(context.WithoutCancel(ctx))
	stopBudget := func() {}
	if f.cfg.Budget > 0 {
		var cancelBudget context.CancelFunc
		fctx, cancelBudget = context.WithTimeoutCause(fctx, f.cfg.Budget, f.cfg.BudgetErr)
		stopBudget = cancelBudget
	}
	fl := &flight[T, E]{ctx: fctx, cancel: cancel, done: make(chan struct{}), subs: make(map[*sub[E]]struct{})}
	f.flights[key] = fl
	f.active.inc()
	go f.run(key, fl, fn, stopBudget)
	return fl
}

func (f *Flights[T, E]) run(key string, fl *flight[T, E], fn func(context.Context, func(E)) (T, error), stopBudget func()) {
	defer func() {
		if r := recover(); r != nil {
			fl.pan = &PanicError{Value: r, Stack: debug.Stack()}
		}
		if fl.err != nil && fl.ctx.Err() != nil &&
			(errors.Is(fl.err, context.Canceled) || errors.Is(fl.err, context.DeadlineExceeded)) {
			fl.err = context.Cause(fl.ctx)
		}
		f.mu.Lock()
		delete(f.flights, key)
		f.mu.Unlock()
		stopBudget()
		fl.cancel(nil)
		close(fl.done)
		f.active.dec()
	}()
	fl.res, fl.err = fn(fl.ctx, func(e E) { f.emit(fl, e) })
}

func (f *Flights[T, E]) emit(fl *flight[T, E], e E) {
	f.mu.Lock()
	subs := make([]*sub[E], 0, len(fl.subs))
	for s := range fl.subs {
		subs = append(subs, s)
	}
	f.mu.Unlock()
	for _, s := range subs {
		s.deliver(e)
	}
}

func (f *Flights[T, E]) wait(ctx context.Context, fl *flight[T, E], s *sub[E]) (T, error) {
	select {
	case <-fl.done:
		s.detach()
		if fl.pan != nil {
			panic(fl.pan)
		}
		return fl.res, fl.err
	case <-ctx.Done():
		cause := context.Cause(ctx)
		s.detach()
		f.mu.Lock()
		delete(fl.subs, s)
		if s.cancellable {
			fl.cancellable--
		} else {
			fl.plain--
		}
		if fl.cancellable == 0 && fl.plain == 0 && !fl.abandoned {
			fl.abandoned = true
			fl.cancel(cause)
		}
		f.mu.Unlock()
		var zero T
		return zero, cause
	}
}

// Len is the number of flights currently registered.
func (f *Flights[T, E]) Len() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.flights)
}

// Shutdown refuses new flights, cancels every running one (cause ErrShutdown) and waits until
// they have all returned, or ctx ends (ErrNotDrained).
func (f *Flights[T, E]) Shutdown(ctx context.Context) error {
	f.mu.Lock()
	f.stopped = true
	for _, fl := range f.flights {
		fl.cancel(ErrShutdown)
	}
	f.mu.Unlock()
	if err := f.active.wait(ctx); err != nil {
		return fmt.Errorf("%d flights still running: %w", f.Len(), err)
	}
	return nil
}

package executor

import (
	"context"
	"fmt"
	"log/slog"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"dev-digest-go/backend/internal/evals/app"
	"dev-digest-go/backend/internal/evals/domain"
)

// Executor runs eval suite runs in the background (spec 7.5.4): Launch starts a run on its own
// goroutine, which works through the run's cases on a bounded set of workers; Shutdown aborts the
// runs and waits for them to record themselves. It mirrors the review executor's lifecycle: a base
// context detached from the request, a sticky stop flag, a drain with a deadline.
//
// This file holds the constructor, Launch and the run loop; case.go one case, record.go the
// recording of a result, finish.go the run's final state and shutdown.go the orderly stop.
type Executor struct {
	store     Store
	providers Providers
	engine    Engine
	clock     app.Clock
	log       *slog.Logger
	opts      Options

	// stopCtx ends when Shutdown begins. It aborts the cases in flight and stops the pick-up of
	// further ones; the database writes use the launch context, which it does not end, so an
	// aborted run still records itself.
	stopCtx context.Context
	stop    context.CancelFunc

	mu      sync.Mutex // guards stopped and the wg.Go calls against Shutdown's wait
	stopped bool
	wg      sync.WaitGroup // one entry per run goroutine
}

var _ app.Launcher = (*Executor)(nil)

// New builds an Executor. A missing required dependency is a programming error and panics.
func New(d Deps, opts Options) *Executor {
	if d.Store == nil || d.Providers == nil || d.Engine == nil {
		panic("evals/app/executor: New needs a store, providers and an engine")
	}
	if opts.CaseBudget <= 0 {
		opts.CaseBudget = domain.CaseBudget
	}
	if opts.Cases <= 0 {
		opts.Cases = domain.CaseConcurrency
	}
	if opts.Drain <= 0 {
		opts.Drain = domain.ShutdownDrain
	}
	if d.Clock == nil {
		d.Clock = time.Now
	}
	if d.Log == nil {
		d.Log = slog.New(slog.DiscardHandler)
	}
	// The executor owns its lifecycle: this context is the stop flag of its goroutines and has
	// no parent to inherit a request's cancellation from.
	stopCtx, stop := context.WithCancel(context.Background())
	return &Executor{
		store: d.Store, providers: d.Providers, engine: d.Engine, clock: d.Clock, log: d.Log, opts: opts,
		stopCtx: stopCtx, stop: stop,
	}
}

// Launch runs the suite run in the background and returns at once. The run detaches from ctx's
// cancellation (it keeps ctx's values); it ends only through its cases, a store failure or
// Shutdown. Failures are recorded on the run, never returned. After Shutdown a launch is refused:
// the run stays running until the next start reaps it.
func (e *Executor) Launch(ctx context.Context, run domain.Run) {
	base := context.WithoutCancel(ctx)
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.stopped {
		e.log.Warn("eval run: launch refused, executor is shutting down", "runId", run.ID)
		return
	}
	e.wg.Go(func() { e.execute(base, run) })
}

// workerPanic is a panic of a case worker, carried to the run goroutine with the worker's stack.
type workerPanic struct {
	val   any
	stack []byte
}

// internalError is the error text of a run whose execution crashed: the details go to the log.
const internalError = "internal error"

// runExec is the state of one run in flight.
type runExec struct {
	run   domain.Run
	start time.Time

	mu    sync.Mutex
	next  int // index of the next case to pick up
	slots []slot
	crash error        // the first store failure: no further cases are picked up
	boom  *workerPanic // the first worker panic
}

// slot is the outcome of the case at one index of the run's case list.
type slot struct {
	done    bool
	outcome domain.CaseOutcome
}

// execute is the goroutine of one run. A panic anywhere in it is logged and ends the run failed.
func (e *Executor) execute(base context.Context, run domain.Run) {
	x := &runExec{run: run, start: e.clock(), slots: make([]slot, len(run.CaseIDs))}
	defer func() {
		if r := recover(); r != nil {
			e.crashed(base, x, r)
		}
	}()
	e.runCases(base, x)
	e.finish(base, x, e.verdict(x))
}

// runCases works through the run's cases on Options.Cases workers. A worker that panics stops the
// pool; once the workers are done the panic is raised again here, on the run goroutine, which
// owns the recovery: a worker's own goroutine has no one to recover it and the process would die.
func (e *Executor) runCases(base context.Context, x *runExec) {
	e.log.Info("eval run started",
		"runId", x.run.ID, "ownerKind", string(x.run.OwnerKind), "skillId", ptrAny(x.run.SkillID),
		"skillVersion", ptrAny(x.run.SkillVersion), "agentId", x.run.AgentID, "agentVersion", x.run.AgentVersion,
		"cases", len(x.run.CaseIDs))

	// The first cases are handed out before any worker runs, so a run starts min(Cases, n) cases
	// at once whatever the scheduler does; the workers then pick up the rest.
	var firsts []int
	for range min(e.opts.Cases, len(x.run.CaseIDs)) {
		if i, ok := e.claim(x); ok {
			firsts = append(firsts, i)
		}
	}
	var wg sync.WaitGroup
	for _, first := range firsts {
		wg.Go(func() { e.worker(base, x, first) })
	}
	wg.Wait()
	if x.boom != nil {
		panic(*x.boom)
	}
}

// worker runs the case it was given, then picks cases until none is left, a store failure
// happened or the executor stops.
func (e *Executor) worker(base context.Context, x *runExec, first int) {
	defer func() {
		if r := recover(); r != nil {
			x.mu.Lock()
			if x.boom == nil {
				x.boom = &workerPanic{val: r, stack: debug.Stack()}
			}
			x.mu.Unlock()
		}
	}()
	for i, ok := first, true; ok; i, ok = e.claim(x) {
		outcome, counted, err := e.runCase(base, x.run, x.run.CaseIDs[i])
		x.mu.Lock()
		switch {
		case err != nil:
			if x.crash == nil {
				x.crash = err // the store is failing: stop picking up cases
			}
		case counted:
			x.slots[i] = slot{done: true, outcome: outcome}
		}
		x.mu.Unlock()
	}
}

// claim hands out the next case index, or false once the cases are used up, a worker failed or
// the executor is stopping.
func (e *Executor) claim(x *runExec) (int, bool) {
	x.mu.Lock()
	defer x.mu.Unlock()
	if x.crash != nil || x.boom != nil || e.stopCtx.Err() != nil || x.next >= len(x.run.CaseIDs) {
		return 0, false
	}
	i := x.next
	x.next++
	return i, true
}

// outcomes are the outcomes of the cases that counted, in case order.
func (x *runExec) outcomes() []domain.CaseOutcome {
	x.mu.Lock()
	defer x.mu.Unlock()
	out := make([]domain.CaseOutcome, 0, len(x.slots))
	for _, s := range x.slots {
		if s.done {
			out = append(out, s.outcome)
		}
	}
	return out
}

// crashed handles a panic of the run goroutine: it logs it (redacted, with the stack) and ends the
// run failed with a generic note so it does not stay running until the next start. It never
// panics itself.
func (e *Executor) crashed(base context.Context, x *runExec, r any) {
	stack := debug.Stack()
	if wp, ok := r.(workerPanic); ok {
		r, stack = wp.val, wp.stack
	}
	e.log.Error("eval run: background execution crashed",
		"runId", x.run.ID, "err", e.logText(fmt.Sprint(r)), "stack", string(stack))
	defer func() {
		if r := recover(); r != nil {
			e.log.Error("eval run: could not record the final state", "runId", x.run.ID, "err", e.logText(fmt.Sprint(r)))
		}
	}()
	msg := internalError
	e.finish(base, x, domain.RunVerdict{Status: domain.RunFailed, Error: &msg})
}

// redact scrubs provider keys out of a text.
func (e *Executor) redact(s string) string { return e.providers.Redact(s) }

// logText is a text safe to put in a log line: redacted, first line only.
func (e *Executor) logText(s string) string {
	line, _, _ := strings.Cut(e.redact(s), "\n")
	return line
}

// sinceMS is the milliseconds elapsed since t on the executor's clock.
func (e *Executor) sinceMS(t time.Time) int { return int(e.clock().Sub(t).Milliseconds()) }

// ptrAny is the value a pointer points to, or nil: log fields show a missing value as null.
func ptrAny[T any](p *T) any {
	if p == nil {
		return nil
	}
	return *p
}

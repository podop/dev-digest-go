// Package executor runs the agent runs of a review request in the background: it loads the
// request's diff once, runs the agents in parallel (a bounded number at a time) through the review
// engine, streams each run's Live Log over the run bus, persists the review in one transaction and
// records how every run ended. It owns all the goroutines it starts; Shutdown cancels them and
// waits.
//
// This file holds the constructor and Start; batch.go the per-request work, run.go one agent
// run, ending.go the failed, cancelled and persisted outcomes, mirror.go the run logger and
// shutdown.go the orderly stop.
package executor

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	reviewerapp "dev-digest-go/backend/internal/reviewer/app"
	reviewer "dev-digest-go/backend/internal/reviewer/domain"
	"dev-digest-go/backend/internal/reviews/app"
	"dev-digest-go/backend/internal/reviews/domain"
)

// DefaultAgentConcurrency is the number of agent runs of one request in flight when
// Options.AgentConcurrency is 0 (config REVIEW_AGENT_CONCURRENCY).
const DefaultAgentConcurrency = 4

// Deps are the collaborators of an Executor; every field but Clock and Log is required.
type Deps struct {
	Store          app.Store
	Tx             app.Tx
	Bus            app.Bus
	Providers      app.Providers
	RepoIntel      app.RepoIntel
	ProjectContext app.ProjectContext
	// Engine is reviewer/app.Run in production.
	Engine app.Engine
	// Clock times the runs; nil = time.Now.
	Clock app.Clock
	// Log receives the mirrored run events and the executor's own lines; nil = discard.
	Log *slog.Logger
}

// Options tune an Executor. Zero values select the defaults.
type Options struct {
	// AgentConcurrency bounds the agent runs of one request in flight; 0 = DefaultAgentConcurrency.
	AgentConcurrency int
	// MapConcurrency is passed to the engine as the chunk calls in flight; 0 = the engine's default.
	MapConcurrency int
	// Drain is how long Shutdown waits for cancelled runs to record themselves; 0 = domain.ShutdownDrain.
	Drain time.Duration
}

// Executor implements app.Executor.
type Executor struct {
	store     app.Store
	tx        app.Tx
	bus       app.Bus
	providers app.Providers
	repoIntel app.RepoIntel
	projCtx   app.ProjectContext
	engine    app.Engine
	clock     app.Clock
	log       *slog.Logger
	opts      Options

	mu      sync.Mutex // guards stopped and the wg.Go calls against Shutdown's wait
	stopped bool
	wg      sync.WaitGroup // one entry per batch goroutine
}

var _ app.Executor = (*Executor)(nil)

// New builds an Executor. A missing required dependency is a programming error and panics.
func New(d Deps, opts Options) *Executor {
	if d.Store == nil || d.Tx == nil || d.Bus == nil || d.Providers == nil || d.RepoIntel == nil ||
		d.ProjectContext == nil || d.Engine == nil {
		panic("reviews/app/executor: New needs a store, a transaction runner, a bus, providers, repo intel, a project-context resolver and an engine")
	}
	if opts.AgentConcurrency <= 0 {
		opts.AgentConcurrency = DefaultAgentConcurrency
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
	return &Executor{
		store: d.Store, tx: d.Tx, bus: d.Bus, providers: d.Providers, repoIntel: d.RepoIntel,
		projCtx: d.ProjectContext, engine: d.Engine, clock: d.Clock, log: d.Log, opts: opts,
	}
}

// Start runs the batch in the background and returns at once. The batch detaches from ctx's
// cancellation (it keeps ctx's values); each run ends only through its own cancel or Shutdown.
// Failures are recorded on the runs, never returned. After Shutdown a batch is refused: its runs
// stay running until the next start reaps them.
func (e *Executor) Start(ctx context.Context, b domain.Batch) {
	if len(b.Jobs) == 0 {
		return
	}
	base := context.WithoutCancel(ctx)
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.stopped {
		e.log.Warn("review: batch refused, executor is shutting down", "prId", b.Pull.ID, "runs", len(b.Jobs))
		return
	}
	e.wg.Go(func() { e.runBatch(base, b) })
}

// isStopped reports whether Shutdown has begun.
func (e *Executor) isStopped() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.stopped
}

// sinceMS is the milliseconds elapsed since t on the executor's clock.
func (e *Executor) sinceMS(t time.Time) int { return int(e.clock().Sub(t).Milliseconds()) }

// redact scrubs provider keys out of a text.
func (e *Executor) redact(s string) string { return e.providers.Redact(s) }

// engineOptions are the options of one engine run.
func (e *Executor) engineOptions(cancelled func() bool) reviewerapp.Options {
	return reviewerapp.Options{MapConcurrency: e.opts.MapConcurrency, Cancelled: cancelled}
}

// strategyOf is the agent's strategy, auto when it has none.
func strategyOf(a domain.ReviewAgent) reviewer.Strategy {
	if a.Strategy == "" {
		return reviewer.StrategyAuto
	}
	return a.Strategy
}

// panicError is what a recovered panic becomes on a run: the note stays generic, the details go
// to the log.
type panicError struct{}

func (panicError) Error() string { return "internal error" }

func panicText(r any) string { return fmt.Sprint(r) }

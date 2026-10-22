// Package jobs runs background work (clone, refresh, later PR import and indexing) on a
// concurrency-limited in-process pool and mirrors every job into a store (the `jobs` table).
//
// Enqueue inserts a `queued` row and returns at once; a job then moves to `running`, and ends
// `done` or `failed` with the error text. Each attempt has a timeout, and transient failures
// are retried. Shutdown drops jobs that have not started (their rows stay `queued`) and aborts
// running ones through their context.
package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"dev-digest-go/backend/internal/platform/retry"
)

// Defaults match the Node runner (platform/jobs.ts) and FR32.
const (
	DefaultConcurrency = 3
	DefaultTimeout     = 120 * time.Second
	DefaultRetries     = 2
	// DefaultShutdownTimeout is how long the composition root gives Shutdown (FR43).
	DefaultShutdownTimeout = 10 * time.Second

	// storeWriteTimeout bounds each status write made after a job's context may already be gone.
	storeWriteTimeout = 5 * time.Second
)

// Status is the lifecycle state stored in the `jobs.status` column.
type Status string

// The four states allowed by the jobs_status_chk constraint.
const (
	StatusQueued  Status = "queued"
	StatusRunning Status = "running"
	StatusDone    Status = "done"
	StatusFailed  Status = "failed"
)

var (
	// ErrUnknownKind is returned by Enqueue when no handler is registered for the kind.
	ErrUnknownKind = errors.New("no job handler registered")
	// ErrShuttingDown is returned by Enqueue once Shutdown has been called.
	ErrShuttingDown = errors.New("job runner is shutting down")
	// ErrShutdownTimeout is returned by Shutdown when running jobs did not settle in time.
	ErrShutdownTimeout = errors.New("job runner did not stop in time")
	// ErrNotFound is returned by a Store when the job row does not exist.
	ErrNotFound = errors.New("job not found")
)

// Handler does the work of one job. It must return promptly once ctx ends: ctx is cancelled when
// the attempt times out or the runner shuts down. A handler that ignores ctx keeps its slot.
type Handler func(ctx context.Context, payload json.RawMessage) error

// Store persists the job mirror. Implemented in adapters/postgres.
type Store interface {
	// Insert adds a `queued` row and returns its id.
	Insert(ctx context.Context, workspaceID, kind string, payload json.RawMessage) (string, error)
	// MarkRunning sets status `running` and started_at.
	MarkRunning(ctx context.Context, id string, startedAt time.Time) error
	// SetAttempts records the retry number reached so far.
	SetAttempts(ctx context.Context, id string, attempts int) error
	// Finish sets the final status (`done` or `failed`), attempts, finished_at and the error text
	// (empty for no error).
	Finish(ctx context.Context, id string, status Status, attempts int, errMsg string, finishedAt time.Time) error
}

// Options configures a Runner. Zero values select the defaults above.
type Options struct {
	// Concurrency is the maximum number of jobs running at once.
	Concurrency int
	// Timeout limits each attempt.
	Timeout time.Duration
	// Retries is the number of re-runs after a transient failure: 0 selects DefaultRetries,
	// a negative value disables retries.
	Retries int
	// RetryBaseDelay is the first backoff step; zero selects the retry package default.
	RetryBaseDelay time.Duration
	// Logger receives one line per finished job (NFR9). Nil discards.
	Logger *slog.Logger
	// Now supplies timestamps; nil uses time.Now.
	Now func() time.Time
}

type job struct {
	id      string
	kind    string
	payload json.RawMessage
	handler Handler
	timeout time.Duration // per-attempt limit of this job's kind
}

// registration is what Register stores for a kind; timeout 0 means the runner's default.
type registration struct {
	handler Handler
	timeout time.Duration
}

// RegisterOption tunes one kind at Register time.
type RegisterOption func(*registration)

// WithTimeout gives the kind its own per-attempt limit instead of the runner's. A value <= 0
// keeps the runner's default.
func WithTimeout(d time.Duration) RegisterOption {
	return func(reg *registration) {
		if d > 0 {
			reg.timeout = d
		}
	}
}

// Runner is the in-process job pool. Create it with New and stop it with Shutdown.
type Runner struct {
	store      Store
	concurrent int
	timeout    time.Duration
	retries    int
	baseDelay  time.Duration
	log        *slog.Logger
	now        func() time.Time

	// ctx is the lifecycle root of every job; cancel is called by Shutdown.
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup // running job goroutines

	mu          sync.Mutex
	handlers    map[string]registration
	pending     []job
	running     int
	outstanding int // accepted jobs not yet finished or dropped
	closed      bool
	idleWaiters []chan struct{}
}

// New builds a Runner on a store.
func New(store Store, opts Options) *Runner {
	r := &Runner{
		store:      store,
		concurrent: opts.Concurrency,
		timeout:    opts.Timeout,
		retries:    opts.Retries,
		baseDelay:  opts.RetryBaseDelay,
		log:        opts.Logger,
		now:        opts.Now,
		handlers:   make(map[string]registration),
	}
	if r.concurrent <= 0 {
		r.concurrent = DefaultConcurrency
	}
	if r.timeout <= 0 {
		r.timeout = DefaultTimeout
	}
	switch {
	case r.retries == 0:
		r.retries = DefaultRetries
	case r.retries < 0:
		r.retries = 0
	}
	if r.log == nil {
		r.log = slog.New(slog.DiscardHandler)
	}
	if r.now == nil {
		r.now = time.Now
	}
	// The runner owns its lifecycle: jobs outlive the request that enqueued them and end only
	// through Shutdown.
	r.ctx, r.cancel = context.WithCancel(context.Background())
	return r
}

// Register binds a handler to a job kind. Kinds registered without WithTimeout run under the
// runner's per-attempt timeout. It panics on a nil handler or a duplicate kind, so a wiring
// mistake fails at startup.
func (r *Runner) Register(kind string, h Handler, opts ...RegisterOption) {
	if h == nil {
		panic("jobs: nil handler for kind " + kind)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, dup := r.handlers[kind]; dup {
		panic("jobs: duplicate handler for kind " + kind)
	}
	reg := registration{handler: h}
	for _, opt := range opts {
		opt(&reg)
	}
	r.handlers[kind] = reg
}

// Registered reports whether a handler is registered for kind and, if so, the per-attempt limit
// its jobs run under: the kind's own WithTimeout, else the runner's timeout.
func (r *Runner) Registered(kind string) (time.Duration, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	reg, ok := r.handlers[kind]
	if !ok {
		return 0, false
	}
	if reg.timeout > 0 {
		return reg.timeout, true
	}
	return r.timeout, true
}

// Enqueue stores a `queued` row for the job and schedules it; it returns the job id without
// waiting for the work. payload is marshalled to JSON. ctx bounds only the insert: the job runs
// on the runner's own lifecycle. It returns ErrUnknownKind when no handler is registered and
// ErrShuttingDown after Shutdown.
func (r *Runner) Enqueue(ctx context.Context, workspaceID, kind string, payload any) (string, error) {
	r.mu.Lock()
	reg, ok := r.handlers[kind]
	closed := r.closed
	r.mu.Unlock()
	if !ok {
		return "", fmt.Errorf("enqueue %q: %w", kind, ErrUnknownKind)
	}
	if closed {
		return "", fmt.Errorf("enqueue %q: %w", kind, ErrShuttingDown)
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("enqueue %q: marshal payload: %w", kind, err)
	}
	id, err := r.store.Insert(ctx, workspaceID, kind, raw)
	if err != nil {
		return "", fmt.Errorf("enqueue %q: insert job: %w", kind, err)
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		// Shutdown began during the insert; the row stays queued like any other undone job.
		return "", fmt.Errorf("enqueue %q: %w", kind, ErrShuttingDown)
	}
	timeout := reg.timeout
	if timeout <= 0 {
		timeout = r.timeout
	}
	r.pending = append(r.pending, job{id: id, kind: kind, payload: raw, handler: reg.handler, timeout: timeout})
	r.outstanding++
	r.dispatchLocked()
	return id, nil
}

// Wait blocks until every accepted job has finished or been dropped by Shutdown, or ctx ends.
func (r *Runner) Wait(ctx context.Context) error {
	r.mu.Lock()
	if r.outstanding == 0 {
		r.mu.Unlock()
		return nil
	}
	ch := make(chan struct{})
	r.idleWaiters = append(r.idleWaiters, ch)
	r.mu.Unlock()
	select {
	case <-ch:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Shutdown stops the runner: it refuses new jobs, drops jobs that have not started (their rows
// stay `queued`), cancels running handlers and waits up to timeout for them to settle and write
// their final status. It returns ErrShutdownTimeout when they did not; those rows may stay
// `running`. It is safe to call more than once.
func (r *Runner) Shutdown(timeout time.Duration) error {
	r.mu.Lock()
	r.closed = true
	r.outstanding -= len(r.pending)
	r.pending = nil
	r.signalIdleLocked()
	r.mu.Unlock()
	r.cancel()

	settled := make(chan struct{})
	go func() {
		r.wg.Wait()
		close(settled)
	}()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-settled:
		return nil
	case <-timer.C:
		return ErrShutdownTimeout
	}
}

// dispatchLocked starts pending jobs while there are free slots. r.mu must be held.
func (r *Runner) dispatchLocked() {
	for !r.closed && r.running < r.concurrent && len(r.pending) > 0 {
		j := r.pending[0]
		r.pending[0] = job{}
		r.pending = r.pending[1:]
		r.running++
		r.wg.Go(func() { r.execute(j) })
	}
}

func (r *Runner) signalIdleLocked() {
	if r.outstanding > 0 {
		return
	}
	for _, ch := range r.idleWaiters {
		close(ch)
	}
	r.idleWaiters = nil
}

func (r *Runner) execute(j job) {
	defer func() {
		r.mu.Lock()
		r.running--
		r.outstanding--
		r.dispatchLocked()
		r.signalIdleLocked()
		r.mu.Unlock()
	}()

	started := r.now()
	if err := r.write(func(ctx context.Context) error { return r.store.MarkRunning(ctx, j.id, started) }); err != nil {
		r.log.Error("job not started: mark running failed", "kind", j.kind, "job_id", j.id, "error", err)
		return
	}

	attempts := 0
	opts := retry.Options{
		Retries:   r.retries,
		BaseDelay: r.baseDelay,
		// A runner that is shutting down never retries (Node parity).
		IsRetryable: func(err error) bool { return r.ctx.Err() == nil && retry.IsTransient(err) },
		OnRetry: func(n int, _ error) {
			attempts = n
			if err := r.write(func(ctx context.Context) error { return r.store.SetAttempts(ctx, j.id, n) }); err != nil {
				r.log.Warn("job attempts not recorded", "kind", j.kind, "job_id", j.id, "attempts", n, "error", err)
			}
		},
	}
	err := retry.Do(r.ctx, opts, func(ctx context.Context) error { return r.attempt(ctx, j) })

	status, errMsg := StatusDone, ""
	if err == nil {
		attempts = 1 // Node parity: a success records attempts=1 whatever the retry count was
	} else {
		status, errMsg = StatusFailed, failureText(r.ctx, err)
	}
	finished := r.now()
	if werr := r.write(func(ctx context.Context) error {
		return r.store.Finish(ctx, j.id, status, attempts, errMsg, finished)
	}); werr != nil {
		r.log.Error("job final status not stored", "kind", j.kind, "job_id", j.id, "status", status, "error", werr)
	}

	line := []any{"kind", j.kind, "job_id", j.id, "status", string(status), "attempts", attempts,
		"duration_ms", finished.Sub(started).Milliseconds()}
	if errMsg != "" {
		line = append(line, "error", errMsg)
	}
	r.log.Info("job finished", line...)
}

// attempt runs the handler once under the per-attempt timeout, turning a panic or a timeout
// into an error.
func (r *Runner) attempt(ctx context.Context, j job) (err error) {
	actx, cancel := context.WithTimeout(ctx, j.timeout)
	defer cancel()
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("handler panic: %v", p)
		}
	}()
	err = j.handler(actx, j.payload)
	if err != nil && actx.Err() != nil && ctx.Err() == nil {
		return fmt.Errorf("attempt timed out after %s: %w", j.timeout, context.DeadlineExceeded)
	}
	return err
}

// write runs a store write on a context that survives Shutdown, with its own deadline.
func (r *Runner) write(fn func(context.Context) error) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.ctx), storeWriteTimeout)
	defer cancel()
	return fn(ctx)
}

func failureText(lifecycle context.Context, err error) string {
	if lifecycle.Err() != nil && errors.Is(err, context.Canceled) {
		return "aborted by shutdown: " + err.Error()
	}
	return err.Error()
}

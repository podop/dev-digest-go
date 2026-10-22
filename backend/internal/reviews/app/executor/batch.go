package executor

import (
	"context"
	"encoding/json"
	"errors"
	"runtime/debug"
	"sync"

	reviewer "dev-digest-go/backend/internal/reviewer/domain"
	"dev-digest-go/backend/internal/reviews/app"
	"dev-digest-go/backend/internal/reviews/domain"
)

// batchRun is what the agent runs of one request share: the request's rows, the diff (loaded
// once) and the logger that fans shared events out to every run.
type batchRun struct {
	b      domain.Batch
	diff   reviewer.Diff
	shared *runLog
	// intent and intentTrace are the request's intent pre-work (spec 7.5): intent is nil when it
	// is unavailable or the switch is off, intentTrace nil only when the pre-work did not run.
	// Both are shared read-only by every run.
	intent      *reviewer.Intent
	intentTrace *domain.IntentTrace
}

// runBatch executes one request: load the diff, then run the agents, at most AgentConcurrency at
// once. A failure of one agent never stops the others. A panic outside a run is logged and leaves
// the unrecorded runs running until they are cancelled or reaped at the next start (EC4020).
func (e *Executor) runBatch(ctx context.Context, b domain.Batch) {
	defer func() {
		if r := recover(); r != nil {
			e.log.Error("review: batch crashed", "prId", b.Pull.ID, "panic", e.logText(panicText(r)), "stack", string(debug.Stack()))
		}
	}()

	runIDs := make([]string, len(b.Jobs))
	for i, j := range b.Jobs {
		runIDs[i] = j.RunID
	}
	shared := newRunLog(e.bus, e.redact, e.log, runIDs, "prId", b.Pull.ID)

	diff, err := e.loadDiff(ctx, b, shared)
	if err != nil {
		msg := domain.DiffFailedMsg(err.Error())
		shared.errorf(msg)
		e.failAll(ctx, b, shared, msg)
		return
	}
	shared.info(domain.DiffReadyMsg(len(diff.Files), len(b.Jobs)), nil)

	br := &batchRun{b: b, diff: diff, shared: shared}
	br.intent, br.intentTrace = e.resolveIntent(ctx, b, diff, shared)
	sem := make(chan struct{}, e.opts.AgentConcurrency)
	var jobs sync.WaitGroup
	defer jobs.Wait()
	for _, job := range b.Jobs {
		sem <- struct{}{}
		jobs.Go(func() {
			defer func() { <-sem }()
			e.runJob(ctx, br, job)
		})
	}
}

// resolveIntent is the request's shared pre-work (spec 7.5): once, after "Diff ready" and before
// the first agent run, it asks the intent resolver for the PR intent and publishes the resolver's
// events to every queued run. It never fails the review: a resolver that fails, returns no trace
// or panics becomes an unavailable result with a warning event. The derivation's context ends
// only when every run of the request has been cancelled (a single cancel must not starve the
// others) or the executor shuts down (which cancels every run). With no resolver it does nothing.
func (e *Executor) resolveIntent(ctx context.Context, b domain.Batch, diff reviewer.Diff, shared *runLog) (*reviewer.Intent, *domain.IntentTrace) {
	if e.intent == nil {
		return nil, nil
	}
	ictx, cancel := context.WithCancel(ctx)
	defer cancel()
	var mu sync.Mutex
	cancelled := make(map[string]struct{}, len(b.Jobs))
	for _, job := range b.Jobs {
		stop := e.bus.OnCancel(job.RunID, func() {
			mu.Lock()
			cancelled[job.RunID] = struct{}{}
			all := len(cancelled) >= len(b.Jobs)
			mu.Unlock()
			if all {
				cancel()
			}
		})
		defer stop()
	}

	paths := make([]string, len(diff.Files))
	for i, f := range diff.Files {
		paths[i] = f.Path
	}
	res, ok := e.callIntent(ictx, app.IntentRequest{
		WorkspaceID: b.WorkspaceID, Pull: b.Pull, Repo: b.Repo, ChangedFiles: paths, DiffExcerpt: diff.Raw,
	}, shared)
	if !ok {
		return nil, domain.UnavailableIntentTrace(intentPanicWarning)
	}
	switch {
	case res.Trace != nil:
		return res.Intent, res.Trace
	case res.Intent != nil: // the port promises a trace; keep the trace consistent with the section
		return res.Intent, &domain.IntentTrace{Status: domain.IntentStatusUsed}
	}
	return nil, domain.UnavailableIntentTrace(intentPanicWarning)
}

// intentPanicWarning is the warning of an intent that is unavailable because the resolver broke
// its contract (panicked, or answered neither an intent nor a trace); the details go to the log.
const intentPanicWarning = "internal error"

// intentWarningData is the data of the "warning: intent unavailable" event (spec 7.4.7).
func intentWarningData() json.RawMessage { return json.RawMessage(`{"warning":"intent_unavailable"}`) }

// callIntent calls the resolver; a panic is recovered, logged and published as the warning event
// of an unavailable intent (ok false), so the batch goes on.
func (e *Executor) callIntent(ctx context.Context, req app.IntentRequest, shared *runLog) (res app.IntentResult, ok bool) {
	defer func() {
		if r := recover(); r != nil {
			e.log.Error("review: intent resolver panicked", "prId", req.Pull.ID, "panic", e.logText(panicText(r)), "stack", string(debug.Stack()))
			shared.info("warning: intent unavailable — "+intentPanicWarning, intentWarningData())
			res, ok = app.IntentResult{}, false
		}
	}()
	return e.intent.Resolve(ctx, req, shared.intentEmit), true
}

// loadDiff reads the pull request's stored patches once and parses them (spec 7.7.1); the step is
// published to every queued run.
func (e *Executor) loadDiff(ctx context.Context, b domain.Batch, shared *runLog) (reviewer.Diff, error) {
	return step(e, shared, reviewer.EventTool, domain.LoadingDiffLabel, func() (reviewer.Diff, error) {
		files, err := e.store.PullFiles(ctx, b.Pull.ID)
		if err != nil {
			return reviewer.Diff{}, err
		}
		return reviewer.ParseUnifiedDiff(reviewer.RawDiff(files)), nil
	})
}

// failAll ends every queued run after a pre-work failure: zero usage, an ended trace, a completed
// bus. The error event is already in each buffer. A run that was cancelled meanwhile ends
// cancelled (the shutdown path has no cancel route to record it).
func (e *Executor) failAll(ctx context.Context, b domain.Batch, shared *runLog, note string) {
	for _, job := range b.Jobs {
		func() {
			defer func() {
				if r := recover(); r != nil {
					e.log.Error("review: recording a failed run crashed", "runId", job.RunID, "panic", e.logText(panicText(r)), "stack", string(debug.Stack()))
				}
			}()
			ending := domain.EndingFor(errors.New(e.redact(note)), e.bus.IsCancelled(job.RunID))
			e.endRun(ctx, endInput{
				job:      job,
				pull:     b.Pull,
				log:      shared.forRun(job.RunID),
				ending:   ending,
				announce: ending.Status == domain.StatusCancelled,
			})
		}()
	}
}

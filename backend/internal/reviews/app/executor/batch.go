package executor

import (
	"context"
	"errors"
	"runtime/debug"
	"sync"

	reviewer "dev-digest-go/backend/internal/reviewer/domain"
	"dev-digest-go/backend/internal/reviews/domain"
)

// batchRun is what the agent runs of one request share: the request's rows, the diff (loaded
// once) and the logger that fans shared events out to every run.
type batchRun struct {
	b      domain.Batch
	diff   reviewer.Diff
	shared *runLog
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

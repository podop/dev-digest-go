package executor

import (
	"context"
	"errors"
	"time"
)

// ErrNotDrained is returned by Shutdown when some runs had not recorded themselves when the drain
// ended. They stay running in the database and are reaped to failed at the next start.
var ErrNotDrained = errors.New("review runs did not finish recording in time")

// Shutdown stops the executor in order: it refuses new batches, raises the cancel flag of every
// live run (which aborts their in-flight LLM calls), waits for the runs to record themselves for at
// most Options.Drain (domain.ShutdownDrain, 5 s) or until ctx ends, and then completes every run
// bus that is left so all SSE streams end. It returns ErrNotDrained when runs were still going.
// It may be called more than once.
func (e *Executor) Shutdown(ctx context.Context) error {
	e.mu.Lock()
	e.stopped = true
	e.mu.Unlock()

	e.bus.CancelAll()

	drained := make(chan struct{})
	go func() {
		e.wg.Wait()
		close(drained)
	}()
	timer := time.NewTimer(e.opts.Drain)
	defer timer.Stop()

	var err error
	select {
	case <-drained:
	case <-timer.C:
		err = ErrNotDrained
	case <-ctx.Done():
		err = ErrNotDrained
	}
	e.bus.CompleteAll()
	return err
}

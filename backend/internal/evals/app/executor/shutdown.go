package executor

import (
	"context"
	"time"
)

// Shutdown stops the executor in order: it refuses new launches, aborts every case in flight and
// stops the pick-up of further ones, then waits for the runs to record themselves failed
// "interrupted" for at most Options.Drain (domain.ShutdownDrain, 5 s) or until ctx ends. It
// returns ErrNotDrained when runs were still going; they stay running in the database and are
// reaped at the next start. It may be called more than once.
func (e *Executor) Shutdown(ctx context.Context) error {
	e.mu.Lock()
	e.stopped = true
	e.mu.Unlock()

	e.stop()

	drained := make(chan struct{})
	go func() {
		e.wg.Wait()
		close(drained)
	}()
	timer := time.NewTimer(e.opts.Drain)
	defer timer.Stop()

	select {
	case <-drained:
		return nil
	case <-timer.C:
		return ErrNotDrained
	case <-ctx.Done():
		return ErrNotDrained
	}
}

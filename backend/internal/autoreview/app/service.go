package app

import (
	"context"
	"io"
	"log/slog"
	"time"
)

// startBudget bounds the claim, start and settle of one automatic review. It runs on a context
// detached from the poller's, so a shutdown does not cut a started review between its record and
// its runs (A4); it is also the longest an evaluation outlives its context (NFR5).
const startBudget = 3 * time.Second

// Service implements API. Evaluations are serialised per process (A5); at-most-once across
// processes is the store's unique (PR, head SHA) claim (NFR4).
type Service struct {
	deps Deps
	log  *slog.Logger
	now  Clock
	// gate is a one-slot semaphore: a channel so that waiting for the previous evaluation can be
	// abandoned when ctx is cancelled.
	gate chan struct{}
}

var _ API = (*Service)(nil)

// NewService builds the use cases from d. A nil Now reads the wall clock and a nil Log discards.
func NewService(d Deps) *Service {
	s := &Service{deps: d, log: d.Log, now: d.Now, gate: make(chan struct{}, 1)}
	if s.now == nil {
		s.now = time.Now
	}
	if s.log == nil {
		s.log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return s
}

// detached returns a context that keeps ctx's values but not its cancellation, bounded by
// startBudget.
func detached(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), startBudget)
}

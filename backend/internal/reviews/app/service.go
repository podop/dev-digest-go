package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"time"

	"dev-digest-go/backend/internal/platform/apperr"
	"dev-digest-go/backend/internal/reviews/domain"
)

// Deps are the collaborators of Service.
type Deps struct {
	Store    Store
	Tx       Tx
	Executor Executor
	Bus      Bus
	// Now is the clock of the triage timestamps and the synthetic stream event; nil = time.Now.
	Now Clock
	// Log receives the use cases' own log lines; nil discards them.
	Log *slog.Logger
}

// Service implements API and Lifecycle.
type Service struct {
	store    Store
	tx       Tx
	executor Executor
	bus      Bus
	now      Clock
	log      *slog.Logger
}

var (
	_ API       = (*Service)(nil)
	_ Lifecycle = (*Service)(nil)
)

// NewService builds a Service. A missing store, transaction runner, executor or bus is a
// programming error and panics at startup.
func NewService(d Deps) *Service {
	if d.Store == nil || d.Tx == nil || d.Executor == nil || d.Bus == nil {
		panic("reviews/app: NewService needs a store, a transaction runner, an executor and a bus")
	}
	s := &Service{store: d.Store, tx: d.Tx, executor: d.Executor, bus: d.Bus, now: d.Now, log: d.Log}
	if s.now == nil {
		s.now = time.Now
	}
	if s.log == nil {
		s.log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return s
}

// FailInterrupted marks the runs a previous process left running as failed.
func (s *Service) FailInterrupted(ctx context.Context) (int, error) {
	n, err := s.store.FailInterrupted(ctx)
	if err != nil {
		return 0, fmt.Errorf("fail interrupted runs: %w", err)
	}
	if n > 0 {
		s.log.InfoContext(ctx, "reviews: interrupted runs failed", "count", n)
	}
	return n, nil
}

// ActiveRuns returns the pull request's running runs.
func (s *Service) ActiveRuns(ctx context.Context, workspaceID, prID string) ([]domain.ActiveRun, error) {
	runs, err := s.store.ActiveRuns(ctx, workspaceID, prID)
	if err != nil {
		return nil, fmt.Errorf("list active runs of pull request %s: %w", prID, err)
	}
	if runs == nil {
		runs = []domain.ActiveRun{}
	}
	return runs, nil
}

// ListRuns returns every run of the pull request, newest first.
func (s *Service) ListRuns(ctx context.Context, workspaceID, prID string) ([]domain.RunSummary, error) {
	runs, err := s.store.ListRuns(ctx, workspaceID, prID)
	if err != nil {
		return nil, fmt.Errorf("list runs of pull request %s: %w", prID, err)
	}
	if runs == nil {
		runs = []domain.RunSummary{}
	}
	return runs, nil
}

// Trace returns a run's stored trace. A stored document that violates the contract is logged
// without its content and answered as an internal error.
func (s *Service) Trace(ctx context.Context, workspaceID, runID string) (domain.Trace, error) {
	t, err := s.store.FindTrace(ctx, workspaceID, runID)
	switch {
	case errors.Is(err, domain.ErrTraceInvalid):
		s.log.ErrorContext(ctx, "reviews: stored run trace violates the contract", "runID", runID)
		return domain.Trace{}, apperr.Internal("Internal error")
	case errors.Is(err, domain.ErrNotFound):
		return domain.Trace{}, apperr.NotFound("Run trace not found")
	case err != nil:
		return domain.Trace{}, fmt.Errorf("find trace of run %s: %w", runID, err)
	}
	return t, nil
}

// notFound turns the store's ErrNotFound into a 404 with the given message; any other error is
// wrapped with what was being done.
func notFound(err error, message, doing string) error {
	if errors.Is(err, domain.ErrNotFound) {
		return apperr.NotFound(message)
	}
	return fmt.Errorf("%s: %w", doing, err)
}

package app

import (
	"context"
	"fmt"

	reviewer "dev-digest-go/backend/internal/reviewer/domain"
	"dev-digest-go/backend/internal/reviews/domain"
)

// CancelRun cancels a run of the workspace: it tells the run, ends the stored run and completes
// the bus, so it also works for a run whose process is gone.
func (s *Service) CancelRun(ctx context.Context, workspaceID, runID string) error {
	if _, err := s.store.FindRun(ctx, workspaceID, runID); err != nil {
		return notFound(err, "Run not found", "find run "+runID)
	}
	s.bus.Publish(runID, reviewer.EventInfo, domain.CancellationRequestedMsg, nil)
	s.bus.Cancel(runID)
	if _, err := s.store.CancelIfRunning(ctx, workspaceID, runID, domain.CancelledNote); err != nil {
		return fmt.Errorf("cancel run %s: %w", runID, err)
	}
	s.bus.Complete(runID)
	return nil
}

// Events opens the event stream of a run of the workspace.
func (s *Service) Events(ctx context.Context, workspaceID, runID string) (Subscription, error) {
	run, err := s.store.FindRun(ctx, workspaceID, runID)
	if err != nil {
		return nil, notFound(err, "Run not found", "find run "+runID)
	}
	if !s.bus.Has(runID) && run.Status != domain.StatusRunning {
		return finalSubscription(domain.FinalStatusEvent(runID, run.Status, run.Error, s.now())), nil
	}
	return s.bus.Subscribe(runID), nil
}

// finalSub yields one event and ends.
type finalSub struct{ ch chan domain.RunEvent }

func finalSubscription(ev domain.RunEvent) Subscription {
	ch := make(chan domain.RunEvent, 1)
	ch <- ev
	close(ch)
	return finalSub{ch: ch}
}

func (f finalSub) C() <-chan domain.RunEvent { return f.ch }
func (finalSub) Close()                      {}

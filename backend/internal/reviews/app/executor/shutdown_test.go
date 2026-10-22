package executor

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	reviewerapp "dev-digest-go/backend/internal/reviewer/app"
	reviewer "dev-digest-go/backend/internal/reviewer/domain"
	"dev-digest-go/backend/internal/reviews/domain"
)

// blockUntilCancelled is an engine that waits in its "LLM call" for the run's context.
func blockUntilCancelled(ctx context.Context, _ reviewerapp.Input, _ reviewerapp.LLM, _ reviewerapp.Events, _ reviewerapp.Options) (reviewer.Outcome, error) {
	<-ctx.Done()
	return reviewer.Outcome{}, ctx.Err()
}

func TestShutdown_CancelsRunsRecordsThemAndCompletesTheBuses(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, Options{}, time.Now)
		h.engine.script = blockUntilCancelled
		h.exec.Start(t.Context(), h.batch(3))
		synctest.Wait() // all three are inside their LLM call

		start := time.Now()
		if err := h.exec.Shutdown(t.Context()); err != nil {
			t.Fatalf("Shutdown() = %v, want nil", err)
		}
		if elapsed := time.Since(start); elapsed != 0 {
			t.Errorf("Shutdown took %v of fake time, want it to return as soon as the runs recorded", elapsed)
		}
		for _, id := range []string{"run-1", "run-2", "run-3"} {
			rec := h.store.run(id)
			if rec.status != domain.StatusCancelled || rec.completion.Error == nil || *rec.completion.Error != "Cancelled by user" {
				t.Errorf("%s status = %q note = %v, want cancelled by user", id, rec.status, rec.completion.Error)
			}
			if !h.bus.isCompleted(id) {
				t.Errorf("%s bus not completed", id)
			}
		}
	})
}

func TestShutdown_GivesUpAfterTheDrainAndStillCompletesTheBuses(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, Options{}, time.Now)
		// The "LLM call" ignores cancellation for 30 s.
		h.engine.script = func(_ context.Context, _ reviewerapp.Input, _ reviewerapp.LLM, _ reviewerapp.Events, _ reviewerapp.Options) (reviewer.Outcome, error) {
			time.Sleep(30 * time.Second)
			return defaultOutcome(), nil
		}
		h.exec.Start(t.Context(), h.batch(1))
		synctest.Wait()

		start := time.Now()
		err := h.exec.Shutdown(t.Context())
		if !errors.Is(err, ErrNotDrained) {
			t.Errorf("Shutdown() = %v, want ErrNotDrained", err)
		}
		if elapsed := time.Since(start); elapsed != domain.ShutdownDrain {
			t.Errorf("Shutdown took %v, want the %v drain", elapsed, domain.ShutdownDrain)
		}
		if !h.bus.isCompleted("run-1") {
			t.Error("bus not completed after the drain")
		}
		if got := h.store.run("run-1").status; got != domain.StatusRunning {
			t.Errorf("status = %q, want running (reaped at the next start)", got)
		}

		// The abandoned run still ends: its cancel flag is sticky, so it records cancelled.
		h.wait()
		if got := h.store.run("run-1").status; got != domain.StatusCancelled {
			t.Errorf("status after the late return = %q, want cancelled", got)
		}
		if len(h.store.reviews) != 0 {
			t.Errorf("reviews = %v, want none", h.store.reviews)
		}
	})
}

func TestShutdown_StopsWhenTheCallerContextEnds(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, Options{}, time.Now)
		h.engine.script = func(_ context.Context, _ reviewerapp.Input, _ reviewerapp.LLM, _ reviewerapp.Events, _ reviewerapp.Options) (reviewer.Outcome, error) {
			time.Sleep(time.Hour)
			return defaultOutcome(), nil
		}
		h.exec.Start(t.Context(), h.batch(1))
		synctest.Wait()

		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()
		if err := h.exec.Shutdown(ctx); !errors.Is(err, ErrNotDrained) {
			t.Errorf("Shutdown() = %v, want ErrNotDrained", err)
		}
		h.wait()
	})
}

func TestShutdown_QueuedRunsEndCancelledWithoutACall(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, Options{AgentConcurrency: 1}, time.Now)
		h.engine.script = blockUntilCancelled
		h.exec.Start(t.Context(), h.batch(3))
		synctest.Wait() // run-1 is in its call, run-2 and run-3 wait for a slot

		if err := h.exec.Shutdown(t.Context()); err != nil {
			t.Fatalf("Shutdown() = %v, want nil", err)
		}
		for _, id := range []string{"run-1", "run-2", "run-3"} {
			if got := h.store.run(id).status; got != domain.StatusCancelled {
				t.Errorf("%s status = %q, want cancelled", id, got)
			}
		}
		if got := h.engine.calls(); got != 0 {
			t.Errorf("engine chunk calls = %d, want 0 (blockUntilCancelled counts none, queued runs make no call)", got)
		}
	})
}

func TestShutdown_RefusesNewBatchesAndIsRepeatable(t *testing.T) {
	h := newHarness(t, Options{}, fixedClock)
	if err := h.exec.Shutdown(t.Context()); err != nil {
		t.Fatalf("Shutdown() = %v, want nil", err)
	}
	h.exec.Start(t.Context(), h.batch(1))
	h.wait()
	if h.store.filesCalls != 0 {
		t.Errorf("PullFiles called %d times after Shutdown, want 0", h.store.filesCalls)
	}
	if got := h.store.run("run-1").status; got != domain.StatusRunning {
		t.Errorf("status = %q, want running (a refused batch is reaped at the next start)", got)
	}
	if got := h.logs.atLevel(t, "WARN"); len(got) != 1 {
		t.Errorf("warn lines = %v, want one refusal", got)
	}
	if err := h.exec.Shutdown(t.Context()); err != nil {
		t.Errorf("second Shutdown() = %v, want nil", err)
	}
}

func TestShutdown_RunStartedAfterTheCancelAllIsCancelledToo(t *testing.T) {
	// A batch still loading its diff when Shutdown runs has no run on the bus yet, so CancelAll
	// misses it; each run checks the stop flag when its turn comes.
	h := newHarness(t, Options{}, fixedClock)
	h.store.filesGate = make(chan struct{})
	h.exec.Start(t.Context(), h.batch(1))

	done := make(chan error, 1)
	go func() { done <- h.exec.Shutdown(t.Context()) }()
	for !h.exec.isStopped() {
		time.Sleep(time.Millisecond)
	}
	close(h.store.filesGate)
	if err := <-done; err != nil {
		t.Fatalf("Shutdown() = %v, want nil", err)
	}
	if got := h.store.run("run-1").status; got != domain.StatusCancelled {
		t.Errorf("status = %q, want cancelled", got)
	}
	if got := h.engine.calls(); got != 0 {
		t.Errorf("engine chunk calls = %d, want 0", got)
	}
}

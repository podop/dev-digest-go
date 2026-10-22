package inflight_test

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"dev-digest-go/backend/internal/platform/inflight"
)

func TestBegin_OneHolderPerKey(t *testing.T) {
	t.Parallel()
	m := inflight.NewMarkers()
	ctx1, release1, ok := m.Begin(t.Context(), "brief:pr1")
	if !ok {
		t.Fatal("Begin(free key) ok = false, want true")
	}
	if ctx1.Err() != nil {
		t.Errorf("fresh marker context = %v, want live", ctx1.Err())
	}
	if _, _, ok := m.Begin(t.Context(), "brief:pr1"); ok {
		t.Error("Begin(running key) ok = true, want false")
	}
	_, releaseOther, ok := m.Begin(t.Context(), "brief:pr2")
	if !ok {
		t.Error("Begin(other key) ok = false, want true")
	}
	if n := m.Len(); n != 2 {
		t.Errorf("Len() = %d, want 2", n)
	}

	release1()
	release1() // idempotent: must not free a later holder of the same key
	if ctx1.Err() == nil {
		t.Error("marker context after release is live, want ended")
	}
	_, release2, ok := m.Begin(t.Context(), "brief:pr1")
	if !ok {
		t.Fatal("Begin(released key) ok = false, want true")
	}
	release1()
	if _, _, ok := m.Begin(t.Context(), "brief:pr1"); ok {
		t.Error("a second call of an old release freed the new holder's key")
	}
	release2()
	releaseOther()
	if n := m.Len(); n != 0 {
		t.Errorf("Len() after all releases = %d, want 0", n)
	}
}

func TestBegin_FollowsTheRequestContext(t *testing.T) {
	t.Parallel()
	m := inflight.NewMarkers()
	errGone := errors.New("client went away")
	parent, cancel := context.WithCancelCause(t.Context())
	ctx, release, _ := m.Begin(parent, "k")
	defer release()
	cancel(errGone)
	if cause := context.Cause(ctx); !errors.Is(cause, errGone) {
		t.Errorf("marker context cause = %v, want the request's (%v)", cause, errGone)
	}
}

func TestShutdown_EndsHoldersAndWaitsForRelease(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m := inflight.NewMarkers()
		ctx, release, _ := m.Begin(context.Background(), "k")
		done := make(chan error, 1)
		go func() { done <- m.Shutdown(context.Background()) }()
		synctest.Wait()

		if cause := context.Cause(ctx); !errors.Is(cause, inflight.ErrShutdown) {
			t.Errorf("holder context cause after Shutdown = %v, want ErrShutdown", cause)
		}
		if len(done) != 0 {
			t.Fatal("Shutdown returned before the marker was released")
		}
		release()
		synctest.Wait()
		if err := <-done; err != nil {
			t.Errorf("Shutdown() after release = %v, want nil", err)
		}
	})
}

func TestShutdown_ReportsMarkersThatStayHeld(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m := inflight.NewMarkers()
		_, release, _ := m.Begin(context.Background(), "k")
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := m.Shutdown(ctx); !errors.Is(err, inflight.ErrNotDrained) {
			t.Errorf("Shutdown() with a holder that never releases = %v, want ErrNotDrained", err)
		}
		release()
		if err := m.Shutdown(context.Background()); err != nil {
			t.Errorf("Shutdown() after the late release = %v, want nil", err)
		}
	})
}

func TestBegin_AfterShutdownHandsOutACancelledContext(t *testing.T) {
	t.Parallel()
	m := inflight.NewMarkers()
	if err := m.Shutdown(t.Context()); err != nil {
		t.Fatalf("Shutdown() on an empty set = %v, want nil", err)
	}
	ctx, release, ok := m.Begin(t.Context(), "k")
	if !ok {
		t.Fatal("Begin after Shutdown ok = false, want true (the caller sees the cancellation)")
	}
	defer release()
	if cause := context.Cause(ctx); !errors.Is(cause, inflight.ErrShutdown) {
		t.Errorf("context cause = %v, want ErrShutdown", cause)
	}
}

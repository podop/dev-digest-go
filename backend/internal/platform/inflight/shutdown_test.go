package inflight_test

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/labstack/echo/v5"

	"dev-digest-go/backend/internal/platform/inflight"
)

// Planner decision P2 (Phase 5, NFR5010): does Echo's graceful shutdown cancel the contexts of
// requests in flight? These tests run a real echo.StartConfig on a real listener, with the
// shortened grace and generation durations below standing in for the 20 s HTTP grace and the
// 120 s generation budget, and observe what the handler of a long generation sees.
const (
	smokeGrace = 500 * time.Millisecond // stands in for the 20 s HTTP grace (cmd/server httpGrace)
	smokeJob   = 2 * time.Second        // stands in for a generation of up to 120 s
)

// smokeOutcome is what one shutdown scenario observed. Durations are measured from the stop signal.
type smokeOutcome struct {
	startReturnedAt time.Duration // echo.StartConfig.Start returned: the drain ended
	drainErr        error         // OnShutdownError: set when the grace ran out first
	handlerEndedAt  time.Duration // the generation handler returned
	ctxErrAtDrain   error         // request context error at the moment Start returned
	handlerErr      error         // what the generation reported
	status          int           // what the client received
	body            string
}

// smokeServer starts the handler gen on /generate behind a real echo.StartConfig, sends one
// request, waits until it is inside gen, then runs stop (the process stop sequence under test:
// it must end by calling stopEcho) and returns what was observed.
func smokeServer(t *testing.T, gen func(ctx context.Context) error, stop func(stopEcho func())) smokeOutcome {
	t.Helper()
	var (
		reqCtx   atomic.Pointer[context.Context]
		entered  = make(chan struct{})
		handlerT atomic.Int64 // unix nanos when gen returned
		genErr   atomic.Pointer[error]
	)
	e := echo.New()
	e.GET("/generate", func(c *echo.Context) error {
		ctx := c.Request().Context()
		reqCtx.Store(&ctx)
		close(entered)
		err := gen(ctx)
		genErr.Store(&err)
		handlerT.Store(time.Now().UnixNano())
		if err != nil {
			return c.String(http.StatusServiceUnavailable, err.Error())
		}
		return c.String(http.StatusOK, "completed")
	})

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	var drainErr atomic.Pointer[error]
	echoCtx, stopEcho := context.WithCancel(context.Background()) // like cmd: not the signal context
	defer stopEcho()
	sc := echo.StartConfig{
		Listener:        ln,
		HideBanner:      true,
		HidePort:        true,
		GracefulTimeout: smokeGrace,
		OnShutdownError: func(err error) { drainErr.Store(&err) },
	}
	started := make(chan struct{ at time.Time }, 1)
	go func() {
		serr := sc.Start(echoCtx, e)
		if serr != nil {
			t.Errorf("Start() = %v, want nil", serr)
		}
		started <- struct{ at time.Time }{time.Now()}
	}()

	type reply struct {
		status int
		body   string
	}
	replies := make(chan reply, 1)
	go func() {
		client := &http.Client{Transport: &http.Transport{DisableKeepAlives: true}}
		resp, err := client.Get("http://" + ln.Addr().String() + "/generate")
		if err != nil {
			replies <- reply{0, err.Error()}
			return
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		replies <- reply{resp.StatusCode, strings.TrimSpace(string(b))}
	}()

	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the request never reached the handler")
	}
	t0 := time.Now()
	stop(stopEcho)

	var o smokeOutcome
	select {
	case s := <-started:
		o.startReturnedAt = s.at.Sub(t0)
	case <-time.After(15 * time.Second):
		t.Fatal("Start did not return")
	}
	o.ctxErrAtDrain = (*reqCtx.Load()).Err()
	if p := drainErr.Load(); p != nil {
		o.drainErr = *p
	}
	select {
	case r := <-replies:
		o.status, o.body = r.status, r.body
	case <-time.After(15 * time.Second):
		t.Fatal("the client never got a reply")
	}
	o.handlerEndedAt = time.Unix(0, handlerT.Load()).Sub(t0)
	if p := genErr.Load(); p != nil {
		o.handlerErr = *p
	}
	return o
}

// job is the generation of the test: it works for smokeJob unless its context ends first.
func job(ctx context.Context) error {
	select {
	case <-time.After(smokeJob):
		return nil
	case <-ctx.Done():
		return context.Cause(ctx)
	}
}

// TestEchoShutdown_NeverCancelsInFlightRequestContexts is the P2 proof: stopping Echo while a
// generation runs on the request context does not cancel that context. Start gives up when the
// grace ends (DeadlineExceeded through OnShutdownError, which makes cmd fail the process), and
// the generation keeps running afterwards for its full length.
func TestEchoShutdown_NeverCancelsInFlightRequestContexts(t *testing.T) {
	t.Parallel()
	o := smokeServer(t, job, func(stopEcho func()) { stopEcho() })
	t.Logf("P2 unguarded: Start returned after %v (grace %v), drain error %v, request ctx at that moment %v; "+
		"generation returned after %v (job %v) with %v; client got %d %q",
		o.startReturnedAt, smokeGrace, o.drainErr, o.ctxErrAtDrain, o.handlerEndedAt, smokeJob, o.handlerErr, o.status, o.body)

	if !errors.Is(o.drainErr, context.DeadlineExceeded) {
		t.Errorf("drain error = %v, want context.DeadlineExceeded (the grace ran out with the request in flight)", o.drainErr)
	}
	if o.ctxErrAtDrain != nil {
		t.Errorf("request context when the grace ended = %v, want nil: Echo's shutdown does not cancel it", o.ctxErrAtDrain)
	}
	if o.startReturnedAt < smokeGrace || o.startReturnedAt >= smokeJob {
		t.Errorf("Start returned after %v, want the end of the grace (%v) well before the job (%v)", o.startReturnedAt, smokeGrace, smokeJob)
	}
	if o.handlerErr != nil || o.status != http.StatusOK || o.handlerEndedAt < smokeJob {
		t.Errorf("generation = {err %v, status %d, ended after %v}, want it to complete after %v: it outlived the grace",
			o.handlerErr, o.status, o.handlerEndedAt, smokeJob)
	}
}

// TestFlightsShutdownBeforeEcho_EndsGenerationsWithinTheGrace is the fix of P2: the generation
// runs on a flight; the stop sequence shuts the flights down, then lets Echo drain. The
// generation ends at once, the drain completes within the grace without error.
func TestFlightsShutdownBeforeEcho_EndsGenerationsWithinTheGrace(t *testing.T) {
	t.Parallel()
	flights := inflight.NewFlights[struct{}, struct{}](inflight.FlightsConfig{Budget: time.Minute})
	gen := func(ctx context.Context) error {
		_, err := flights.Do(ctx, "pr1:hash", nil, func(fctx context.Context, _ func(struct{})) (struct{}, error) {
			return struct{}{}, job(fctx)
		})
		return err
	}
	var shutdownErr error
	o := smokeServer(t, gen, func(stopEcho func()) {
		shutdownErr = flights.Shutdown(context.Background())
		stopEcho()
	})
	t.Logf("P2 with Flights.Shutdown first: Start returned after %v (grace %v), drain error %v; "+
		"generation returned after %v with %v; client got %d %q; flights left %d",
		o.startReturnedAt, smokeGrace, o.drainErr, o.handlerEndedAt, o.handlerErr, o.status, o.body, flights.Len())

	assertDrainedWithinGrace(t, o, shutdownErr)
	if n := flights.Len(); n != 0 {
		t.Errorf("flights left after the drain = %d, want 0 (NFR5010)", n)
	}
}

// TestMarkersShutdownBeforeEcho_EndsGenerationsWithinTheGrace is the same for a generation
// guarded by a marker (brief, onboarding).
func TestMarkersShutdownBeforeEcho_EndsGenerationsWithinTheGrace(t *testing.T) {
	t.Parallel()
	markers := inflight.NewMarkers()
	gen := func(ctx context.Context) error {
		mctx, release, ok := markers.Begin(ctx, "brief:pr1")
		if !ok {
			return errors.New("already running")
		}
		defer release()
		return job(mctx)
	}
	var shutdownErr error
	o := smokeServer(t, gen, func(stopEcho func()) {
		shutdownErr = markers.Shutdown(context.Background())
		stopEcho()
	})
	t.Logf("P2 with Markers.Shutdown first: Start returned after %v (grace %v), drain error %v; "+
		"generation returned after %v with %v; client got %d %q; markers left %d",
		o.startReturnedAt, smokeGrace, o.drainErr, o.handlerEndedAt, o.handlerErr, o.status, o.body, markers.Len())

	assertDrainedWithinGrace(t, o, shutdownErr)
	if n := markers.Len(); n != 0 {
		t.Errorf("markers left after the drain = %d, want 0 (NFR5010)", n)
	}
}

func assertDrainedWithinGrace(t *testing.T, o smokeOutcome, shutdownErr error) {
	t.Helper()
	if shutdownErr != nil {
		t.Errorf("service Shutdown() = %v, want nil", shutdownErr)
	}
	if o.drainErr != nil {
		t.Errorf("drain error = %v, want nil (the drain finished within the grace)", o.drainErr)
	}
	if o.startReturnedAt >= smokeGrace {
		t.Errorf("Start returned after %v, want within the grace (%v)", o.startReturnedAt, smokeGrace)
	}
	if !errors.Is(o.handlerErr, inflight.ErrShutdown) || o.status != http.StatusServiceUnavailable {
		t.Errorf("generation = {err %v, status %d}, want ErrShutdown and 503", o.handlerErr, o.status)
	}
	if o.handlerEndedAt >= smokeGrace {
		t.Errorf("generation ended after %v, want within the grace (%v), far before its %v job", o.handlerEndedAt, smokeGrace, smokeJob)
	}
}

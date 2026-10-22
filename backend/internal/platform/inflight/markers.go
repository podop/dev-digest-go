// Package inflight keeps the in-process bookkeeping of long generations (spec D5013, NFR5010):
// Markers answers "is a generation for this key already running" (brief per PR, onboarding per
// repo), Flights shares one derivation per key among concurrent callers (intent per PR and
// input hash, spec 7.4.6). Both end their work when the owning service is shut down.
//
// Why a service stop is needed on top of the request context (planner decision P2, proven by
// shutdown_test.go): Echo's graceful shutdown (http.Server.Shutdown) never cancels the context
// of a request in flight, it only waits. Work on the request context alone therefore outlives
// the 20 s HTTP grace. cmd calls Shutdown on every Markers and Flights before it lets Echo
// drain, so the handlers return within the grace.
//
// The state is per process and forgotten on restart (D5013).
package inflight

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

var (
	// ErrShutdown is the cancellation cause of work ended by Shutdown, and the error of a call
	// made after it.
	ErrShutdown = errors.New("service is shutting down")
	// ErrNotDrained is returned by Shutdown when work was still running at its deadline. The
	// work has been cancelled anyway; it only did not return in time.
	ErrNotDrained = errors.New("in-flight work did not finish before the deadline")
)

// Markers is a set of keys with a generation running. At most one holder per key; the holder's
// context also ends on Shutdown. The zero value is not usable, use NewMarkers.
type Markers struct {
	mu      sync.Mutex
	held    map[string]context.CancelCauseFunc
	stopped bool
	active  drain
}

// NewMarkers returns an empty set.
func NewMarkers() *Markers {
	return &Markers{held: make(map[string]context.CancelCauseFunc)}
}

// Begin marks key as running. When it was free it returns ok = true, a context that ends when
// ctx ends, when Shutdown is called (cause ErrShutdown) or when release is called, and the
// release function; release is idempotent and must be called when the generation ends (defer
// it). When key is already running it returns ok = false, ctx unchanged and a no-op release.
// After Shutdown, Begin still registers the key but hands out an already cancelled context.
func (m *Markers) Begin(ctx context.Context, key string) (_ context.Context, release func(), ok bool) {
	m.mu.Lock()
	if _, running := m.held[key]; running {
		m.mu.Unlock()
		return ctx, func() {}, false
	}
	mctx, cancel := context.WithCancelCause(ctx)
	m.held[key] = cancel
	m.active.inc()
	stopped := m.stopped
	m.mu.Unlock()
	if stopped {
		cancel(ErrShutdown)
	}

	var once sync.Once
	return mctx, func() {
		once.Do(func() {
			cancel(nil)
			m.mu.Lock()
			delete(m.held, key)
			m.mu.Unlock()
			m.active.dec()
		})
	}, true
}

// Len is the number of keys currently marked.
func (m *Markers) Len() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.held)
}

// Shutdown cancels the context of every running generation (cause ErrShutdown) and waits until
// all of them have released their marker, or ctx ends (ErrNotDrained).
func (m *Markers) Shutdown(ctx context.Context) error {
	m.mu.Lock()
	m.stopped = true
	for _, cancel := range m.held {
		cancel(ErrShutdown)
	}
	m.mu.Unlock()
	if err := m.active.wait(ctx); err != nil {
		return fmt.Errorf("%d generation markers still held: %w", m.Len(), err)
	}
	return nil
}

// drain counts running units of work and lets a caller wait until none is left.
type drain struct {
	mu   sync.Mutex
	n    int
	idle chan struct{} // closed when n reaches 0; nil while nobody waits
}

func (d *drain) inc() {
	d.mu.Lock()
	d.n++
	d.mu.Unlock()
}

func (d *drain) dec() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.n--
	if d.n == 0 && d.idle != nil {
		close(d.idle)
		d.idle = nil
	}
}

// wait returns nil once the count is zero, ErrNotDrained when ctx ends first.
func (d *drain) wait(ctx context.Context) error {
	d.mu.Lock()
	if d.n == 0 {
		d.mu.Unlock()
		return nil
	}
	if d.idle == nil {
		d.idle = make(chan struct{})
	}
	idle := d.idle
	d.mu.Unlock()
	select {
	case <-idle:
		return nil
	case <-ctx.Done():
		return ErrNotDrained
	}
}

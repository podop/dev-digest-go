// Package runbus is the in-memory run bus: per-run numbered event logs with replay, live
// subscribers that never block the publisher, sticky cancel flags, and eviction of a completed
// run's state after a TTL. It mirrors the Node RunBus (server/src/platform/sse.ts); state is lost
// on restart.
package runbus

import (
	"encoding/json"
	"slices"
	"sync"
	"time"
)

// Defaults for Options; they equal reviews' domain.BusTTL and domain.SubscriberBacklog.
const (
	DefaultTTL     = 5 * time.Minute
	DefaultBacklog = 1_024
)

// Options configures New. The zero value is the production setup.
type Options struct {
	// Now is the clock that stamps events; nil = time.Now.
	Now func() time.Time
	// AfterFunc schedules f once after d; nil = time.AfterFunc. Tests inject a manual timer.
	AfterFunc func(d time.Duration, f func())
	// TTL is how long a completed run's buffer and cancel flag are kept; <= 0 = DefaultTTL.
	TTL time.Duration
	// Backlog is how many events a subscriber may have queued, beyond the replay, before its
	// stream ends; <= 0 = DefaultBacklog.
	Backlog int
}

// Bus holds the state of every run. All methods are safe for concurrent use and none blocks on a
// subscriber.
type Bus struct {
	now       func() time.Time
	afterFunc func(time.Duration, func())
	ttl       time.Duration
	backlog   int

	mu   sync.Mutex
	runs map[string]*run
}

// New returns an empty bus.
func New(opts Options) *Bus {
	b := &Bus{
		now:       opts.Now,
		afterFunc: opts.AfterFunc,
		ttl:       opts.TTL,
		backlog:   opts.Backlog,
		runs:      map[string]*run{},
	}
	if b.now == nil {
		b.now = time.Now
	}
	if b.afterFunc == nil {
		b.afterFunc = func(d time.Duration, f func()) { time.AfterFunc(d, f) }
	}
	if b.ttl <= 0 {
		b.ttl = DefaultTTL
	}
	if b.backlog <= 0 {
		b.backlog = DefaultBacklog
	}
	return b
}

// get returns the run's state, creating it; the caller holds mu.
func (b *Bus) get(runID string) *run {
	r := b.runs[runID]
	if r == nil {
		r = newRun()
		b.runs[runID] = r
	}
	return r
}

// Publish numbers and stamps an event and delivers it to the live subscribers. After Complete it
// only appends to the retained buffer. A subscriber whose queue is full is ended instead of
// waited for. data is nil or a JSON document.
func (b *Bus) Publish(runID, kind, msg string, data json.RawMessage) Event {
	b.mu.Lock()
	defer b.mu.Unlock()
	r := b.get(runID)
	r.seq++
	ev := Event{RunID: runID, Seq: r.seq, Kind: kind, Msg: msg, T: b.now().Format(time.TimeOnly), Data: data}
	r.buffer = append(r.buffer, ev)
	if r.completed {
		return ev
	}
	for s := range r.subs {
		select {
		case s.ch <- ev:
		default:
			s.end()
		}
	}
	return ev
}

// Subscribe replays the buffer, then follows the live events. For a completed run the channel
// carries the buffer and is closed.
func (b *Bus) Subscribe(runID string) *Subscription {
	b.mu.Lock()
	defer b.mu.Unlock()
	r := b.get(runID)
	s := &Subscription{bus: b, run: r}
	if r.completed {
		s.ch = make(chan Event, len(r.buffer))
	} else {
		s.ch = make(chan Event, len(r.buffer)+b.backlog)
	}
	for _, ev := range r.buffer {
		s.ch <- ev
	}
	if r.completed {
		s.closed = true
		close(s.ch)
		return s
	}
	r.subs[s] = struct{}{}
	return s
}

// Subscribers returns how many subscriptions of the run are still attached.
func (b *Bus) Subscribers(runID string) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	if r := b.runs[runID]; r != nil {
		return len(r.subs)
	}
	return 0
}

// Has reports whether the bus holds the run (live, or completed and not yet evicted).
func (b *Bus) Has(runID string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	_, ok := b.runs[runID]
	return ok
}

// Buffer returns a copy of the run's events so far (nil when the bus does not hold the run).
func (b *Bus) Buffer(runID string) []Event {
	b.mu.Lock()
	defer b.mu.Unlock()
	if r := b.runs[runID]; r != nil {
		return slices.Clone(r.buffer)
	}
	return nil
}

// Complete ends every subscriber and schedules the eviction of the run's state after the TTL.
// The buffer and the cancel flag are kept until then. A second call does nothing.
func (b *Bus) Complete(runID string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if r := b.runs[runID]; r != nil {
		b.complete(runID, r)
	}
}

// complete does Complete's work; the caller holds mu.
func (b *Bus) complete(runID string, r *run) {
	if r.completed {
		return
	}
	r.completed = true
	for s := range r.subs {
		s.end()
	}
	b.afterFunc(b.ttl, func() { b.evict(runID, r) })
}

// evict forgets the run if it still is the completed state it was scheduled for.
func (b *Bus) evict(runID string, r *run) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.runs[runID] == r {
		delete(b.runs, runID)
	}
}

// Cancel raises the run's sticky cancel flag and fires its listeners once, outside the bus lock.
// Complete does not clear the flag; it lives until the run is evicted.
func (b *Bus) Cancel(runID string) {
	b.mu.Lock()
	r := b.get(runID)
	r.cancelled = true
	fns := b.takeListeners(r)
	b.mu.Unlock()
	for _, fn := range fns {
		fn()
	}
}

// takeListeners removes and returns the run's cancel listeners in registration order; the caller
// holds mu.
func (b *Bus) takeListeners(r *run) []func() {
	ids := make([]int, 0, len(r.listeners))
	for id := range r.listeners {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	fns := make([]func(), 0, len(ids))
	for _, id := range ids {
		fns = append(fns, r.listeners[id])
		delete(r.listeners, id)
	}
	return fns
}

// IsCancelled reports whether the run's cancel flag is raised.
func (b *Bus) IsCancelled(runID string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	r := b.runs[runID]
	return r != nil && r.cancelled
}

// OnCancel registers fn to run when the run is cancelled; when it already is, fn runs at once on
// the caller's goroutine and the returned stop does nothing. stop withdraws a pending listener.
// Listeners run on the goroutine that calls Cancel, without the bus lock held, so they may call
// the bus.
func (b *Bus) OnCancel(runID string, fn func()) (stop func()) {
	b.mu.Lock()
	r := b.get(runID)
	if r.cancelled {
		b.mu.Unlock()
		fn()
		return func() {}
	}
	id := r.nextID
	r.nextID++
	r.listeners[id] = fn
	b.mu.Unlock()
	return func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		delete(r.listeners, id)
	}
}

// CancelAll raises the cancel flag of every live (not completed) run and returns their ids,
// sorted. It is for shutdown: the runs abort and complete themselves.
func (b *Bus) CancelAll() []string {
	b.mu.Lock()
	ids := b.liveIDs()
	var fns []func()
	for _, id := range ids {
		r := b.runs[id]
		r.cancelled = true
		fns = append(fns, b.takeListeners(r)...)
	}
	b.mu.Unlock()
	for _, fn := range fns {
		fn()
	}
	return ids
}

// CompleteAll completes every run that is not completed yet, so no stream outlives shutdown.
func (b *Bus) CompleteAll() {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, id := range b.liveIDs() {
		b.complete(id, b.runs[id])
	}
}

// liveIDs returns the sorted ids of the runs that are not completed; the caller holds mu.
func (b *Bus) liveIDs() []string {
	var ids []string
	for id, r := range b.runs {
		if !r.completed {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	return ids
}

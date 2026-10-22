package peers

import (
	"encoding/json"
	"sync"

	"dev-digest-go/backend/internal/platform/runbus"
	reviewer "dev-digest-go/backend/internal/reviewer/domain"
	reviewsapp "dev-digest-go/backend/internal/reviews/app"
	"dev-digest-go/backend/internal/reviews/domain"
)

// Bus adapts *runbus.Bus (events of plain strings) to reviews/app.Bus (typed events).
type Bus struct{ b *runbus.Bus }

var _ reviewsapp.Bus = (*Bus)(nil)

// NewBus wraps the run bus.
func NewBus(b *runbus.Bus) *Bus { return &Bus{b: b} }

func toEvent(e runbus.Event) domain.RunEvent {
	return domain.RunEvent{RunID: e.RunID, Seq: e.Seq, Kind: reviewer.EventKind(e.Kind), Msg: e.Msg, T: e.T, Data: e.Data}
}

// Publish numbers and stamps an event.
func (a *Bus) Publish(runID string, kind reviewer.EventKind, msg string, data json.RawMessage) domain.RunEvent {
	return toEvent(a.b.Publish(runID, string(kind), msg, data))
}

// Subscribe replays the buffer, then follows the live events. A forwarding goroutine converts the
// events; it ends when the source ends (run completed, subscriber too slow) or on Close, and
// never blocks the bus: the bus drops a slow subscriber, the forwarder only waits for the reader.
func (a *Bus) Subscribe(runID string) reviewsapp.Subscription {
	src := a.b.Subscribe(runID)
	s := &subscription{src: src, out: make(chan domain.RunEvent), done: make(chan struct{})}
	s.wg.Go(s.forward)
	return s
}

// Has reports whether the bus holds the run.
func (a *Bus) Has(runID string) bool { return a.b.Has(runID) }

// Buffer returns a copy of the run's events so far.
func (a *Bus) Buffer(runID string) []domain.RunEvent {
	events := a.b.Buffer(runID)
	out := make([]domain.RunEvent, len(events))
	for i, e := range events {
		out[i] = toEvent(e)
	}
	return out
}

// Complete ends every subscriber of the run.
func (a *Bus) Complete(runID string) { a.b.Complete(runID) }

// Cancel raises the run's cancel flag and fires its listeners.
func (a *Bus) Cancel(runID string) { a.b.Cancel(runID) }

// IsCancelled reports the run's cancel flag.
func (a *Bus) IsCancelled(runID string) bool { return a.b.IsCancelled(runID) }

// OnCancel registers fn for the run's cancellation and returns the function that withdraws it.
func (a *Bus) OnCancel(runID string, fn func()) (stop func()) { return a.b.OnCancel(runID, fn) }

// CancelAll cancels every live run and returns their ids.
func (a *Bus) CancelAll() []string { return a.b.CancelAll() }

// CompleteAll completes every run that is not completed yet.
func (a *Bus) CompleteAll() { a.b.CompleteAll() }

// subscription converts a runbus subscription. out is closed by the forwarder when the source
// closes (after delivering every queued event) or when Close is called.
type subscription struct {
	src  *runbus.Subscription
	out  chan domain.RunEvent
	done chan struct{}
	once sync.Once
	wg   sync.WaitGroup
}

func (s *subscription) C() <-chan domain.RunEvent { return s.out }

// Close releases the subscription and waits for the forwarder to stop.
func (s *subscription) Close() {
	s.once.Do(func() {
		close(s.done)
		s.src.Close()
	})
	s.wg.Wait()
}

func (s *subscription) forward() {
	defer close(s.out)
	for e := range s.src.C() {
		select {
		case s.out <- toEvent(e):
		case <-s.done:
			return
		}
	}
}

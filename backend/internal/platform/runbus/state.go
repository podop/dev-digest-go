package runbus

import "encoding/json"

// Event is one numbered line of a run's live log.
type Event struct {
	RunID string
	// Seq counts from 1 without gaps within a run.
	Seq int
	// Kind is the event kind ("info", "tool", "result", ...); the bus does not interpret it.
	Kind string
	Msg  string
	// T is the local wall-clock time "HH:MM:SS".
	T string
	// Data is the event's JSON payload; nil = none.
	Data json.RawMessage
}

// run is the bus's state for one run. Every field is guarded by Bus.mu.
type run struct {
	seq       int
	buffer    []Event
	subs      map[*Subscription]struct{}
	completed bool
	cancelled bool
	listeners map[int]func()
	nextID    int
}

func newRun() *run {
	return &run{subs: map[*Subscription]struct{}{}, listeners: map[int]func(){}}
}

// Subscription is a live view of one run: the events buffered at subscription time, then the live
// ones, with no gap and no duplicate. C is closed when the run completes, when the subscriber
// falls more than the backlog behind, or on Close.
type Subscription struct {
	bus *Bus
	run *run
	ch  chan Event
	// closed is true once ch is closed; guarded by bus.mu.
	closed bool
}

// C returns the event channel. It is closed when the stream ends; events already queued can still
// be read after that.
func (s *Subscription) C() <-chan Event { return s.ch }

// Close releases the subscription. It is safe to call more than once and from any goroutine.
func (s *Subscription) Close() {
	s.bus.mu.Lock()
	defer s.bus.mu.Unlock()
	s.end()
}

// end closes the channel and detaches the subscription; the caller holds bus.mu.
func (s *Subscription) end() {
	if s.closed {
		return
	}
	s.closed = true
	delete(s.run.subs, s)
	close(s.ch)
}

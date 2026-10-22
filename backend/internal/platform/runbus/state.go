package runbus

import (
	"encoding/json"
	"sync"
)

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
// ones, with no gap and no duplicate. C is closed when the run completes (after the pending events
// were read), when the subscriber has more than the backlog of live events pending (after the
// events queued up to then were read), and on Close (at once; the events still pending can be read
// from the channel C returns after Close).
//
// An idle subscription costs a few hundred bytes plus one parked goroutine: the replay is a view of
// the run's buffer and the live events wait in a queue that grows with the pending count, up to the
// backlog. A goroutine moves them to C one at a time; it ends when C is closed, so a caller must
// read C to its end or call Close.
type Subscription struct {
	bus *Bus
	run *run
	// out is C; unbuffered, closed by the pump goroutine only.
	out chan Event
	// done is closed by Close; wake (capacity 1) tells the pump that the queue or ended changed;
	// exited is closed when the pump returned.
	done      chan struct{}
	wake      chan struct{}
	exited    chan struct{}
	closeOnce sync.Once

	mu sync.Mutex
	// replay is the part of the run's buffer not delivered yet; it is a view of immutable
	// elements (the buffer only grows at its end).
	replay []Event
	// queue[head:] are the live events not delivered yet. An event stays queued until the reader
	// took it, so len(queue)-head is the pending count the backlog bounds.
	queue []Event
	head  int
	// ended is true once no further event will be queued (run completed, cut off, Close).
	ended bool
	// closed is true once Close was called; final then holds the events that were still pending,
	// built by the first C call after Close.
	closed bool
	final  chan Event
}

func newSubscription(b *Bus, r *run, replay []Event, ended bool) *Subscription {
	s := &Subscription{
		bus: b, run: r, replay: replay, ended: ended,
		out:    make(chan Event),
		done:   make(chan struct{}),
		wake:   make(chan struct{}, 1),
		exited: make(chan struct{}),
	}
	go s.pump()
	return s
}

// C returns the event channel. It is closed when the stream ends, after the events that were
// pending then. After Close it returns a closed channel that holds the events still pending at
// Close; a channel obtained before Close just ends.
func (s *Subscription) C() <-chan Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.closed {
		return s.out
	}
	if s.final == nil {
		s.final = make(chan Event, len(s.replay)+len(s.queue)-s.head)
		for _, ev := range s.replay {
			s.final <- ev
		}
		for _, ev := range s.queue[s.head:] {
			s.final <- ev
		}
		close(s.final)
	}
	return s.final
}

// Close releases the subscription and returns once C is closed. It is safe to call more than once
// and from any goroutine.
func (s *Subscription) Close() {
	s.bus.mu.Lock()
	s.end()
	s.bus.mu.Unlock()
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.closed = true
		s.mu.Unlock()
		close(s.done)
	})
	<-s.exited
}

// end stops the live feed and detaches the subscription; the pump still hands over what is
// pending. The caller holds bus.mu.
func (s *Subscription) end() {
	s.mu.Lock()
	s.ended = true
	s.mu.Unlock()
	delete(s.run.subs, s)
	s.signal()
}

// offer queues a live event; it reports false when the subscriber is more than backlog events
// behind (the event is not queued). The caller holds bus.mu.
func (s *Subscription) offer(ev Event, backlog int) bool {
	s.mu.Lock()
	if s.ended {
		s.mu.Unlock()
		return true
	}
	if len(s.queue)-s.head >= backlog {
		s.mu.Unlock()
		return false
	}
	s.queue = append(s.queue, ev)
	s.mu.Unlock()
	s.signal()
	return true
}

func (s *Subscription) signal() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// pump hands the replay, then the queued events, to out in order, one at a time.
func (s *Subscription) pump() {
	defer close(s.exited)
	defer close(s.out)
	for {
		ev, ok, finished := s.peek()
		switch {
		case ok:
			select {
			case s.out <- ev:
				s.pop()
			case <-s.done:
				return
			}
		case finished:
			return
		default:
			select {
			case <-s.wake:
			case <-s.done:
				return
			}
		}
	}
}

// peek returns the next event to deliver; finished is true when nothing is pending and nothing
// will be.
func (s *Subscription) peek() (ev Event, ok, finished bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case len(s.replay) > 0:
		return s.replay[0], true, false
	case s.head < len(s.queue):
		return s.queue[s.head], true, false
	default:
		return Event{}, false, s.ended
	}
}

// pop drops the event peek returned, after it was delivered.
func (s *Subscription) pop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.replay) > 0 {
		s.replay = s.replay[1:]
		if len(s.replay) == 0 {
			s.replay = nil
		}
		return
	}
	s.queue[s.head] = Event{}
	s.head++
	switch {
	case s.head == len(s.queue):
		s.queue, s.head = s.queue[:0], 0
	case s.head >= 256 && s.head*2 >= len(s.queue):
		s.queue = s.queue[:copy(s.queue, s.queue[s.head:])]
		s.head = 0
	}
}

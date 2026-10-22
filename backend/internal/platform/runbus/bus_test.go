package runbus_test

import (
	"encoding/json"
	"fmt"
	"runtime"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/platform/runbus"
)

// manualTimer is an injectable AfterFunc that fires only when the test says so.
type manualTimer struct {
	mu      sync.Mutex
	pending []timerEntry
}

type timerEntry struct {
	d time.Duration
	f func()
}

func (m *manualTimer) AfterFunc(d time.Duration, f func()) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pending = append(m.pending, timerEntry{d, f})
}

func (m *manualTimer) delays() []time.Duration {
	m.mu.Lock()
	defer m.mu.Unlock()
	var ds []time.Duration
	for _, e := range m.pending {
		ds = append(ds, e.d)
	}
	return ds
}

func (m *manualTimer) fireAll() {
	m.mu.Lock()
	pending := m.pending
	m.pending = nil
	m.mu.Unlock()
	for _, e := range pending {
		e.f()
	}
}

// drain reads sub until it is closed and returns the sequence numbers it saw.
func drain(sub *runbus.Subscription) []int {
	var seqs []int
	for ev := range sub.C() {
		seqs = append(seqs, ev.Seq)
	}
	return seqs
}

func seqRange(from, to int) []int {
	var s []int
	for i := from; i <= to; i++ {
		s = append(s, i)
	}
	return s
}

func fixedNow() time.Time { return time.Date(2026, 10, 22, 9, 5, 7, 0, time.Local) }

func TestBus_ReplayLiveCompleteEvict(t *testing.T) {
	timer := &manualTimer{}
	bus := runbus.New(runbus.Options{Now: fixedNow, AfterFunc: timer.AfterFunc})

	first := bus.Subscribe("r1")
	for i := 1; i <= 10; i++ {
		ev := bus.Publish("r1", "info", fmt.Sprintf("m%d", i), nil)
		if ev.Seq != i {
			t.Fatalf("Publish #%d: Seq = %d, want %d", i, ev.Seq, i)
		}
	}
	second := bus.Subscribe("r1")
	for i := 11; i <= 15; i++ {
		bus.Publish("r1", "info", fmt.Sprintf("m%d", i), nil)
	}
	bus.Complete("r1")
	bus.Complete("r1") // idempotent
	late := bus.Publish("r1", "info", "after", nil)

	if diff := cmp.Diff(seqRange(1, 15), drain(first)); diff != "" {
		t.Errorf("first subscriber mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff(seqRange(1, 15), drain(second)); diff != "" {
		t.Errorf("second subscriber mismatch (-want +got):\n%s", diff)
	}
	if late.Seq != 16 {
		t.Errorf("Publish after Complete: Seq = %d, want 16", late.Seq)
	}
	buf := bus.Buffer("r1")
	if len(buf) != 16 || buf[15].Msg != "after" {
		t.Errorf("Buffer after Complete holds %d events (last %q), want 16 ending in %q", len(buf), buf[len(buf)-1].Msg, "after")
	}
	if got := timer.delays(); !slices.Equal(got, []time.Duration{runbus.DefaultTTL}) {
		t.Errorf("eviction timers = %v, want exactly one of %v", got, runbus.DefaultTTL)
	}
	if !bus.Has("r1") {
		t.Error("Has(r1) = false before the TTL elapsed, want true")
	}

	// A subscriber joining after completion gets the whole buffer and then the end.
	if diff := cmp.Diff(seqRange(1, 16), drain(bus.Subscribe("r1"))); diff != "" {
		t.Errorf("late subscriber mismatch (-want +got):\n%s", diff)
	}

	bus.Cancel("r1")
	timer.fireAll()
	if bus.Has("r1") || bus.Buffer("r1") != nil || bus.IsCancelled("r1") {
		t.Errorf("after the TTL: Has=%v Buffer=%v IsCancelled=%v, want the run forgotten", bus.Has("r1"), bus.Buffer("r1"), bus.IsCancelled("r1"))
	}
}

func TestBus_EventFields(t *testing.T) {
	bus := runbus.New(runbus.Options{Now: fixedNow})
	data := json.RawMessage(`{"a":1}`)
	got := bus.Publish("r", "tool", "msg", data)
	want := runbus.Event{RunID: "r", Seq: 1, Kind: "tool", Msg: "msg", T: "09:05:07", Data: data}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Publish mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff([]runbus.Event{want}, bus.Buffer("r")); diff != "" {
		t.Errorf("Buffer mismatch (-want +got):\n%s", diff)
	}
}

func TestBus_NumberingIsPerRun(t *testing.T) {
	bus := runbus.New(runbus.Options{})
	bus.Publish("a", "info", "x", nil)
	bus.Publish("a", "info", "y", nil)
	if got := bus.Publish("b", "info", "z", nil).Seq; got != 1 {
		t.Errorf("first Publish on run b: Seq = %d, want 1", got)
	}
}

func TestBus_BufferIsACopy(t *testing.T) {
	bus := runbus.New(runbus.Options{})
	bus.Publish("r", "info", "orig", nil)
	buf := bus.Buffer("r")
	buf[0].Msg = "changed"
	if got := bus.Buffer("r")[0].Msg; got != "orig" {
		t.Errorf("Buffer()[0].Msg = %q after the caller edited its copy, want %q", got, "orig")
	}
}

func TestBus_UnknownRun(t *testing.T) {
	bus := runbus.New(runbus.Options{})
	if bus.Has("x") || bus.IsCancelled("x") || bus.Buffer("x") != nil || bus.Subscribers("x") != 0 {
		t.Error("an unknown run must report no state")
	}
	bus.Complete("x") // no state, no panic, no state created
	if bus.Has("x") {
		t.Error("Complete on an unknown run created state")
	}
}

func TestBus_SubscribeBeforeFirstPublish(t *testing.T) {
	// EC4028: the stream opens while the run is queued; the first event still reaches it.
	bus := runbus.New(runbus.Options{})
	sub := bus.Subscribe("r")
	defer sub.Close()
	bus.Publish("r", "info", "first", nil)
	bus.Complete("r")
	if diff := cmp.Diff([]int{1}, drain(sub)); diff != "" {
		t.Errorf("subscriber mismatch (-want +got):\n%s", diff)
	}
}

func TestBus_CancelFlagIsSticky(t *testing.T) {
	timer := &manualTimer{}
	bus := runbus.New(runbus.Options{AfterFunc: timer.AfterFunc})
	bus.Publish("r", "info", "x", nil)
	var fired atomic.Int32
	bus.OnCancel("r", func() { fired.Add(1) })

	bus.Cancel("r")
	bus.Complete("r")
	bus.Cancel("r") // the listeners fire once only

	if !bus.IsCancelled("r") {
		t.Error("IsCancelled after Cancel+Complete = false, want true (sticky)")
	}
	if got := fired.Load(); got != 1 {
		t.Errorf("listener ran %d times, want 1", got)
	}

	var late atomic.Int32
	stop := bus.OnCancel("r", func() { late.Add(1) })
	if got := late.Load(); got != 1 {
		t.Errorf("OnCancel on a cancelled run: listener ran %d times at once, want 1", got)
	}
	stop() // must be a no-op, not a panic
	bus.Cancel("r")
	if got := late.Load(); got != 1 {
		t.Errorf("listener added after Cancel ran %d times in total, want 1", got)
	}
}

func TestBus_CancelBeforeAnyEvent(t *testing.T) {
	bus := runbus.New(runbus.Options{})
	bus.Cancel("queued")
	if !bus.IsCancelled("queued") {
		t.Error("IsCancelled = false for a run cancelled before it published, want true")
	}
}

func TestBus_OnCancelStop(t *testing.T) {
	bus := runbus.New(runbus.Options{})
	var kept, stopped atomic.Int32
	bus.OnCancel("r", func() { kept.Add(1) })
	stop := bus.OnCancel("r", func() { stopped.Add(1) })
	stop()
	stop() // twice is fine
	bus.Cancel("r")
	if kept.Load() != 1 || stopped.Load() != 0 {
		t.Errorf("listeners ran kept=%d stopped=%d, want 1 and 0", kept.Load(), stopped.Load())
	}
}

func TestBus_ListenerMayCallTheBus(t *testing.T) {
	bus := runbus.New(runbus.Options{})
	done := make(chan struct{})
	bus.OnCancel("r", func() {
		bus.Publish("r", "info", "Cancellation requested", nil)
		bus.Complete("r")
		bus.OnCancel("r", func() {})
		close(done)
	})
	go bus.Cancel("r")
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("a cancel listener calling the bus deadlocked")
	}
}

// TestBus_SlowSubscriberIsCutOff publishes while one subscriber reads nothing. Publish never
// waiting for it is shown by the loop finishing at all (it would hang on a full queue); no
// wall-clock bound is asserted. The healthy subscriber is read in step with the publisher, so it
// can never be more than one event behind.
func TestBus_SlowSubscriberIsCutOff(t *testing.T) {
	bus := runbus.New(runbus.Options{})
	slow := bus.Subscribe("r")
	healthy := bus.Subscribe("r")
	defer healthy.Close()

	const total = 1_100
	var got []int
	published := make(chan struct{})
	go func() {
		defer close(published)
		for range total {
			bus.Publish("r", "info", "m", nil)
			ev, ok := <-healthy.C()
			if !ok {
				return
			}
			got = append(got, ev.Seq)
		}
	}()
	select {
	case <-published:
	case <-time.After(time.Minute): // a hang guard, not a latency bound
		t.Fatal("Publish did not return while a subscriber was not reading")
	}
	if diff := cmp.Diff(seqRange(1, total), got); diff != "" {
		t.Errorf("healthy subscriber mismatch (-want +got):\n%s", diff)
	}
	if n := bus.Subscribers("r"); n != 1 {
		t.Errorf("Subscribers after the cut-off = %d, want 1 (the healthy one)", n)
	}

	// The slow stream holds the first 1,024 events and then ends.
	if diff := cmp.Diff(seqRange(1, runbus.DefaultBacklog), drain(slow)); diff != "" {
		t.Errorf("slow subscriber's queued events mismatch (-want +got):\n%s", diff)
	}
	slow.Close() // already ended: no panic
	bus.Complete("r")
}

// TestBus_PendingGrowsAndDrains keeps a subscriber a few hundred events behind, long enough for
// the queue to be compacted, and checks no event is lost, repeated or reordered.
func TestBus_PendingGrowsAndDrains(t *testing.T) {
	bus := runbus.New(runbus.Options{})
	sub := bus.Subscribe("r")
	defer sub.Close()
	const lag, total = 700, 5_000
	var got []int
	for i := 1; i <= total; i++ {
		bus.Publish("r", "info", "m", nil)
		if i > lag {
			got = append(got, (<-sub.C()).Seq)
		}
	}
	bus.Complete("r")
	got = append(got, drain(sub)...)
	if diff := cmp.Diff(seqRange(1, total), got); diff != "" {
		t.Errorf("subscriber mismatch (-want +got):\n%s", diff)
	}
}

// TestBus_PendingAtTheBacklogIsKept: exactly Backlog pending live events are fine, one more ends
// the stream, and the replay does not count.
func TestBus_PendingAtTheBacklogIsKept(t *testing.T) {
	bus := runbus.New(runbus.Options{Backlog: 4})
	for range 10 { // a replay larger than the backlog
		bus.Publish("r", "info", "m", nil)
	}
	sub := bus.Subscribe("r")
	defer sub.Close()
	for range 4 {
		bus.Publish("r", "info", "m", nil)
	}
	if n := bus.Subscribers("r"); n != 1 {
		t.Fatalf("Subscribers with 4 pending = %d, want 1", n)
	}
	bus.Publish("r", "info", "m", nil)
	if n := bus.Subscribers("r"); n != 0 {
		t.Errorf("Subscribers with 5 pending = %d, want 0", n)
	}
	if diff := cmp.Diff(seqRange(1, 14), drain(sub)); diff != "" {
		t.Errorf("subscriber mismatch (-want +got):\n%s", diff)
	}
}

// TestBus_IdleSubscribersAreCheap guards F3a: the queue is not allocated up front. The cost per
// subscriber was about 105 KB (a 1,024-event channel); it is now a few KB.
func TestBus_IdleSubscribersAreCheap(t *testing.T) {
	bus := runbus.New(runbus.Options{})
	bus.Publish("r", "info", "m", nil)
	const n = 500
	subs := make([]*runbus.Subscription, 0, n)
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	for range n {
		subs = append(subs, bus.Subscribe("r"))
	}
	runtime.ReadMemStats(&after)
	for _, s := range subs {
		s.Close()
	}
	if per := (after.TotalAlloc - before.TotalAlloc) / n; per > 16<<10 {
		t.Errorf("Subscribe allocated %d bytes per idle subscriber, want <= 16 KiB", per)
	}
}

func TestBus_BacklogOption(t *testing.T) {
	bus := runbus.New(runbus.Options{Backlog: 3})
	sub := bus.Subscribe("r")
	for range 5 {
		bus.Publish("r", "info", "m", nil)
	}
	if diff := cmp.Diff([]int{1, 2, 3}, drain(sub)); diff != "" {
		t.Errorf("subscriber mismatch (-want +got):\n%s", diff)
	}
}

func TestBus_ReplayLargerThanBacklogIsNotACutOff(t *testing.T) {
	bus := runbus.New(runbus.Options{Backlog: 3})
	for range 10 {
		bus.Publish("r", "info", "m", nil)
	}
	sub := bus.Subscribe("r")
	bus.Publish("r", "info", "m", nil)
	bus.Complete("r")
	if diff := cmp.Diff(seqRange(1, 11), drain(sub)); diff != "" {
		t.Errorf("subscriber mismatch (-want +got):\n%s", diff)
	}
}

func TestBus_SubscriberClose(t *testing.T) {
	bus := runbus.New(runbus.Options{})
	a, b := bus.Subscribe("r"), bus.Subscribe("r")
	if n := bus.Subscribers("r"); n != 2 {
		t.Fatalf("Subscribers = %d, want 2", n)
	}
	a.Close()
	a.Close()
	if n := bus.Subscribers("r"); n != 1 {
		t.Errorf("Subscribers after Close = %d, want 1", n)
	}
	if _, ok := <-a.C(); ok {
		t.Error("channel of a closed subscription is still open")
	}
	bus.Publish("r", "info", "m", nil)
	bus.Complete("r")
	b.Close() // after the run completed
	if diff := cmp.Diff([]int{1}, drain(b)); diff != "" {
		t.Errorf("surviving subscriber mismatch (-want +got):\n%s", diff)
	}
}

func TestBus_CancelAllCompleteAll(t *testing.T) {
	timer := &manualTimer{}
	bus := runbus.New(runbus.Options{AfterFunc: timer.AfterFunc})
	bus.Publish("b", "info", "x", nil)
	bus.Publish("a", "info", "x", nil)
	bus.Publish("done", "info", "x", nil)
	bus.Complete("done")
	var fired atomic.Int32
	bus.OnCancel("a", func() { fired.Add(1) })
	subA := bus.Subscribe("a")

	if diff := cmp.Diff([]string{"a", "b"}, bus.CancelAll()); diff != "" {
		t.Errorf("CancelAll() mismatch (-want +got):\n%s", diff)
	}
	if !bus.IsCancelled("a") || !bus.IsCancelled("b") || bus.IsCancelled("done") {
		t.Errorf("cancel flags a=%v b=%v done=%v, want true true false", bus.IsCancelled("a"), bus.IsCancelled("b"), bus.IsCancelled("done"))
	}
	if fired.Load() != 1 {
		t.Errorf("listener of a ran %d times, want 1", fired.Load())
	}

	bus.CompleteAll()
	if diff := cmp.Diff([]int{1}, drain(subA)); diff != "" {
		t.Errorf("subscriber of a mismatch (-want +got):\n%s", diff)
	}
	if got := bus.CancelAll(); len(got) != 0 {
		t.Errorf("CancelAll() after CompleteAll = %v, want none", got)
	}
	if n := len(timer.delays()); n != 3 {
		t.Errorf("eviction timers scheduled = %d, want 3 (one per run, none twice)", n)
	}
}

// TestBus_TTLWithRealTimer runs the default timer on synctest's fake clock.
func TestBus_TTLWithRealTimer(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		bus := runbus.New(runbus.Options{})
		bus.Publish("r", "info", "x", nil)
		bus.Cancel("r")
		bus.Complete("r")

		time.Sleep(runbus.DefaultTTL - time.Second)
		if !bus.Has("r") || !bus.IsCancelled("r") {
			t.Error("run forgotten before the TTL elapsed")
		}
		time.Sleep(2 * time.Second)
		synctest.Wait()
		if bus.Has("r") {
			t.Error("run still held after the TTL elapsed")
		}
		if bus.IsCancelled("r") {
			t.Error("cancel flag survived eviction")
		}
	})
}

func TestBus_TTLOption(t *testing.T) {
	timer := &manualTimer{}
	bus := runbus.New(runbus.Options{TTL: time.Minute, AfterFunc: timer.AfterFunc})
	bus.Publish("r", "info", "x", nil)
	bus.Complete("r")
	if got := timer.delays(); !slices.Equal(got, []time.Duration{time.Minute}) {
		t.Errorf("eviction delay = %v, want [1m0s]", got)
	}
}

// TestBus_Concurrent is the NFR4012 / AC4024 race test: concurrent publish, subscribe, cancel,
// complete and evict on 50 runs; run it with -race.
func TestBus_Concurrent(t *testing.T) {
	const runs, subsPerRun, events = 50, 4, 200
	// Eviction runs on its own goroutine as soon as a run completes.
	bus := runbus.New(runbus.Options{AfterFunc: func(_ time.Duration, f func()) { go f() }})

	var subsReady, all sync.WaitGroup
	for i := range runs {
		id := fmt.Sprintf("run-%d", i)
		subsReady.Add(subsPerRun)
		pubDone := make(chan struct{})
		var pubs sync.WaitGroup

		for range subsPerRun {
			all.Go(func() {
				sub := bus.Subscribe(id)
				subsReady.Done()
				var next = 1
				for ev := range sub.C() {
					if ev.Seq != next {
						t.Errorf("run %s: subscriber saw Seq %d, want %d", id, ev.Seq, next)
						return
					}
					next++
				}
				if next != events+1 {
					t.Errorf("run %s: subscriber ended after %d events, want %d", id, next-1, events)
				}
			})
		}
		for range 2 { // two publishers share the sequence
			pubs.Go(func() {
				for range events / 2 {
					bus.Publish(id, "info", "m", nil)
				}
			})
		}
		all.Go(func() { // chatter that must not disturb anything
			for {
				select {
				case <-pubDone:
					return
				default:
				}
				bus.Has(id)
				bus.Buffer(id)
				bus.IsCancelled(id)
				bus.OnCancel(id, func() {})()
			}
		})
		all.Go(func() {
			bus.Cancel(id)
			bus.CancelAll()
		})
		all.Go(func() {
			pubs.Wait()
			close(pubDone)
			subsReady.Wait()
			bus.Complete(id)
		})
	}
	all.Wait()
}

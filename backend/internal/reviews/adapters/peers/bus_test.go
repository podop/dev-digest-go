package peers_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/platform/runbus"
	reviewer "dev-digest-go/backend/internal/reviewer/domain"
	"dev-digest-go/backend/internal/reviews/adapters/peers"
	"dev-digest-go/backend/internal/reviews/domain"
)

func newBus(backlog int) (*peers.Bus, *runbus.Bus) {
	raw := runbus.New(runbus.Options{
		Now:       func() time.Time { return time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC) },
		AfterFunc: func(time.Duration, func()) {}, // no eviction in these tests
		Backlog:   backlog,
	})
	return peers.NewBus(raw), raw
}

func next(t *testing.T, ch <-chan domain.RunEvent) (domain.RunEvent, bool) {
	t.Helper()
	select {
	case e, ok := <-ch:
		return e, ok
	case <-time.After(5 * time.Second):
		t.Fatal("no event within 5 s")
		return domain.RunEvent{}, false
	}
}

func TestBus_PublishConvertsTheEvent(t *testing.T) {
	b, _ := newBus(0)
	got := b.Publish("r1", reviewer.EventTool, "Loading", json.RawMessage(`{"file":"a"}`))
	want := domain.RunEvent{RunID: "r1", Seq: 1, Kind: reviewer.EventTool, Msg: "Loading", T: "03:04:05", Data: json.RawMessage(`{"file":"a"}`)}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Publish() mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff([]domain.RunEvent{want}, b.Buffer("r1")); diff != "" {
		t.Errorf("Buffer() mismatch (-want +got):\n%s", diff)
	}
	if !b.Has("r1") || b.Has("nope") {
		t.Errorf("Has(r1) = %v, Has(nope) = %v, want true, false", b.Has("r1"), b.Has("nope"))
	}
}

func TestBus_SubscribeReplaysThenFollowsAndEndsOnComplete(t *testing.T) {
	b, _ := newBus(0)
	b.Publish("r1", reviewer.EventInfo, "one", nil)
	sub := b.Subscribe("r1")
	defer sub.Close()
	b.Publish("r1", reviewer.EventResult, "two", nil)
	b.Complete("r1")

	var msgs []string
	for {
		e, ok := next(t, sub.C())
		if !ok {
			break
		}
		msgs = append(msgs, e.Msg)
	}
	if diff := cmp.Diff([]string{"one", "two"}, msgs); diff != "" {
		t.Errorf("subscription events mismatch (-want +got):\n%s", diff)
	}
}

func TestBus_SubscribeToACompletedRunReplaysAndCloses(t *testing.T) {
	b, _ := newBus(0)
	b.Publish("r1", reviewer.EventInfo, "one", nil)
	b.Complete("r1")
	sub := b.Subscribe("r1")
	defer sub.Close()
	if e, ok := next(t, sub.C()); !ok || e.Msg != "one" {
		t.Fatalf("first event = %+v, %v, want one", e, ok)
	}
	if _, ok := next(t, sub.C()); ok {
		t.Error("channel still open after the completed run's buffer")
	}
}

func TestBus_CloseReleasesTheForwarderAndTheSource(t *testing.T) {
	b, raw := newBus(0)
	b.Publish("r1", reviewer.EventInfo, "one", nil)
	sub := b.Subscribe("r1")
	// The forwarder is now blocked handing "one" to a reader that never comes.
	sub.Close()
	sub.Close() // idempotent
	if n := raw.Subscribers("r1"); n != 0 {
		t.Errorf("Subscribers() = %d after Close, want 0", n)
	}
	for e := range sub.C() { // the channel is closed; at most the held event may be buffered, none is
		t.Errorf("event %+v delivered after Close", e)
	}
}

func TestBus_SlowSubscriberIsEndedWithoutBlockingThePublisher(t *testing.T) {
	b, _ := newBus(2)
	sub := b.Subscribe("r1")
	defer sub.Close()
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := range 20 {
			b.Publish("r1", reviewer.EventInfo, string(rune('a'+i)), nil)
		}
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Publish blocked on a subscriber that does not read")
	}
	var seqs []int
	for {
		e, ok := next(t, sub.C())
		if !ok {
			break
		}
		seqs = append(seqs, e.Seq)
	}
	if len(seqs) == 0 || len(seqs) >= 20 {
		t.Fatalf("slow subscriber received %d events, want a short prefix", len(seqs))
	}
	for i, s := range seqs {
		if s != i+1 {
			t.Fatalf("slow subscriber seqs = %v, want a gapless prefix from 1", seqs)
		}
	}
}

func TestBus_CancelFlagPassThrough(t *testing.T) {
	b, _ := newBus(0)
	b.Publish("live", reviewer.EventInfo, "x", nil)
	b.Publish("done", reviewer.EventInfo, "x", nil)
	b.Complete("done")

	fired := make(chan struct{}, 1)
	stop := b.OnCancel("live", func() { fired <- struct{}{} })
	withdrawn := b.OnCancel("live", func() { t.Error("withdrawn listener fired") })
	withdrawn()
	if b.IsCancelled("live") {
		t.Error("IsCancelled(live) = true before Cancel")
	}
	if diff := cmp.Diff([]string{"live"}, b.CancelAll()); diff != "" {
		t.Errorf("CancelAll() mismatch (-want +got):\n%s", diff)
	}
	waitFired := func() {
		select {
		case <-fired:
		case <-time.After(5 * time.Second):
			t.Error("OnCancel listener did not fire")
		}
	}
	waitFired()
	stop()
	if !b.IsCancelled("live") {
		t.Error("IsCancelled(live) = false after CancelAll")
	}
	b.Cancel("done")
	if !b.IsCancelled("done") {
		t.Error("IsCancelled(done) = false after Cancel")
	}
	b.CompleteAll()
	sub := b.Subscribe("live")
	defer sub.Close()
	if _, ok := next0(t, sub.C()); ok {
		t.Error("live run still open after CompleteAll")
	}
}

func next0(t *testing.T, ch <-chan domain.RunEvent) (domain.RunEvent, bool) {
	t.Helper()
	for {
		e, ok := next(t, ch)
		if !ok {
			return e, false
		}
	}
}

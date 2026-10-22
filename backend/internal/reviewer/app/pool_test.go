package app

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"

	"github.com/google/go-cmp/cmp"
)

func TestMapOrdered_KeepsItemOrder(t *testing.T) {
	// Later items finish first: item 0 waits for item 2.
	gate := make(chan struct{})
	got, err := mapOrdered([]int{0, 1, 2}, 3, func(item, _ int) (int, error) {
		switch item {
		case 0:
			<-gate
		case 2:
			close(gate)
		}
		return item * 10, nil
	})
	if err != nil {
		t.Fatalf("mapOrdered(...) error = %v, want nil", err)
	}
	if diff := cmp.Diff([]int{0, 10, 20}, got); diff != "" {
		t.Errorf("mapOrdered(...) mismatch (-want +got):\n%s", diff)
	}
}

func TestMapOrdered_LimitBoundsInFlight(t *testing.T) {
	for _, limit := range []int{0, 1, 2, 3} {
		var inFlight, peak atomic.Int32
		items := make([]int, 8)
		_, err := mapOrdered(items, limit, func(_, _ int) (struct{}, error) {
			n := inFlight.Add(1)
			for {
				p := peak.Load()
				if n <= p || peak.CompareAndSwap(p, n) {
					break
				}
			}
			inFlight.Add(-1)
			return struct{}{}, nil
		})
		if err != nil {
			t.Fatalf("mapOrdered(limit=%d) error = %v", limit, err)
		}
		if want := int32(max(limit, 1)); peak.Load() > want {
			t.Errorf("mapOrdered(limit=%d) peak in flight = %d, want <= %d", limit, peak.Load(), want)
		}
	}
}

func TestMapOrdered_FirstFailureStopsNewItemsAndAwaitsInFlight(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		boom := errors.New("boom")
		var (
			mu      sync.Mutex
			started []int
			done    atomic.Bool
		)
		release, started1 := make(chan struct{}), make(chan struct{})
		type outcome struct {
			got []int
			err error
		}
		out := make(chan outcome, 1)
		go func() {
			got, err := mapOrdered([]int{0, 1, 2, 3}, 2, func(item, _ int) (int, error) {
				mu.Lock()
				started = append(started, item)
				mu.Unlock()
				switch item {
				case 0:
					<-started1 // fail only once item 1 is in flight
					return 0, boom
				case 1: // in flight while item 0 fails; must still be awaited
					close(started1)
					<-release
					done.Store(true)
				}
				return item, nil
			})
			out <- outcome{got, err}
		}()
		// Item 0 has failed and item 1 is blocked: nothing else can happen until release.
		synctest.Wait()
		close(release)
		r := <-out
		if !errors.Is(r.err, boom) {
			t.Fatalf("mapOrdered(...) error = %v, want %v", r.err, boom)
		}
		if !done.Load() {
			t.Error("mapOrdered(...) returned before the in-flight call finished")
		}
		mu.Lock()
		defer mu.Unlock()
		for _, n := range started {
			if n >= 2 {
				t.Errorf("item %d started after the failure; started = %v", n, started)
			}
		}
	})
}

func TestMapOrdered_Empty(t *testing.T) {
	got, err := mapOrdered([]int(nil), 3, func(int, int) (int, error) { return 0, errors.New("unreachable") })
	if err != nil || len(got) != 0 {
		t.Errorf("mapOrdered(nil) = %v, %v, want empty, nil", got, err)
	}
}

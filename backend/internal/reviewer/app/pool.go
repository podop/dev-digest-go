package app

import "sync"

// mapOrdered runs fn over items with at most limit calls in flight and returns the results in item
// order, not completion order. After the first failure no further item starts; the calls already
// in flight are awaited, so their side effects (usage reports) land, and the first error to occur
// is returned. A limit below 1 means 1. A panic in fn stops the pool like a failure and, once the calls
// in flight have finished, is raised again on the caller's goroutine: a worker's own goroutine has
// no one to recover it, and the whole process would die.
func mapOrdered[T, R any](items []T, limit int, fn func(item T, index int) (R, error)) ([]R, error) {
	results := make([]R, len(items))
	var (
		mu       sync.Mutex
		next     int
		failed   bool
		firstErr error
		panicked bool
		panicVal any
	)
	// claim hands out the next item index, or false once the items are used up or a call failed.
	claim := func() (int, bool) {
		mu.Lock()
		defer mu.Unlock()
		if failed || next >= len(items) {
			return 0, false
		}
		i := next
		next++
		return i, true
	}
	fail := func(err error) {
		mu.Lock()
		defer mu.Unlock()
		if !failed {
			failed = true
			firstErr = err
		}
	}

	// crash records the first panic and stops the pool.
	crash := func(v any) {
		mu.Lock()
		defer mu.Unlock()
		failed = true
		if !panicked {
			panicked, panicVal = true, v
		}
	}

	width := max(1, min(limit, len(items)))
	var wg sync.WaitGroup
	for range width {
		wg.Go(func() {
			defer func() {
				if v := recover(); v != nil {
					crash(v)
				}
			}()
			for {
				i, ok := claim()
				if !ok {
					return
				}
				r, err := fn(items[i], i)
				if err != nil {
					fail(err)
					return
				}
				results[i] = r // each index is written by exactly one worker
			}
		})
	}
	wg.Wait()
	if panicked {
		panic(panicVal)
	}
	if failed {
		return nil, firstErr
	}
	return results, nil
}

package app_test

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/workspace/app"
	"dev-digest-go/backend/internal/workspace/domain"
)

// pruneCall is one PruneTraces call as the fake saw it.
type pruneCall struct {
	workspace string
	at        time.Time // when the call started (fake clock)
	cutoff    time.Time
}

// fakeTraces records PruneTraces calls; per-workspace behaviour is set before Run starts.
type fakeTraces struct {
	mu      sync.Mutex
	calls   []pruneCall
	deleted int64
	took    time.Duration
	failFor map[string]error
	panicOn string
	blockOn bool // wait for ctx instead of returning
}

func (f *fakeTraces) PruneTraces(ctx context.Context, ws string, cutoff time.Time) (int64, error) {
	f.mu.Lock()
	f.calls = append(f.calls, pruneCall{workspace: ws, at: time.Now(), cutoff: cutoff})
	failure, panicNow, block := f.failFor[ws], f.panicOn == ws, f.blockOn
	f.mu.Unlock()
	if panicNow {
		panic("boom")
	}
	if block {
		<-ctx.Done()
		return 0, ctx.Err()
	}
	time.Sleep(f.took)
	return f.deleted, failure
}

func (f *fakeTraces) snapshot() []pruneCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]pruneCall(nil), f.calls...)
}

type fakeWorkspaces struct {
	mu  sync.Mutex
	ids []string
	err error
}

func (f *fakeWorkspaces) WorkspaceIDs(context.Context) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.ids, f.err
}

func (f *fakeWorkspaces) put(ids []string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ids, f.err = ids, err
}

// fakeRetention serves a mutable per-workspace retention; unlisted workspaces get the default.
type fakeRetention struct {
	mu  sync.Mutex
	set map[string]domain.Retention
	err map[string]error
}

func (f *fakeRetention) Retention(_ context.Context, ws string) (domain.Retention, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.err[ws]; err != nil {
		return domain.Retention{}, err
	}
	if r, ok := f.set[ws]; ok {
		return r, nil
	}
	return domain.DefaultRetention(), nil
}

func (f *fakeRetention) put(ws string, r domain.Retention) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.set == nil {
		f.set = map[string]domain.Retention{}
	}
	f.set[ws] = r
}

// logRecord is a captured slog record.
type logRecord struct {
	level slog.Level
	msg   string
	attrs map[string]any
}

type recorder struct {
	mu   sync.Mutex
	recs []logRecord
}

func (r *recorder) Enabled(context.Context, slog.Level) bool { return true }
func (r *recorder) WithAttrs([]slog.Attr) slog.Handler       { return r }
func (r *recorder) WithGroup(string) slog.Handler            { return r }
func (r *recorder) Handle(_ context.Context, rec slog.Record) error {
	attrs := map[string]any{}
	rec.Attrs(func(a slog.Attr) bool {
		attrs[a.Key] = a.Value.Any()
		return true
	})
	r.mu.Lock()
	defer r.mu.Unlock()
	r.recs = append(r.recs, logRecord{level: rec.Level, msg: rec.Message, attrs: attrs})
	return nil
}

func (r *recorder) at(level slog.Level) []logRecord {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []logRecord
	for _, rec := range r.recs {
		if rec.level == level {
			out = append(out, rec)
		}
	}
	return out
}

// start runs the pruner in the current synctest bubble and returns a stop func that cancels it and
// waits for Run to return.
func start(t *testing.T, p *app.Pruner) (stop func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		defer close(done)
		p.Run(ctx)
	}()
	return func() {
		cancel()
		<-done
	}
}

func workspaceNames(calls []pruneCall) []string {
	var out []string
	for _, c := range calls {
		out = append(out, c.workspace)
	}
	return out
}

func TestPruner_RunsAtStartAndThen24hAfterThePreviousRunEnds(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		t0 := time.Now()
		traces := &fakeTraces{took: time.Hour, deleted: 3}
		log := &recorder{}
		p := app.NewPruner(&fakeWorkspaces{ids: []string{"w1"}}, &fakeRetention{}, traces, slog.New(log))
		stop := start(t, p)
		defer stop()

		time.Sleep(time.Minute) // let the first prune start
		synctest.Wait()
		calls := traces.snapshot()
		if len(calls) != 1 || !calls[0].at.Equal(t0) {
			t.Fatalf("after start: calls = %+v, want one call at %v", calls, t0)
		}
		if want := t0.Add(-90 * 24 * time.Hour); !calls[0].cutoff.Equal(want) {
			t.Errorf("cutoff = %v, want %v (default 90 days)", calls[0].cutoff, want)
		}

		// The first prune ended at t0+1h; 24 h later is t0+25h, not t0+24h.
		time.Sleep(24*time.Hour - time.Minute) // now t0+24h
		synctest.Wait()
		if n := len(traces.snapshot()); n != 1 {
			t.Fatalf("at t0+24h: %d prunes, want 1 (the interval counts from the end of the run)", n)
		}
		time.Sleep(time.Hour) // t0+25h
		synctest.Wait()
		calls = traces.snapshot()
		if len(calls) != 2 {
			t.Fatalf("at t0+25h: %d prunes, want 2", len(calls))
		}
		if want := t0.Add(25 * time.Hour); !calls[1].at.Equal(want) {
			t.Errorf("second prune at %v, want %v", calls[1].at, want)
		}

		infos := log.at(slog.LevelInfo)
		if len(infos) == 0 {
			t.Fatal("no INFO line logged")
		}
		got := infos[0]
		want := map[string]any{
			"workspace_id":   "w1",
			"retention_days": int64(90),
			"deleted":        int64(3),
			"duration_ms":    int64(time.Hour.Milliseconds()),
		}
		if diff := cmp.Diff(want, got.attrs); diff != "" {
			t.Errorf("INFO %q attrs mismatch (-want +got):\n%s", got.msg, diff)
		}
	})
}

func TestPruner_RereadsRetentionEveryRun(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		traces := &fakeTraces{}
		ret := &fakeRetention{}
		ret.put("w1", domain.Retention{Days: 30})
		ret.put("w2", domain.Retention{Days: 30})
		p := app.NewPruner(&fakeWorkspaces{ids: []string{"w1", "w2"}}, ret, traces, slog.New(&recorder{}))
		stop := start(t, p)
		defer stop()

		synctest.Wait()
		ret.put("w1", domain.Retention{Days: 365}) // changed between the runs
		ret.put("w2", domain.KeepForever())
		time.Sleep(app.DefaultPruneInterval)
		synctest.Wait()

		calls := traces.snapshot()
		if diff := cmp.Diff([]string{"w1", "w2", "w1"}, workspaceNames(calls)); diff != "" {
			t.Fatalf("pruned workspaces mismatch (-want +got):\n%s", diff)
		}
		for i, days := range map[int]int{0: 30, 1: 30, 2: 365} {
			if want := calls[i].at.Add(-time.Duration(days) * 24 * time.Hour); !calls[i].cutoff.Equal(want) {
				t.Errorf("call %d cutoff = %v, want now-%dd = %v", i, calls[i].cutoff, days, want)
			}
		}
	})
}

func TestPruner_KeepForeverSkipsAndLogs(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		traces := &fakeTraces{}
		ret := &fakeRetention{}
		ret.put("w1", domain.KeepForever())
		log := &recorder{}
		p := app.NewPruner(&fakeWorkspaces{ids: []string{"w1"}}, ret, traces, slog.New(log))
		stop := start(t, p)
		defer stop()
		synctest.Wait()

		if calls := traces.snapshot(); len(calls) != 0 {
			t.Errorf("PruneTraces called %d times for a keep-forever workspace, want 0", len(calls))
		}
		infos := log.at(slog.LevelInfo)
		if len(infos) != 1 || infos[0].attrs["workspace_id"] != "w1" {
			t.Errorf("INFO lines = %+v, want one for w1", infos)
		}
	})
}

func TestPruner_InvalidStoredRetentionWarnsAndDeletesNothing(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		traces := &fakeTraces{}
		ret := &fakeRetention{err: map[string]error{"w1": fmt.Errorf("45 days: %w", domain.ErrInvalidRetention)}}
		log := &recorder{}
		p := app.NewPruner(&fakeWorkspaces{ids: []string{"w1", "w2"}}, ret, traces, slog.New(log))
		stop := start(t, p)
		defer stop()
		synctest.Wait()

		if diff := cmp.Diff([]string{"w2"}, workspaceNames(traces.snapshot())); diff != "" {
			t.Errorf("pruned workspaces mismatch (-want +got):\n%s", diff)
		}
		warns := log.at(slog.LevelWarn)
		if len(warns) != 1 || warns[0].attrs["workspace_id"] != "w1" {
			t.Errorf("WARN lines = %+v, want one for w1", warns)
		}
		if errs := log.at(slog.LevelError); len(errs) != 0 {
			t.Errorf("ERROR lines = %+v, want none", errs)
		}
	})
}

func TestPruner_PruneErrorLogsAndTheLoopContinues(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		traces := &fakeTraces{failFor: map[string]error{"w1": errors.New("db down")}}
		log := &recorder{}
		p := app.NewPruner(&fakeWorkspaces{ids: []string{"w1", "w2"}}, &fakeRetention{}, traces, slog.New(log))
		stop := start(t, p)
		defer stop()
		synctest.Wait()

		// The failing workspace does not stop the next one in the same run ...
		if diff := cmp.Diff([]string{"w1", "w2"}, workspaceNames(traces.snapshot())); diff != "" {
			t.Fatalf("first run mismatch (-want +got):\n%s", diff)
		}
		errs := log.at(slog.LevelError)
		if len(errs) != 1 || errs[0].attrs["workspace_id"] != "w1" {
			t.Fatalf("ERROR lines = %+v, want one for w1", errs)
		}
		// ... and the next scheduled run still happens.
		time.Sleep(app.DefaultPruneInterval)
		synctest.Wait()
		if diff := cmp.Diff([]string{"w1", "w2", "w1", "w2"}, workspaceNames(traces.snapshot())); diff != "" {
			t.Errorf("second run mismatch (-want +got):\n%s", diff)
		}
	})
}

func TestPruner_ListFailureLogsAndTheNextRunHappens(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		traces := &fakeTraces{}
		ws := &fakeWorkspaces{err: errors.New("db down")}
		log := &recorder{}
		p := app.NewPruner(ws, &fakeRetention{}, traces, slog.New(log))
		stop := start(t, p)
		defer stop()
		synctest.Wait()
		if n := len(log.at(slog.LevelError)); n != 1 {
			t.Fatalf("ERROR lines after the first run = %d, want 1", n)
		}
		ws.put([]string{"w1"}, nil)
		time.Sleep(app.DefaultPruneInterval)
		synctest.Wait()
		if diff := cmp.Diff([]string{"w1"}, workspaceNames(traces.snapshot())); diff != "" {
			t.Errorf("second run mismatch (-want +got):\n%s", diff)
		}
	})
}

func TestPruner_PanicIsRecoveredAndTheLoopContinues(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		traces := &fakeTraces{panicOn: "w1"}
		log := &recorder{}
		p := app.NewPruner(&fakeWorkspaces{ids: []string{"w1", "w2"}}, &fakeRetention{}, traces, slog.New(log))
		stop := start(t, p)
		defer stop()
		synctest.Wait()

		if diff := cmp.Diff([]string{"w1", "w2"}, workspaceNames(traces.snapshot())); diff != "" {
			t.Fatalf("first run mismatch (-want +got):\n%s", diff)
		}
		if errs := log.at(slog.LevelError); len(errs) != 1 || errs[0].attrs["panic"] != "boom" {
			t.Errorf("ERROR lines = %+v, want one carrying panic=boom", errs)
		}
		time.Sleep(app.DefaultPruneInterval)
		synctest.Wait()
		if n := len(traces.snapshot()); n != 4 {
			t.Errorf("prunes after the second run = %d, want 4", n)
		}
	})
}

func TestPruner_StopsOnCancelWhileWaiting(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		traces := &fakeTraces{}
		p := app.NewPruner(&fakeWorkspaces{ids: []string{"w1"}}, &fakeRetention{}, traces, slog.New(&recorder{}))
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan struct{})
		go func() {
			defer close(done)
			p.Run(ctx)
		}()
		time.Sleep(time.Hour)
		synctest.Wait()

		t0 := time.Now()
		cancel()
		<-done
		if d := time.Since(t0); d > 5*time.Second {
			t.Errorf("Run returned %v after cancel, want <= 5s", d)
		}
		time.Sleep(2 * app.DefaultPruneInterval)
		synctest.Wait()
		if n := len(traces.snapshot()); n != 1 {
			t.Errorf("prunes after cancel = %d, want 1 (none after the stop)", n)
		}
	})
}

func TestPruner_StopsOnCancelDuringAPrune(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		traces := &fakeTraces{blockOn: true}
		log := &recorder{}
		p := app.NewPruner(&fakeWorkspaces{ids: []string{"w1", "w2"}}, &fakeRetention{}, traces, slog.New(log))
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan struct{})
		go func() {
			defer close(done)
			p.Run(ctx)
		}()
		synctest.Wait() // PruneTraces(w1) is blocked on ctx

		t0 := time.Now()
		cancel()
		<-done
		if d := time.Since(t0); d > 5*time.Second {
			t.Errorf("Run returned %v after cancel, want <= 5s", d)
		}
		if diff := cmp.Diff([]string{"w1"}, workspaceNames(traces.snapshot())); diff != "" {
			t.Errorf("pruned workspaces mismatch (-want +got):\n%s", diff)
		}
		if errs := log.at(slog.LevelError); len(errs) != 0 {
			t.Errorf("ERROR lines = %+v, want none for a shutdown", errs)
		}
	})
}

func TestPruner_Options(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		fixed := time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)
		traces := &fakeTraces{}
		p := app.NewPruner(&fakeWorkspaces{ids: []string{"w1"}}, &fakeRetention{}, traces, slog.New(&recorder{}),
			app.WithPruneInterval(time.Minute), app.WithPruneNow(func() time.Time { return fixed }))
		stop := start(t, p)
		defer stop()
		time.Sleep(90 * time.Second)
		synctest.Wait()

		calls := traces.snapshot()
		if len(calls) != 2 {
			t.Fatalf("prunes after 90s with a 1m interval = %d, want 2", len(calls))
		}
		if want := fixed.Add(-90 * 24 * time.Hour); !calls[0].cutoff.Equal(want) {
			t.Errorf("cutoff = %v, want %v from the injected clock", calls[0].cutoff, want)
		}
	})
}

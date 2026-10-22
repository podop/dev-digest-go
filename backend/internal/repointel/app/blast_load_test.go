package app_test

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/repointel/app"
	"dev-digest-go/backend/internal/repointel/domain"
)

// heapFiles is a clone of one declaring file and many large caller files; sample runs inside
// ReadCallerFile, that is while the scan is between two files.
type heapFiles struct {
	decl, caller []byte
	n            int
	sample       func(read int)
	reads        int
}

func (h *heapFiles) ScanCallers(ctx context.Context, root string) ([]string, error) {
	out := []string{"a/decl.ts"}
	for i := range h.n {
		out = append(out, fmt.Sprintf("b/c%04d.ts", i))
	}
	return out, nil
}

func (h *heapFiles) ReadCallerFile(root, rel string) ([]byte, error) {
	h.reads++
	if h.sample != nil {
		h.sample(h.reads)
	}
	if rel == "a/decl.ts" {
		return h.decl, nil
	}
	return h.caller, nil
}

func (h *heapFiles) ReadFile(root, rel string) ([]byte, error) { return h.ReadCallerFile(root, rel) }

type oneRepo struct{}

func (oneRepo) GetRepo(_ context.Context, id string) (domain.Repo, error) {
	return domain.Repo{ID: id, ClonePath: "/clone"}, nil
}

// F1 of the Phase 5 security review: the degraded scan kept the enclosing-symbol index of every
// caller file with a hit, and with it the whole text of the file (the symbol names and the row
// labels are substrings of it). The probe shape: a declaring file and many caller files that are
// all declarations plus one call below a declaration, so the row label is a substring of the
// text. The live heap, measured after GC while the scan is between two files, must not grow with
// the number of files scanned (before the fix it grew by about 2 times the file size per file).
func TestBlastRadius_ScanRetainsNoCallerFileText(t *testing.T) {
	const (
		files    = 32
		fileSize = 128 << 10
		bound    = 3 << 20 // before the fix about 2 x 4 MiB (decoded text and symbols of 31 files) stayed
	)
	var caller strings.Builder
	caller.WriteString("function outer() {}\nfoo();\n")
	for i := 0; caller.Len() < fileSize; i++ {
		fmt.Fprintf(&caller, "function f%d(){}\n", i)
	}
	hf := &heapFiles{decl: []byte("export function foo() {}\n"), caller: []byte(caller.String()), n: files}

	runtime.GC()
	var base runtime.MemStats
	runtime.ReadMemStats(&base)
	var peak uint64
	hf.sample = func(read int) {
		if read%8 != 0 && read != files {
			return
		}
		var ms runtime.MemStats
		runtime.GC()
		runtime.ReadMemStats(&ms)
		peak = max(peak, ms.HeapAlloc)
	}

	st := newFakeStore()
	r := app.NewReader(st, false, app.WithCallerScan(app.CallerScan{Repos: oneRepo{}, Files: hf, Analyzer: &fakeAnalyzer{}}))
	got, err := r.BlastRadius(t.Context(), repoID, []string{"a/decl.ts"})
	if err != nil {
		t.Fatalf("BlastRadius() error = %v", err)
	}
	if len(got.Callers) != files {
		t.Fatalf("BlastRadius() found %d callers, want %d (results must be unchanged)", len(got.Callers), files)
	}
	if want := (domain.BlastCaller{File: "b/c0000.ts", Symbol: "outer", ViaSymbol: "foo", Line: 2}); got.Callers[0] != want {
		t.Errorf("BlastRadius() first caller = %+v, want %+v", got.Callers[0], want)
	}
	if hf.reads < files {
		t.Fatalf("scan read %d files, want all %d", hf.reads, files)
	}
	delta := int64(peak) - int64(base.HeapAlloc)
	t.Logf("live heap growth while scanning: %d KiB", delta>>10)
	if delta > bound {
		t.Errorf("BlastRadius() live heap grew by %d KiB while scanning %d files of %d KiB, want under %d KiB (caller file text kept reachable)",
			delta>>10, files, fileSize>>10, int64(bound)>>10)
	}
}

// gateFiles is a clone whose listing waits on gate, and counts and bounds the scans in progress.
type gateFiles struct {
	files   map[string]string
	gate    chan struct{}
	started atomic.Int64
	active  atomic.Int64
	maxSeen atomic.Int64
}

func (g *gateFiles) ScanCallers(ctx context.Context, root string) ([]string, error) {
	g.started.Add(1)
	n := g.active.Add(1)
	defer g.active.Add(-1)
	for {
		m := g.maxSeen.Load()
		if n <= m || g.maxSeen.CompareAndSwap(m, n) {
			break
		}
	}
	select {
	case <-g.gate:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	out := make([]string, 0, len(g.files))
	for p := range g.files {
		out = append(out, p)
	}
	slices.Sort(out)
	return out, nil
}

func (g *gateFiles) ReadCallerFile(root, rel string) ([]byte, error) { return g.ReadFile(root, rel) }

func (g *gateFiles) ReadFile(root, rel string) ([]byte, error) {
	s, ok := g.files[rel]
	if !ok {
		return nil, fs.ErrNotExist
	}
	return []byte(s), nil
}

func gateReader(g *gateFiles) *app.Reader {
	return app.NewReader(newFakeStore(), false, app.WithCallerScan(app.CallerScan{Repos: oneRepo{}, Files: g, Analyzer: &fakeAnalyzer{}}))
}

// Callers asking for the same repo and changed files at the same time share one scan, whatever
// the order of their changed files, and each gets its own copy of the result.
func TestBlastRadius_IdenticalConcurrentCallsShareOneScan(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		g := &gateFiles{gate: make(chan struct{}), files: map[string]string{
			"src/decl.ts": declTS, "src/other.ts": "export function bar() {}\n", "src/use.ts": useTS,
		}}
		r := gateReader(g)
		const callers = 6
		var (
			wg   sync.WaitGroup
			mu   sync.Mutex
			outs []domain.BlastResult
			errs []error
		)
		for i := range callers {
			changed := []string{"src/decl.ts", "src/other.ts"}
			if i%2 == 1 {
				changed = []string{"src/other.ts", "src/decl.ts", "src/decl.ts"}
			}
			wg.Go(func() {
				res, err := r.BlastRadius(context.Background(), repoID, changed)
				mu.Lock()
				outs, errs = append(outs, res), append(errs, err)
				mu.Unlock()
			})
		}
		synctest.Wait()
		close(g.gate)
		wg.Wait()

		if n := g.started.Load(); n != 1 {
			t.Errorf("%d concurrent identical BlastRadius calls started %d scans, want 1", callers, n)
		}
		for _, err := range errs {
			if err != nil {
				t.Errorf("BlastRadius() error = %v", err)
			}
		}
		if len(outs) != callers || len(outs[0].Callers) == 0 {
			t.Fatalf("BlastRadius() answered %d results, first with %d callers, want %d results with callers", len(outs), len(outs[0].Callers), callers)
		}
		outs[0].Callers[0].Symbol = "mutated"
		for i, o := range outs[1:] {
			if o.Callers[0].Symbol == "mutated" {
				t.Errorf("BlastRadius() result %d shares its rows with another caller", i+1)
			}
			o.Callers[0].Symbol = outs[0].Callers[0].Symbol
			if diff := cmp.Diff(outs[0], o); diff != "" {
				t.Errorf("BlastRadius() results of one scan differ (-first +other):\n%s", diff)
			}
		}
	})
}

// A caller that leaves stops waiting at once and does not end the scan the others wait for.
func TestBlastRadius_ACallerLeavingKeepsTheSharedScan(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		g := &gateFiles{gate: make(chan struct{}), files: map[string]string{"src/decl.ts": declTS, "src/use.ts": useTS}}
		r := gateReader(g)
		leaver, cancel := context.WithCancel(context.Background())
		var (
			wg       sync.WaitGroup
			leftErr  error
			stayRes  domain.BlastResult
			stayErr  error
			changed  = []string{"src/decl.ts"}
			leftDone = make(chan struct{})
		)
		wg.Go(func() { _, leftErr = r.BlastRadius(leaver, repoID, changed); close(leftDone) })
		wg.Go(func() { stayRes, stayErr = r.BlastRadius(context.Background(), repoID, changed) })
		synctest.Wait()
		cancel()
		<-leftDone
		if !errors.Is(leftErr, context.Canceled) {
			t.Errorf("BlastRadius() of the caller that left error = %v, want context.Canceled", leftErr)
		}
		close(g.gate)
		wg.Wait()
		if stayErr != nil || len(stayRes.Callers) == 0 {
			t.Errorf("BlastRadius() of the caller that stayed = %d callers, error %v, want the scan result", len(stayRes.Callers), stayErr)
		}
		if n := g.started.Load(); n != 1 {
			t.Errorf("started %d scans, want 1", n)
		}
	})
}

// Scans of different changed sets run at most two at a time; a third waits for a slot and its
// caller can leave while it waits.
func TestBlastRadius_ScansAreLimitedAndWaitersHonourTheirContext(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		g := &gateFiles{gate: make(chan struct{}), files: map[string]string{
			"src/decl.ts": declTS, "src/other.ts": "export function bar() {}\n", "src/third.ts": "export function baz() {}\n", "src/use.ts": useTS,
		}}
		r := gateReader(g)
		var (
			wg   sync.WaitGroup
			errs = make([]error, 4)
		)
		call := func(ctx context.Context, i int, changed ...string) {
			wg.Go(func() { _, errs[i] = r.BlastRadius(ctx, repoID, changed) })
		}
		call(context.Background(), 0, "src/decl.ts")
		call(context.Background(), 1, "src/other.ts")
		synctest.Wait()
		waiter, cancel := context.WithCancel(context.Background())
		call(waiter, 2, "src/third.ts")
		synctest.Wait()
		if n := g.active.Load(); n != 2 {
			t.Errorf("with a third scan waiting for a slot, %d scans in progress, want 2", n)
		}
		cancel()
		synctest.Wait()
		if !errors.Is(errs[2], context.Canceled) {
			t.Errorf("BlastRadius() of a caller that left while its scan waited for a slot error = %v, want context.Canceled", errs[2])
		}
		call(context.Background(), 3, "src/decl.ts", "src/other.ts") // takes a slot once a scan is done
		synctest.Wait()
		close(g.gate)
		wg.Wait()
		for _, i := range []int{0, 1, 3} {
			if errs[i] != nil {
				t.Errorf("BlastRadius() of call %d error = %v", i, errs[i])
			}
		}
		if m := g.maxSeen.Load(); m != 2 {
			t.Errorf("most scans in progress at once = %d, want 2", m)
		}
		if n := g.started.Load(); n != 3 {
			t.Errorf("started %d scans, want 3 (the caller that left never began its scan)", n)
		}
	})
}

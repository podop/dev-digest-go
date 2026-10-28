package app_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/repointel/domain"
)

func TestFull_CleanIndexWritesEverything(t *testing.T) {
	e := newEnv(t)
	e.addFile("src/a.ts", "export function a() {}", fn("a", 1))
	e.addFile("src/b.ts", "app.get('/users', h)\nexport function b() {}", fn("b", 2))
	e.an.ex["src/a.ts"] = domain.Extraction{
		Symbols:    []domain.Symbol{fn("a", 1)},
		References: []domain.Reference{{Name: "b", Line: 1}},
	}
	e.graph.edges = []domain.Edge{{From: "src/a.ts", To: "src/b.ts"}}

	res, err := e.ix.Full(t.Context(), repoID)
	if err != nil {
		t.Fatalf("Full() error = %v", err)
	}
	if res.Status != domain.StatusFull || res.FilesIndexed != 2 || res.FilesSkipped != 0 {
		t.Errorf("Full() = %+v, want full, 2 indexed, 0 skipped", res)
	}
	st := e.state(t)
	if st.Status != domain.StatusFull || st.IndexerVersion != domain.IndexerVersion || st.LastIndexedSHA != "head-sha" ||
		st.FilesIndexed != 2 || st.FilesSkipped != 0 {
		t.Errorf("state = %+v, want full, the current version, head-sha, 2 indexed", st)
	}
	if !st.UpdatedAt.Equal(e.clock.t) {
		t.Errorf("state.UpdatedAt = %v, want the clock's %v", st.UpdatedAt, e.clock.t)
	}
	wantStats := map[string]int{
		"totalCandidates": 2, "skippedTooLarge": 0, "bounded": 0, "filesSeen": 2, "symbolsWritten": 2,
		"referencesWritten": 1, "edgesWritten": 1, "ranked": 2, "factsWritten": 1,
	}
	for k, want := range wantStats {
		if got := statInt(t, st, k); got != want {
			t.Errorf("stats[%s] = %d, want %d", k, got, want)
		}
	}
	if st.Stats["hotnessAvailable"] != false || st.Stats["softBudgetReached"] != false {
		t.Errorf("stats hotnessAvailable/softBudgetReached = %v/%v, want false/false", st.Stats["hotnessAvailable"], st.Stats["softBudgetReached"])
	}
	if _, ok := st.Stats["graphFailed"]; ok {
		t.Errorf("stats has graphFailed = %v on a clean pass", st.Stats["graphFailed"])
	}
	if got := degradedFiles(t, st); len(got) != 0 {
		t.Errorf("parseDegraded = %v, want []", got)
	}
	d := e.store.data
	if len(d.symbols) != 2 || len(d.refs) != 1 || len(d.edges) != 1 || len(d.ranks) != 2 || len(d.facts) != 1 {
		t.Errorf("rows symbols/refs/edges/ranks/facts = %d/%d/%d/%d/%d, want 2/1/1/2/1",
			len(d.symbols), len(d.refs), len(d.edges), len(d.ranks), len(d.facts))
	}
	if diff := cmp.Diff([]bool{false}, d.resolved); diff != "" {
		t.Errorf("ResolveReferences reset flags mismatch (-want +got):\n%s", diff)
	}
	if len(d.maps) != 1 || d.maps[0].CommitSHA != "head-sha" || d.maps[0].Budget != 1500 {
		t.Errorf("repo maps = %+v, want one for (head-sha, 1500)", d.maps)
	}
	if !strings.Contains(d.maps[0].Text, "function a()") {
		t.Errorf("repo map text = %q, want it to list the signatures", d.maps[0].Text)
	}
}

func TestFull_TransactionStartsWithTheRepoLock(t *testing.T) {
	e := newEnv(t)
	e.addFile("src/a.ts", "x", fn("a", 1))
	if _, err := e.ix.Full(t.Context(), repoID); err != nil {
		t.Fatalf("Full() error = %v", err)
	}
	var writes []string
	for _, c := range e.store.calls {
		if c != "GetRepo" && c != "GetState" && c != "MapCandidates" {
			writes = append(writes, c)
		}
	}
	if len(writes) < 2 || writes[0] != "LockRepo" || writes[1] != "DeleteRepoRows" {
		t.Errorf("first writer calls = %v, want LockRepo then DeleteRepoRows", writes)
	}
}

func TestFull_EndsWithoutIndexing(t *testing.T) {
	tests := []struct {
		name      string
		setup     func(e *env)
		wantState bool
		wantStats map[string]string
		wantRes   string
	}{
		{
			name:      "repo is not cloned",
			setup:     func(e *env) { r := e.store.repos[repoID]; r.ClonePath = ""; e.store.repos[repoID] = r },
			wantState: true,
			wantStats: map[string]string{"reason": "no_clone", "degradedReason": "no_data"},
			wantRes:   "no_clone",
		},
		{
			name:    "repo does not exist",
			setup:   func(e *env) { delete(e.store.repos, repoID) },
			wantRes: "repo_not_found",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			tc.setup(e)
			res, err := e.ix.Full(t.Context(), repoID)
			if err != nil {
				t.Fatalf("Full() error = %v", err)
			}
			if res.Status != domain.StatusDegraded || res.Reason != tc.wantRes {
				t.Errorf("Full() = %+v, want degraded with reason %q", res, tc.wantRes)
			}
			if !tc.wantState {
				e.noState(t)
				return
			}
			st := e.state(t)
			if st.Status != domain.StatusDegraded || st.LastIndexedSHA != "" || st.FilesIndexed != 0 || st.FilesSkipped != 0 ||
				st.IndexerVersion != domain.IndexerVersion {
				t.Errorf("state = %+v, want degraded, empty sha, zero counters, the current version", st)
			}
			for k, want := range tc.wantStats {
				if got := statStr(st, k); got != want {
					t.Errorf("stats[%s] = %q, want %q", k, got, want)
				}
			}
			if _, ok := st.Stats["durationMs"]; !ok {
				t.Error("stats has no durationMs")
			}
		})
	}
}

func TestFull_NoFilesLeavesTheTablesAlone(t *testing.T) {
	e := newEnv(t)
	e.seedIndex(domain.StatusFull)
	before := e.store.snapshot()
	e.clone.walk = &domain.WalkResult{Stats: domain.WalkStats{TotalCandidates: 2, SkippedTooLarge: 2}}

	res, err := e.ix.Full(t.Context(), repoID)
	if err != nil {
		t.Fatalf("Full() error = %v", err)
	}
	if res.Status != domain.StatusPartial || res.Reason != "no_files" {
		t.Errorf("Full() = %+v, want partial / no_files", res)
	}
	st := e.state(t)
	if st.Status != domain.StatusPartial || st.FilesIndexed != 0 || st.FilesSkipped != 2 || statStr(st, "reason") != "no_files" || st.LastIndexedSHA != "head-sha" {
		t.Errorf("state = %+v, want partial, 0 indexed, 2 skipped, reason no_files", st)
	}
	after := e.store.snapshot()
	after.states = nil
	before.states = nil
	if diff := cmp.Diff(before, after, cmp.AllowUnexported(storeData{})); diff != "" {
		t.Errorf("tables changed (-before +after):\n%s", diff)
	}
}

func TestFull_HeadUnreadableIndexesWithoutMap(t *testing.T) {
	e := newEnv(t)
	e.git.headErr = errors.New("not a git repository")
	e.addFile("src/a.ts", "x", fn("a", 1))
	if _, err := e.ix.Full(t.Context(), repoID); err != nil {
		t.Fatalf("Full() error = %v", err)
	}
	if st := e.state(t); st.LastIndexedSHA != "" || st.Status != domain.StatusFull {
		t.Errorf("state = %+v, want full with an empty sha", st)
	}
	if len(e.store.data.maps) != 0 {
		t.Errorf("repo maps = %+v, want none without a sha", e.store.data.maps)
	}
}

func TestFull_SoftBudgetStopsParsingAndKeepsGraphTables(t *testing.T) {
	e := newEnv(t)
	files := manyFiles(e, 20)
	e.seedIndex(domain.StatusFull)
	e.graph.edges = []domain.Edge{{From: files[0], To: files[1]}}
	var parsed []string
	e.an.onExtract = func(p string) {
		parsed = append(parsed, p)
		if len(parsed) == 10 {
			e.clock.Advance(domain.SoftBudget)
		}
	}
	prev := e.store.snapshot()

	res, err := e.ix.Full(t.Context(), repoID)
	if err != nil {
		t.Fatalf("Full() error = %v", err)
	}
	if diff := cmp.Diff(files[:10], parsed); diff != "" {
		t.Errorf("parsed files mismatch (-want +got):\n%s", diff)
	}
	st := e.state(t)
	if res.Status != domain.StatusPartial || st.Status != domain.StatusPartial || st.Stats["softBudgetReached"] != true {
		t.Errorf("Full() = %+v, state %+v, want partial with softBudgetReached", res, st)
	}
	if st.FilesIndexed != 10 {
		t.Errorf("state.FilesIndexed = %d, want 10", st.FilesIndexed)
	}
	d := e.store.data
	if diff := cmp.Diff(prev.edges, d.edges); diff != "" {
		t.Errorf("file_edges changed (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff(prev.ranks, d.ranks); diff != "" {
		t.Errorf("file_rank changed (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff(prev.maps, d.maps); diff != "" {
		t.Errorf("repo_map_cache changed (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff(prev.facts, d.facts); diff != "" {
		t.Errorf("file_facts changed (-want +got):\n%s", diff)
	}
	if len(d.symbols) != 10 {
		t.Errorf("symbols = %d rows, want the 10 parsed files' rows", len(d.symbols))
	}
}

func TestFull_RowBudgetStopsParsingAndKeepsGraphTables(t *testing.T) {
	// Each file holds 1 symbol and 3 references = 4 rows; with a budget of 10 rows the third file
	// reaches it (12 rows) and the fourth is never started.
	e := newEnv(t, withMaxRows(10))
	files := manyFiles(e, 6)
	for _, f := range files {
		ex := e.an.ex[f]
		ex.References = []domain.Reference{{Name: "x", Line: 1}, {Name: "y", Line: 2}, {Name: "z", Line: 3}}
		e.an.ex[f] = ex
	}
	e.seedIndex(domain.StatusFull)
	e.graph.edges = []domain.Edge{{From: files[0], To: files[1]}}
	var parsed []string
	e.an.onExtract = func(p string) { parsed = append(parsed, p) }
	prev := e.store.snapshot()

	res, err := e.ix.Full(t.Context(), repoID)
	if err != nil {
		t.Fatalf("Full() error = %v", err)
	}
	if diff := cmp.Diff(files[:3], parsed); diff != "" {
		t.Errorf("parsed files mismatch (-want +got):\n%s", diff)
	}
	st := e.state(t)
	if res.Status != domain.StatusPartial || st.Status != domain.StatusPartial || res.Reason != "row_budget" {
		t.Errorf("Full() = %+v, state status %q, want partial / row_budget", res, st.Status)
	}
	if st.Stats["rowBudgetReached"] != true || st.Stats["softBudgetReached"] != true {
		t.Errorf("stats rowBudgetReached/softBudgetReached = %v/%v, want true/true", st.Stats["rowBudgetReached"], st.Stats["softBudgetReached"])
	}
	if st.FilesIndexed != 3 {
		t.Errorf("state.FilesIndexed = %d, want 3", st.FilesIndexed)
	}
	d := e.store.data
	if len(d.symbols) != 3 || len(d.refs) != 9 {
		t.Errorf("rows = %d symbols / %d references, want 3 / 9 (the parsed files only)", len(d.symbols), len(d.refs))
	}
	for name, got := range map[string]any{"edges": d.edges, "ranks": d.ranks, "maps": d.maps, "facts": d.facts} {
		want := map[string]any{"edges": prev.edges, "ranks": prev.ranks, "maps": prev.maps, "facts": prev.facts}[name]
		if diff := cmp.Diff(want, got); diff != "" {
			t.Errorf("%s changed (-want +got):\n%s", name, diff)
		}
	}
}

func TestFull_UnderTheRowBudgetIsUnchanged(t *testing.T) {
	e := newEnv(t, withMaxRows(1000))
	manyFiles(e, 6)
	res, err := e.ix.Full(t.Context(), repoID)
	if err != nil {
		t.Fatalf("Full() error = %v", err)
	}
	st := e.state(t)
	if _, has := st.Stats["rowBudgetReached"]; res.Status != domain.StatusFull || has || st.FilesIndexed != 6 {
		t.Errorf("Full() = %+v, state %+v, want full, 6 files and no rowBudgetReached key", res, st)
	}
}

func TestFull_GraphBuilderFailure(t *testing.T) {
	tests := []struct {
		name  string
		graph fakeGraph
		want  string
	}{
		{name: "error", graph: fakeGraph{err: errors.New("resolver broke, key sk-secret")}, want: "resolver broke, key [redacted]"},
		{name: "panic", graph: fakeGraph{panics: true}, want: "import graph panicked: graph exploded"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			e.addFile("src/a.ts", "x", fn("a", 1))
			e.addFile("src/b.ts", "x", fn("b", 1))
			*e.graph = tc.graph
			e.graph.edges = nil
			res, err := e.ix.Full(t.Context(), repoID)
			if err != nil {
				t.Fatalf("Full() error = %v", err)
			}
			st := e.state(t)
			if res.Status != domain.StatusPartial || st.Status != domain.StatusPartial {
				t.Errorf("Full() = %+v, state %+v, want partial", res, st)
			}
			if got := statStr(st, "graphFailed"); got != tc.want {
				t.Errorf("stats[graphFailed] = %q, want %q", got, tc.want)
			}
			if len(e.store.data.edges) != 0 {
				t.Errorf("file_edges = %v, want none", e.store.data.edges)
			}
			if len(e.store.data.ranks) != 2 {
				t.Errorf("file_rank rows = %d, want a row per walked file", len(e.store.data.ranks))
			}
		})
	}
}

func TestFull_CancelledGraphBuildStopsTheIndex(t *testing.T) {
	e := newEnv(t)
	e.addFile("src/a.ts", "x", fn("a", 1))
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	e.graph.err = context.Canceled
	e.an.onExtract = func(string) { cancel() }
	if _, err := e.ix.Full(ctx, repoID); !errors.Is(err, context.Canceled) {
		t.Errorf("Full() error = %v, want context.Canceled", err)
	}
	e.noState(t)
}

func TestFull_FailedFilesAreReportedAndContributeNoRows(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newEnv(t, withWorkers(2))
		e.addFile("src/ok.ts", "x", fn("ok", 1))
		e.addFile("src/slow.ts", "x", fn("slow", 1))
		e.addFile("src/unreadable.ts", "x", fn("unreadable", 1))
		e.addFile("src/boom.ts", "x", fn("boom", 1))
		e.an.hang = map[string]bool{"src/slow.ts": true}
		e.an.panicOn = map[string]bool{"src/boom.ts": true}
		e.clone.unreadable["src/unreadable.ts"] = true

		res, err := e.ix.Full(t.Context(), repoID)
		if err != nil {
			t.Fatalf("Full() error = %v", err)
		}
		st := e.state(t)
		if res.Status != domain.StatusPartial || st.FilesIndexed != 1 || st.FilesSkipped != 3 {
			t.Errorf("Full() = %+v, state %+v, want partial, 1 indexed, 3 skipped", res, st)
		}
		want := []string{
			"src/boom.ts: parser panicked: boom",
			"src/slow.ts: parse timed out after 2000 ms",
			"src/unreadable.ts: read src/unreadable.ts: no such file",
		}
		if diff := cmp.Diff(want, degradedFiles(t, st)); diff != "" {
			t.Errorf("parseDegraded mismatch (-want +got):\n%s", diff)
		}
		if len(e.store.data.symbols) != 1 || e.store.data.symbols[0].Path != "src/ok.ts" {
			t.Errorf("symbols = %+v, want only src/ok.ts's", e.store.data.symbols)
		}
		if len(e.store.data.ranks) != 4 {
			t.Errorf("file_rank rows = %d, want a row for every walked file", len(e.store.data.ranks))
		}
	})
}

func TestFull_SlowParseGetsItsOwnTwoSecondLimit(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newEnv(t, withWorkers(1))
		e.addFile("src/a.ts", "x", fn("a", 1))
		e.addFile("src/b.ts", "x", fn("b", 1))
		e.an.hang = map[string]bool{"src/a.ts": true}
		start := time.Now()
		if _, err := e.ix.Full(t.Context(), repoID); err != nil {
			t.Fatalf("Full() error = %v", err)
		}
		if got := time.Since(start); got != domain.ParseTimeout {
			t.Errorf("Full() took %v of fake time, want %v", got, domain.ParseTimeout)
		}
		if st := e.state(t); st.FilesIndexed != 1 {
			t.Errorf("state.FilesIndexed = %d, want the other file indexed", st.FilesIndexed)
		}
	})
}

func TestFull_ParseDegradedIsCappedAtFifty(t *testing.T) {
	e := newEnv(t)
	for _, f := range manyFiles(e, 60) {
		e.clone.unreadable[f] = true
	}
	if _, err := e.ix.Full(t.Context(), repoID); err != nil {
		t.Fatalf("Full() error = %v", err)
	}
	st := e.state(t)
	if got := len(degradedFiles(t, st)); got != 50 {
		t.Errorf("len(parseDegraded) = %d, want 50", got)
	}
	if st.FilesSkipped != 60 {
		t.Errorf("state.FilesSkipped = %d, want 60", st.FilesSkipped)
	}
}

func TestFull_DecodesInvalidUTF8BeforeHashingAndParsing(t *testing.T) {
	e := newEnv(t)
	raw := []byte("const a = 1;\x00\xff\xe2\x82 end")
	e.addFile("src/a.ts", "", fn("a", 1))
	e.clone.files["src/a.ts"] = raw
	if _, err := e.ix.Full(t.Context(), repoID); err != nil {
		t.Fatalf("Full() error = %v", err)
	}
	wantText := domain.DecodeText(raw)
	if got := e.an.seen["src/a.ts"]; got != wantText || !strings.Contains(got, "�") {
		t.Errorf("analyzer text = %q, want the U+FFFD-decoded %q", got, wantText)
	}
	if got, want := e.store.data.symbols[0].ContentHash, domain.ContentHash(wantText); got != want {
		t.Errorf("content hash = %s, want %s", got, want)
	}
}

func TestFull_SkippedTooLargeFilesCountAsSkipped(t *testing.T) {
	e := newEnv(t)
	e.addFile("src/a.ts", "x", fn("a", 1))
	e.clone.walk = &domain.WalkResult{Files: []string{"src/a.ts"}, Stats: domain.WalkStats{TotalCandidates: 3, SkippedTooLarge: 2, Bounded: 0}}
	if _, err := e.ix.Full(t.Context(), repoID); err != nil {
		t.Fatalf("Full() error = %v", err)
	}
	if st := e.state(t); st.FilesIndexed != 1 || st.FilesSkipped != 2 || statInt(t, st, "skippedTooLarge") != 2 {
		t.Errorf("state = %+v, want 1 indexed, 2 skipped", st)
	}
}

func TestFull_ParsesWithBoundedParallelism(t *testing.T) {
	e := newEnv(t, withWorkers(3))
	manyFiles(e, 30)
	var (
		mu            sync.Mutex
		running, peak int
	)
	e.an.onExtract = func(string) {
		mu.Lock()
		running++
		peak = max(peak, running)
		mu.Unlock()
		runtime.Gosched()
		mu.Lock()
		running--
		mu.Unlock()
	}
	if _, err := e.ix.Full(t.Context(), repoID); err != nil {
		t.Fatalf("Full() error = %v", err)
	}
	if peak < 1 || peak > 3 {
		t.Errorf("peak parallel parses = %d, want between 1 and 3", peak)
	}
	if st := e.state(t); st.FilesIndexed != 30 {
		t.Errorf("state.FilesIndexed = %d, want 30", st.FilesIndexed)
	}
}

func TestFull_FailureRules(t *testing.T) {
	long := strings.Repeat("sk-secret ", 100)
	tests := []struct {
		name       string
		prev       domain.Status // "" = no state row
		wantStatus domain.Status
	}{
		{name: "no state row becomes failed", wantStatus: domain.StatusFailed},
		{name: "degraded state becomes failed", prev: domain.StatusDegraded, wantStatus: domain.StatusFailed},
		{name: "failed state is rewritten", prev: domain.StatusFailed, wantStatus: domain.StatusFailed},
		{name: "full state is kept", prev: domain.StatusFull, wantStatus: domain.StatusFull},
		{name: "partial state is kept", prev: domain.StatusPartial, wantStatus: domain.StatusPartial},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			e.addFile("src/a.ts", "x", fn("a", 1))
			if tc.prev != "" {
				e.seedIndex(tc.prev)
			}
			prevRows := e.store.snapshot()
			prevState, hadState := e.store.states[repoID]
			e.store.hooks["InsertSymbols"] = func(context.Context) error { return errors.New(long) }

			_, err := e.ix.Full(t.Context(), repoID)
			if err == nil {
				t.Fatal("Full() error = nil, want the database error")
			}
			st := e.state0(repoID)
			if st == nil || st.Status != tc.wantStatus {
				t.Fatalf("state = %+v, want status %s", st, tc.wantStatus)
			}
			if tc.wantStatus == domain.StatusFailed {
				got := *st
				if statStr(got, "reason") != "index_failed" || statStr(got, "degradedReason") != "index_failed" {
					t.Errorf("stats = %v, want reason and degradedReason index_failed", got.Stats)
				}
				msg := statStr(got, "error")
				if domain.UTF16Len(msg) > 500 || strings.Contains(msg, "sk-secret") || !strings.Contains(msg, "[redacted]") {
					t.Errorf("stats[error] = %q (%d units), want a redacted text of at most 500", msg, domain.UTF16Len(msg))
				}
				if got.FilesIndexed != 0 || got.FilesSkipped != 0 || got.LastIndexedSHA != prevState.LastIndexedSHA {
					t.Errorf("failed state = %+v, want zero counters and the previous sha %q", got, prevState.LastIndexedSHA)
				}
			} else if diff := cmp.Diff(prevState, *st); diff != "" {
				t.Errorf("state changed (-want +got):\n%s", diff)
			}
			_ = hadState
			now := e.store.snapshot()
			now.states, prevRows.states = nil, nil
			if diff := cmp.Diff(prevRows, now, cmp.AllowUnexported(storeData{})); diff != "" {
				t.Errorf("tables changed by the failed attempt (-before +after):\n%s", diff)
			}
		})
	}
}

func TestFull_TimeoutRollsBackAndRecordsFailure(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newEnv(t)
		e.addFile("src/a.ts", "x", fn("a", 1))
		e.store.hooks["LockRepo"] = blockUntilDone
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()

		_, err := e.ix.Full(ctx, repoID)
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("Full() error = %v, want a deadline error", err)
		}
		st := e.state(t)
		if st.Status != domain.StatusFailed || statStr(st, "reason") != "index_failed" {
			t.Errorf("state = %+v, want failed / index_failed", st)
		}
		if len(e.store.data.symbols) != 0 {
			t.Errorf("symbols = %v, want none after the rollback", e.store.data.symbols)
		}
	})
}

func TestFull_ShutdownWritesNothing(t *testing.T) {
	e := newEnv(t)
	e.addFile("src/a.ts", "x", fn("a", 1))
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	e.store.hooks["LockRepo"] = func(ctx context.Context) error {
		cancel()
		return ctx.Err()
	}
	if _, err := e.ix.Full(ctx, repoID); !errors.Is(err, context.Canceled) {
		t.Errorf("Full() error = %v, want context.Canceled", err)
	}
	e.noState(t)
}

func TestFull_VanishedRepoCreatesNoFailedState(t *testing.T) {
	e := newEnv(t)
	e.addFile("src/a.ts", "x", fn("a", 1))
	e.store.hooks["InsertSymbols"] = func(context.Context) error {
		delete(e.store.repos, repoID)
		return fmt.Errorf("insert symbols: %w", domain.ErrRepoNotFound)
	}
	if _, err := e.ix.Full(t.Context(), repoID); !errors.Is(err, domain.ErrRepoNotFound) {
		t.Errorf("Full() error = %v, want ErrRepoNotFound", err)
	}
	e.noState(t)
}

func TestFull_AFailingEarlyStateWriteDoesNotFailTheIndex(t *testing.T) {
	var buf bytes.Buffer
	e := newEnv(t, withLog(slog.New(slog.NewTextHandler(&buf, nil))))
	r := e.store.repos[repoID]
	r.ClonePath = ""
	e.store.repos[repoID] = r
	e.store.hooks["UpsertState"] = func(context.Context) error { return errors.New("db down") }
	res, err := e.ix.Full(t.Context(), repoID)
	if err != nil || res.Reason != "no_clone" {
		t.Errorf("Full() = %+v, %v, want the no_clone result without an error", res, err)
	}
	if !strings.Contains(buf.String(), "db down") {
		t.Errorf("log = %q, want the ignored write failure", buf.String())
	}
}

func TestFull_ErrorFromWalkIsReturned(t *testing.T) {
	e := newEnv(t)
	e.clone.walkErr = fmt.Errorf("walk clone: %w", domain.ErrCloneMissing)
	if _, err := e.ix.Full(t.Context(), repoID); !errors.Is(err, domain.ErrCloneMissing) {
		t.Errorf("Full() error = %v, want ErrCloneMissing", err)
	}
	if st := e.state(t); st.Status != domain.StatusFailed {
		t.Errorf("state = %+v, want failed", st)
	}
}

func TestFull_WritesFilePackages(t *testing.T) {
	e := newEnv(t)
	e.addFile("a/a.go", "package a", fn("a", 1))
	e.graph.packages = []domain.FilePackage{{Path: "a/a.go", Key: "example.com/m/a"}}

	if _, err := e.ix.Full(t.Context(), repoID); err != nil {
		t.Fatalf("Full() error = %v", err)
	}
	if diff := cmp.Diff(e.graph.packages, e.store.data.pkgs); diff != "" {
		t.Errorf("file_packages mismatch (-want +got):\n%s", diff)
	}
	if got, want := callIndex(e.store.calls, "ReplaceFilePackages"), callIndex(e.store.calls, "ResolveReferences"); got < 0 || got > want {
		t.Errorf("ReplaceFilePackages at %d, ResolveReferences at %d in %v, want packages first", got, want, e.store.calls)
	}
}

func TestFull_BudgetStopLeavesFilePackages(t *testing.T) {
	e := newEnv(t)
	files := manyFiles(e, 20)
	e.seedIndex(domain.StatusFull)
	e.graph.packages = []domain.FilePackage{{Path: files[0], Key: "k"}}
	e.an.onExtract = func(string) { e.clock.Advance(domain.SoftBudget) }

	if _, err := e.ix.Full(t.Context(), repoID); err != nil {
		t.Fatalf("Full() error = %v", err)
	}
	if len(e.store.data.pkgs) != 0 || callIndex(e.store.calls, "ReplaceFilePackages") >= 0 {
		t.Errorf("file_packages = %v, calls %v, want no package write after a budget stop", e.store.data.pkgs, e.store.calls)
	}
}

// callIndex is the position of the first call named name, or -1.
func callIndex(calls []string, name string) int { return slices.Index(calls, name) }

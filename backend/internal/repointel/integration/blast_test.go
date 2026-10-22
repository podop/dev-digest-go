package integration_test

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/repointel/adapters/clonetree"
	"dev-digest-go/backend/internal/repointel/adapters/treesitter"
	"dev-digest-go/backend/internal/repointel/app"
	"dev-digest-go/backend/internal/repointel/domain"
)

// blastReader is the read port with the clone scan wired like production; enabled is
// REPO_INTEL_ENABLED.
func (e *env) blastReader(enabled bool) *app.Reader {
	e.t.Helper()
	return app.NewReader(e.store, enabled, app.WithCallerScan(app.CallerScan{
		Repos: e.store, Files: clonetree.New(), Analyzer: treesitter.New(),
	}))
}

func (e *env) blast(r *app.Reader, changed ...string) domain.BlastResult {
	e.t.Helper()
	got, err := r.BlastRadius(e.t.Context(), e.repo, changed)
	if err != nil {
		e.t.Fatalf("BlastRadius(%v) error = %v", changed, err)
	}
	return got
}

// setState writes the repo's state row.
func (e *env) setState(status, stats string) {
	e.t.Helper()
	e.exec(`INSERT INTO repo_index_state (repo_id, last_indexed_sha, indexer_version, status, stats)
		VALUES (?, 'sha', ?, ?, ?::jsonb)
		ON CONFLICT (repo_id) DO UPDATE SET status = EXCLUDED.status, stats = EXCLUDED.stats`,
		e.repo, domain.IndexerVersion, status, stats)
}

func (e *env) addSymbol(path, name, kind string, line int) {
	e.t.Helper()
	e.exec(`INSERT INTO symbols (repo_id, path, name, kind, line, end_line) VALUES (?, ?, ?, ?, ?, ?)`, e.repo, path, name, kind, line, line)
}

// addRef inserts a reference; declFile "" is an unresolved one (NULL).
func (e *env) addRef(from, to string, line int, declFile string) {
	e.t.Helper()
	var decl any
	if declFile != "" {
		decl = declFile
	}
	e.exec(`INSERT INTO "references" (repo_id, from_path, to_symbol, line, decl_file) VALUES (?, ?, ?, ?, ?)`, e.repo, from, to, line, decl)
}

func (e *env) addRank(path string, rank float64) {
	e.t.Helper()
	e.exec(`INSERT INTO file_rank (repo_id, file_path, pagerank, hotness, rank, percentile) VALUES (?, ?, 0, 0, ?, 50)`, e.repo, path, rank)
}

func (e *env) addFacts(path, endpoints, crons string) {
	e.t.Helper()
	e.exec(`INSERT INTO file_facts (repo_id, file_path, endpoints, crons) VALUES (?, ?, ?::jsonb, ?::jsonb)`, e.repo, path, endpoints, crons)
}

// AC5038: the persistent path over a real index of a small project.
func TestBlastRadius_RealIndex(t *testing.T) {
	files := baseFiles()
	files["src/routes.ts"] = "import { core } from './core';\n\nconst app = makeApp();\napp.get('/core', () => core());\n"
	e := newEnv(t, withFiles(files))
	e.mustFull()
	got := e.blast(e.reader, "src/core.ts")

	if got.Degraded || got.Reason != "" {
		t.Errorf("BlastRadius() of a full index = degraded %v reason %q, want complete", got.Degraded, got.Reason)
	}
	wantSyms := []domain.BlastSymbol{{File: "src/core.ts", Name: "core", Kind: "function"}, {File: "src/core.ts", Name: "Store", Kind: "class"}}
	if diff := cmp.Diff(wantSyms, got.ChangedSymbols); diff != "" {
		t.Errorf("BlastRadius() changed symbols mismatch (-want +got):\n%s", diff)
	}
	byFile := map[string]domain.BlastCaller{}
	for _, c := range got.Callers {
		byFile[c.File] = c
		if c.ViaSymbol != "core" || c.Rank <= 0 {
			t.Errorf("BlastRadius() caller %+v, want a caller of core with a rank", c)
		}
	}
	wantCallers := map[string]string{"src/a.ts": "runA", "src/b.ts": "runB", "src/c.ts": "runC", "src/core.test.ts": "core.test.ts", "src/routes.ts": "routes.ts"}
	for file, sym := range wantCallers {
		if c, ok := byFile[file]; !ok || c.Symbol != sym {
			t.Errorf("BlastRadius() caller in %s = %+v, want symbol %q", file, c, sym)
		}
	}
	if len(got.Callers) != len(wantCallers) {
		t.Errorf("BlastRadius() returned %d callers (%+v), want %d", len(got.Callers), got.Callers, len(wantCallers))
	}
	if !slices.IsSortedFunc(got.Callers, func(a, b domain.BlastCaller) int {
		switch {
		case a.Rank > b.Rank:
			return -1
		case a.Rank < b.Rank:
			return 1
		}
		return 0
	}) {
		t.Errorf("BlastRadius() callers %+v, want rank descending", got.Callers)
	}
	if diff := cmp.Diff(domain.BlastFacts{Endpoints: []string{"GET /core"}, Crons: []string{}}, got.FactsByFile["src/routes.ts"]); diff != "" {
		t.Errorf("BlastRadius() facts of the route file mismatch (-want +got):\n%s", diff)
	}
}

// AC5038: crafted index rows: qualified names, ambiguous references, more than 20 callers of one
// symbol, rank ties, files without a file_rank row, facts of dropped callers.
func TestBlastRadius_PersistentCorpus(t *testing.T) {
	e := newEnv(t, withFiles(map[string]string{"x.ts": "export const x = 1;\n"}))
	e.setState("full", `{}`)
	// declarations: foo and the class Cls with its method (both forms) in the changed file
	e.addSymbol("src/core.ts", "foo", "function", 1)
	e.addSymbol("src/core.ts", "Cls", "class", 10)
	e.addSymbol("src/core.ts", "Cls.run", "method", 11)
	e.addSymbol("src/core.ts", "run", "method", 11)
	e.addSymbol("src/other.ts", "foo", "function", 1) // another file's foo is not a changed symbol

	// callers of foo: 25 files with distinct ranks; the 5 worst are cut by the cap
	for i := range 25 {
		f := fmt.Sprintf("src/u%02d.ts", i)
		e.addSymbol(f, fmt.Sprintf("use%02d", i), "function", 1)
		e.addRef(f, "foo", 3, "src/core.ts")
		e.addRank(f, float64(100-i))
		e.addFacts(f, fmt.Sprintf(`["GET /u%02d"]`, i), `[]`)
	}
	// rank ties are ordered by path, then line, then symbol
	for _, f := range []string{"src/t2.ts", "src/t1.ts"} {
		e.addSymbol(f, "tie", "function", 1)
		e.addRef(f, "Cls", 5, "src/core.ts")
		e.addRef(f, "Cls", 2, "src/core.ts") // same enclosing symbol: only the line 2 row is kept... after ordering by line
		e.addRank(f, 1.5)
	}
	// a reference resolved to the changed file but from a file with no file_rank row, an ambiguous
	// reference and a reference resolved to another file are no callers
	e.addRef("src/norank.ts", "foo", 1, "src/core.ts")
	e.addRef("src/ambiguous.ts", "foo", 1, "")
	e.addRank("src/ambiguous.ts", 99)
	e.addRef("src/elsewhere.ts", "foo", 1, "src/other.ts")
	e.addRank("src/elsewhere.ts", 99)
	// a caller whose file has two top-level symbols, the later before the line and a qualified one
	e.addSymbol("src/two.ts", "early", "function", 1)
	e.addSymbol("src/two.ts", "late", "function", 20)
	e.addSymbol("src/two.ts", "Cls.run", "method", 25)
	e.addRef("src/two.ts", "run", 30, "src/core.ts")
	e.addRank("src/two.ts", 1.2)
	e.addFacts("src/two.ts", `[]`, `["0 * * * *","job:index"]`)

	got := e.blast(e.reader, "src/core.ts")

	wantSyms := []domain.BlastSymbol{
		{File: "src/core.ts", Name: "foo", Kind: "function"}, {File: "src/core.ts", Name: "Cls", Kind: "class"}, {File: "src/core.ts", Name: "run", Kind: "method"},
	}
	if diff := cmp.Diff(wantSyms, got.ChangedSymbols); diff != "" {
		t.Errorf("BlastRadius() changed symbols mismatch (-want +got):\n%s", diff)
	}
	var viaFoo []string
	for _, c := range got.Callers {
		if c.ViaSymbol == "foo" {
			viaFoo = append(viaFoo, c.File)
		}
	}
	if len(viaFoo) != domain.MaxCallersPerSymbol || viaFoo[0] != "src/u00.ts" || viaFoo[19] != "src/u19.ts" {
		t.Errorf("BlastRadius() callers of foo = %v, want the 20 best-ranked files u00..u19", viaFoo)
	}
	for _, f := range []string{"src/norank.ts", "src/ambiguous.ts", "src/elsewhere.ts"} {
		for _, c := range got.Callers {
			if c.File == f {
				t.Errorf("BlastRadius() returned the caller %+v, want none from %s", c, f)
			}
		}
	}
	rest := got.Callers[20:]
	wantRest := []domain.BlastCaller{
		{File: "src/two.ts", Symbol: "late", ViaSymbol: "run", Line: 30, Rank: 1.2},
	}
	// the tie rows come first (rank 1.5), by path: t1 before t2, the lowest line of each
	wantRest = append([]domain.BlastCaller{
		{File: "src/t1.ts", Symbol: "tie", ViaSymbol: "Cls", Line: 2, Rank: 1.5},
		{File: "src/t2.ts", Symbol: "tie", ViaSymbol: "Cls", Line: 2, Rank: 1.5},
	}, wantRest...)
	if diff := cmp.Diff(wantRest, rest); diff != "" {
		t.Errorf("BlastRadius() callers after the cap mismatch (-want +got):\n%s", diff)
	}
	if _, ok := got.FactsByFile["src/u20.ts"]; ok {
		t.Error("BlastRadius() returned the facts of a caller dropped by the cap, want none")
	}
	if diff := cmp.Diff(domain.BlastFacts{Endpoints: []string{"GET /u00"}, Crons: []string{}}, got.FactsByFile["src/u00.ts"]); diff != "" {
		t.Errorf("BlastRadius() facts of u00 mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff(domain.BlastFacts{Endpoints: []string{}, Crons: []string{"0 * * * *", "job:index"}}, got.FactsByFile["src/two.ts"]); diff != "" {
		t.Errorf("BlastRadius() facts of two.ts mismatch (-want +got):\n%s", diff)
	}
	if len(got.FactsByFile) != 21 {
		t.Errorf("BlastRadius() returned facts of %d files, want 21 (20 kept u files and two.ts)", len(got.FactsByFile))
	}
}

// AC5040: a partial index and an index with stats.bounded keep their data, tagged degraded.
func TestBlastRadius_PartialAndBounded(t *testing.T) {
	e := newEnv(t, withFiles(map[string]string{"x.ts": "export const x = 1;\n"}))
	e.addSymbol("src/core.ts", "foo", "function", 1)
	e.addSymbol("src/a.ts", "useFoo", "function", 1)
	e.addRef("src/a.ts", "foo", 2, "src/core.ts")
	e.addRank("src/a.ts", 1)
	for _, tc := range []struct {
		name, status, stats string
		reason              domain.DegradedReason
		degraded            bool
	}{
		{name: "full", status: "full", stats: `{"bounded":0}`},
		{name: "partial", status: "partial", stats: `{}`, degraded: true, reason: domain.ReasonIndexPartial},
		{name: "bounded", status: "full", stats: `{"bounded":3}`, degraded: true, reason: domain.ReasonRepoTooLarge},
		{name: "partial and bounded", status: "partial", stats: `{"bounded":3}`, degraded: true, reason: domain.ReasonRepoTooLarge},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e.t = t
			e.setState(tc.status, tc.stats)
			got := e.blast(e.reader, "src/core.ts")
			if got.Degraded != tc.degraded || got.Reason != tc.reason || len(got.Callers) != 1 || len(got.ChangedSymbols) != 1 {
				t.Errorf("BlastRadius() = degraded %v reason %q with %d symbols and %d callers, want the index data tagged %v %q",
					got.Degraded, got.Reason, len(got.ChangedSymbols), len(got.Callers), tc.degraded, tc.reason)
			}
		})
	}
}

// AC5039: the clone scan with the switch off, with no index row and with a failed index.
func TestBlastRadius_CloneScan(t *testing.T) {
	e := newEnv(t, withFiles(map[string]string{
		"src/decl.ts":              "export function foo(): number {\n  return 1;\n}\n\nexport class Cls {\n  run(): void {\n  }\n}\n",
		"src/use.ts":               "import { foo } from './decl';\n\nexport function useFoo(): number {\n  return foo();\n}\n",
		"src/routes.ts":            "const app = makeApp();\napp.get('/x', () => foo());\n",
		"node_modules/lib/lib.js":  "function lib() {\n  return foo();\n}\n",
		"dist/out.js":              "function built() {\n  return foo();\n}\n",
		"src/deep/nested/inner.ts": "export function inner() {\n  const c = new Cls();\n  return foo() + c.run();\n}\n",
	}))
	secret := t.TempDir()
	writeClone(t, secret, map[string]string{"s.ts": "export function stolen(): number {\n  return foo();\n}\n"})
	if err := os.Symlink(filepath.Join(secret, "s.ts"), filepath.Join(e.dir, "src", "link.ts")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, filepath.Join(e.dir, "linked")); err != nil {
		t.Fatal(err)
	}

	wantSyms := []domain.BlastSymbol{
		{File: "src/decl.ts", Name: "foo", Kind: "function"}, {File: "src/decl.ts", Name: "Cls", Kind: "class"},
		{File: "src/decl.ts", Name: "Cls.run", Kind: "method"}, {File: "src/decl.ts", Name: "run", Kind: "method"},
	}
	wantCallers := []domain.BlastCaller{
		{File: "src/deep/nested/inner.ts", Symbol: "inner", ViaSymbol: "foo", Line: 3},
		{File: "src/routes.ts", Symbol: "routes.ts", ViaSymbol: "foo", Line: 2},
		{File: "src/use.ts", Symbol: "useFoo", ViaSymbol: "foo", Line: 4},
		{File: "src/deep/nested/inner.ts", Symbol: "inner", ViaSymbol: "Cls", Line: 2},
		{File: "src/deep/nested/inner.ts", Symbol: "inner", ViaSymbol: "Cls.run", Line: 3},
		{File: "src/deep/nested/inner.ts", Symbol: "inner", ViaSymbol: "run", Line: 3},
	}
	tests := []struct {
		name    string
		reader  func() *app.Reader
		setup   func()
		reason  domain.DegradedReason
		changed []string
	}{
		{name: "switch off", reader: func() *app.Reader { return e.blastReader(false) }, reason: domain.ReasonFlagOff},
		{name: "no index row", reader: func() *app.Reader { return e.blastReader(true) }, reason: domain.ReasonNoData},
		{name: "failed index", reader: func() *app.Reader { return e.blastReader(true) }, setup: func() { e.setState("failed", `{}`) }, reason: domain.ReasonIndexFailed},
		{name: "degraded index with its reason", reader: func() *app.Reader { return e.blastReader(true) },
			setup: func() { e.setState("degraded", `{"degradedReason":"repo_too_large"}`) }, reason: domain.ReasonRepoTooLarge},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e.t = t
			if tc.setup != nil {
				tc.setup()
			}
			got := e.blast(tc.reader(), "src/decl.ts")
			want := domain.BlastResult{ChangedSymbols: wantSyms, Callers: wantCallers, Degraded: true, Reason: tc.reason}
			if diff := cmp.Diff(want, got); diff != "" {
				t.Errorf("BlastRadius() mismatch (-want +got):\n%s", diff)
			}
		})
	}

	t.Run("no clone and no changed files", func(t *testing.T) {
		e.t = t
		bare := newEnv(t, withoutClone())
		empty := func(reason domain.DegradedReason) domain.BlastResult {
			return domain.BlastResult{ChangedSymbols: []domain.BlastSymbol{}, Callers: []domain.BlastCaller{}, Degraded: true, Reason: reason}
		}
		if diff := cmp.Diff(empty(domain.ReasonFlagOff), bare.blast(bare.blastReader(false), "src/decl.ts")); diff != "" {
			t.Errorf("BlastRadius(repo without a clone) mismatch (-want +got):\n%s", diff)
		}
		if diff := cmp.Diff(empty(domain.ReasonNoData), e.blast(e.blastReader(true))); diff != "" {
			t.Errorf("BlastRadius(no changed file) mismatch (-want +got):\n%s", diff)
		}
		if diff := cmp.Diff(empty(domain.ReasonFlagOff), e.blast(e.blastReader(false))); diff != "" {
			t.Errorf("BlastRadius(no changed file, switch off) mismatch (-want +got):\n%s", diff)
		}
	})
}

// AC5041, NFR5006, EC5017: a hostile clone. 120,000 files (the walk keeps 100,000), 50 files of
// 1,900,000 bytes of identifiers that call the declared names on every line, and a changed file
// declaring 3,000 names (the scan looks for the first 1,000). The call ends within the 31 s of the
// spec with at most 5,000 callers (or the empty degraded result at the time limit), reads no
// file of 2,000,000 bytes or more, and the heap stays far below 256 MiB.
func TestBlastRadius_HostileClone(t *testing.T) {
	if testing.Short() {
		t.Skip("hostile clone: builds 120,000 files (skipped with -short)")
	}
	const (
		names     = 3000
		smallFile = 120_000
		bigFiles  = 50
		bigBytes  = 1_900_000
	)
	var decl strings.Builder
	for i := range names {
		fmt.Fprintf(&decl, "export function name%04d(): number {\n  return %d;\n}\n", i, i)
	}
	e := newEnv(t, withFiles(map[string]string{"changed.ts": decl.String()}), withFlagOff())

	var call strings.Builder
	for i := range names {
		fmt.Fprintf(&call, "name%04d(); ", i)
	}
	call.WriteString("\n")
	bigLine := call.String()
	big := strings.Repeat(bigLine, bigBytes/len(bigLine))
	for i := range bigFiles {
		writeClone(t, e.dir, map[string]string{fmt.Sprintf("big%02d.ts", i): big})
	}
	// a file of exactly the limit is never read
	writeClone(t, e.dir, map[string]string{"huge.ts": strings.Repeat("x", domain.MaxCallerFileBytes)})
	for d := range 120 {
		dir := filepath.Join(e.dir, fmt.Sprintf("pkg%03d", d))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		for i := range smallFile / 120 {
			if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("f%04d.ts", i)), []byte("export const v = 1;\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}

	files := &callerLog{}
	r := app.NewReader(e.store, false, app.WithCallerScan(app.CallerScan{
		Repos: e.store, Files: files, Analyzer: treesitter.New(),
	}))

	var peak atomic.Uint64
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		var ms runtime.MemStats
		for {
			runtime.ReadMemStats(&ms)
			if ms.HeapInuse > peak.Load() {
				peak.Store(ms.HeapInuse)
			}
			select {
			case <-stop:
				return
			case <-time.After(20 * time.Millisecond):
			}
		}
	}()
	runtime.GC()
	start := time.Now()
	got, err := r.BlastRadius(t.Context(), e.repo, []string{"changed.ts"})
	elapsed := time.Since(start)
	close(stop)
	<-done
	if err != nil {
		t.Fatalf("BlastRadius() error = %v", err)
	}
	t.Logf("hostile clone: %v, %d changed symbols, %d callers, reason %s, peak heap %d MiB, %d files read",
		elapsed.Round(time.Millisecond), len(got.ChangedSymbols), len(got.Callers), got.Reason, peak.Load()>>20, len(files.paths))

	if elapsed > 31*time.Second {
		t.Errorf("BlastRadius() took %v, want at most 31 s", elapsed)
	}
	if !got.Degraded || got.Reason != domain.ReasonFlagOff {
		t.Errorf("BlastRadius() = degraded %v reason %q, want degraded flag_off", got.Degraded, got.Reason)
	}
	if len(got.Callers) > domain.MaxBlastScanCallers {
		t.Errorf("BlastRadius() returned %d callers, want at most %d", len(got.Callers), domain.MaxBlastScanCallers)
	}
	if len(got.ChangedSymbols) != 0 && len(got.ChangedSymbols) != domain.MaxBlastScanSymbols {
		t.Errorf("BlastRadius() returned %d changed symbols, want %d or the empty result", len(got.ChangedSymbols), domain.MaxBlastScanSymbols)
	}
	if len(got.ChangedSymbols) == 0 && len(got.Callers) != 0 {
		t.Errorf("BlastRadius() returned callers with no changed symbols: %d", len(got.Callers))
	}
	if len(files.paths) > domain.MaxCallerScanFiles+1 {
		t.Errorf("BlastRadius() read %d files, want at most %d", len(files.paths), domain.MaxCallerScanFiles+1)
	}
	for _, p := range files.paths {
		if p == "huge.ts" {
			t.Error("BlastRadius() read the file of 2,000,000 bytes, want files below the limit only")
		}
	}
	if mib := peak.Load() >> 20; mib > 256 {
		t.Errorf("BlastRadius() peak heap %d MiB, want below 256 MiB", mib)
	}
}

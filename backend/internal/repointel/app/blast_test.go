package app_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/repointel/app"
	"dev-digest-go/backend/internal/repointel/domain"
)

// blastIndexed is a repo with a full index: src/core.ts declares foo, Cls and Cls.run; src/a.ts
// and src/b.ts call them. file_rank rows are implied by the caller rows (the fake does not join).
func blastIndexed() *fakeStore {
	s := newFakeStore()
	s.repos[repoID] = domain.Repo{ID: repoID, ClonePath: "/clone"}
	s.states[repoID] = domain.IndexState{RepoID: repoID, Status: domain.StatusFull}
	s.data.symbols = []domain.SymbolRow{
		{Path: "src/core.ts", Name: "foo", Kind: "function", Line: 1},
		{Path: "src/core.ts", Name: "Cls", Kind: "class", Line: 5},
		{Path: "src/core.ts", Name: "Cls.run", Kind: "method", Line: 6},
		{Path: "src/core.ts", Name: "run", Kind: "method", Line: 6},
		{Path: "src/a.ts", Name: "useFoo", Kind: "function", Line: 2},
		{Path: "src/a.ts", Name: "useBoth", Kind: "function", Line: 9},
		{Path: "src/b.ts", Name: "other", Kind: "function", Line: 1},
	}
	s.callers = []domain.ResolvedCaller{
		{File: "src/a.ts", ToSymbol: "foo", Line: 3, Rank: 0.9},
		{File: "src/a.ts", ToSymbol: "foo", Line: 4, Rank: 0.9}, // same enclosing symbol: dropped
		{File: "src/a.ts", ToSymbol: "Cls", Line: 10, Rank: 0.9},
		{File: "src/b.ts", ToSymbol: "foo", Line: 2, Rank: 0.4},
		{File: "src/nosyms.ts", ToSymbol: "run", Line: 7, Rank: 0.2},
	}
	s.factRows = []domain.FileFacts{
		{Path: "src/a.ts", Endpoints: []string{"GET /x"}, Crons: []string{}},
		{Path: "src/b.ts", Endpoints: []string{}, Crons: []string{"0 * * * *"}},
		{Path: "src/unrelated.ts", Endpoints: []string{"GET /never"}, Crons: []string{}},
	}
	return s
}

func TestBlastRadius_PersistentPath(t *testing.T) {
	r := app.NewReader(blastIndexed(), true)
	got, err := r.BlastRadius(t.Context(), repoID, []string{"src/core.ts"})
	if err != nil {
		t.Fatalf("BlastRadius() error = %v", err)
	}
	want := domain.BlastResult{
		ChangedSymbols: []domain.BlastSymbol{
			{File: "src/core.ts", Name: "foo", Kind: "function"}, {File: "src/core.ts", Name: "Cls", Kind: "class"},
			{File: "src/core.ts", Name: "run", Kind: "method"},
		},
		Callers: []domain.BlastCaller{
			{File: "src/a.ts", Symbol: "useFoo", ViaSymbol: "foo", Line: 3, Rank: 0.9},
			{File: "src/a.ts", Symbol: "useBoth", ViaSymbol: "Cls", Line: 10, Rank: 0.9},
			{File: "src/b.ts", Symbol: "other", ViaSymbol: "foo", Line: 2, Rank: 0.4},
			{File: "src/nosyms.ts", Symbol: "nosyms.ts", ViaSymbol: "run", Line: 7, Rank: 0.2},
		},
		FactsByFile: map[string]domain.BlastFacts{
			"src/a.ts": {Endpoints: []string{"GET /x"}, Crons: []string{}},
			"src/b.ts": {Endpoints: []string{}, Crons: []string{"0 * * * *"}},
		},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("BlastRadius() mismatch (-want +got):\n%s", diff)
	}
}

func TestBlastRadius_PersistentFlags(t *testing.T) {
	tests := []struct {
		name         string
		st           domain.IndexState
		wantDegraded bool
		wantReason   domain.DegradedReason
	}{
		{name: "full", st: domain.IndexState{Status: domain.StatusFull}},
		{name: "partial", st: domain.IndexState{Status: domain.StatusPartial}, wantDegraded: true, wantReason: domain.ReasonIndexPartial},
		{name: "bounded", st: domain.IndexState{Status: domain.StatusFull, Stats: map[string]any{"bounded": float64(3)}}, wantDegraded: true, wantReason: domain.ReasonRepoTooLarge},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := blastIndexed()
			tc.st.RepoID = repoID
			s.states[repoID] = tc.st
			got, err := app.NewReader(s, true).BlastRadius(t.Context(), repoID, []string{"src/core.ts"})
			if err != nil {
				t.Fatalf("BlastRadius() error = %v", err)
			}
			if got.Degraded != tc.wantDegraded || got.Reason != tc.wantReason || len(got.Callers) != 4 || got.FactsByFile == nil {
				t.Errorf("BlastRadius() = degraded %v reason %q with %d callers, want index data tagged %v %q", got.Degraded, got.Reason, len(got.Callers), tc.wantDegraded, tc.wantReason)
			}
		})
	}
}

func TestBlastRadius_NoDeclaredNames(t *testing.T) {
	s := blastIndexed()
	got, err := app.NewReader(s, true).BlastRadius(t.Context(), repoID, []string{"src/other.ts"})
	if err != nil {
		t.Fatalf("BlastRadius() error = %v", err)
	}
	want := domain.BlastResult{ChangedSymbols: []domain.BlastSymbol{}, Callers: []domain.BlastCaller{}, FactsByFile: map[string]domain.BlastFacts{}}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("BlastRadius(file without symbols) mismatch (-want +got):\n%s", diff)
	}
}

func TestBlastRadius_PersistentErrors(t *testing.T) {
	boom := errors.New("boom")
	for _, method := range []string{"SymbolsInFiles", "ResolvedCallers", "FileFacts"} {
		t.Run(method, func(t *testing.T) {
			s := blastIndexed()
			s.hooks[method] = func(context.Context) error { return boom }
			_, err := app.NewReader(s, true).BlastRadius(t.Context(), repoID, []string{"src/core.ts"})
			if !errors.Is(err, boom) {
				t.Errorf("BlastRadius() with %s failing error = %v, want %v", method, err, boom)
			}
		})
	}
	t.Run("cancelled context", func(t *testing.T) {
		s := blastIndexed()
		s.hooks["GetState"] = func(ctx context.Context) error { return context.Canceled }
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		_, err := app.NewReader(s, true).BlastRadius(ctx, repoID, []string{"src/core.ts"})
		if !errors.Is(err, context.Canceled) {
			t.Errorf("BlastRadius() with a cancelled context error = %v, want context.Canceled", err)
		}
	})
}

// scanEnv is a repo whose clone holds the files, read with the REPO_INTEL_ENABLED switch off, so
// that every call is a clone scan (reason flag_off); no index is stored.
func scanEnv(files map[string]string, opts ...func(*app.CallerScan)) (*app.Reader, *fakeStore, *callerFiles) {
	s := newFakeStore()
	s.repos[repoID] = domain.Repo{ID: repoID, ClonePath: "/clone"}
	cf := &callerFiles{files: files}
	cs := app.CallerScan{Repos: s, Files: cf, Analyzer: &fakeAnalyzer{}}
	for _, o := range opts {
		o(&cs)
	}
	return app.NewReader(s, false, app.WithCallerScan(cs)), s, cf
}

const (
	declTS = "export function foo() {}\nexport class Cls {\n  run() {\n  }\n}\n"
	useTS  = "import { foo } from './decl';\n\nexport function useFoo() {\n  return foo();\n}\n\nexport function useCls() {\n  return new Cls();\n}\n// foo()\n"
)

func TestBlastRadius_ScanFallbackReasons(t *testing.T) {
	tests := []struct {
		name    string
		enabled bool
		setup   func(s *fakeStore)
		want    domain.DegradedReason
	}{
		{name: "flag off", want: domain.ReasonFlagOff},
		{name: "no state row", enabled: true, want: domain.ReasonNoData},
		{name: "failed", enabled: true, setup: func(s *fakeStore) { s.states[repoID] = domain.IndexState{Status: domain.StatusFailed} }, want: domain.ReasonIndexFailed},
		{name: "degraded without a reason", enabled: true, setup: func(s *fakeStore) { s.states[repoID] = domain.IndexState{Status: domain.StatusDegraded} }, want: domain.ReasonIndexFailed},
		{name: "degraded with its reason", enabled: true, setup: func(s *fakeStore) {
			s.states[repoID] = domain.IndexState{Status: domain.StatusDegraded, Stats: map[string]any{"degradedReason": "repo_too_large"}}
		}, want: domain.ReasonRepoTooLarge},
		{name: "state unreadable", enabled: true, setup: func(s *fakeStore) {
			s.hooks["GetState"] = func(context.Context) error { return errors.New("db down") }
		}, want: domain.ReasonNoData},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := newFakeStore()
			s.repos[repoID] = domain.Repo{ID: repoID, ClonePath: "/clone"}
			if tc.setup != nil {
				tc.setup(s)
			}
			cs := app.CallerScan{Repos: s, Files: &callerFiles{files: map[string]string{"src/decl.ts": declTS, "src/use.ts": useTS}}, Analyzer: &fakeAnalyzer{}}
			got, err := app.NewReader(s, tc.enabled, app.WithCallerScan(cs)).BlastRadius(t.Context(), repoID, []string{"src/decl.ts"})
			if err != nil {
				t.Fatalf("BlastRadius() error = %v", err)
			}
			if !got.Degraded || got.Reason != tc.want || got.FactsByFile != nil || len(got.Callers) == 0 {
				t.Errorf("BlastRadius() = degraded %v reason %q facts %v with %d callers, want the clone-scan answer tagged %q without facts",
					got.Degraded, got.Reason, got.FactsByFile, len(got.Callers), tc.want)
			}
		})
	}
}

func TestBlastRadius_Scan(t *testing.T) {
	r, _, _ := scanEnv(map[string]string{
		"src/decl.ts":  declTS,
		"src/use.ts":   useTS,
		"src/other.ts": "foo();\nCls.run();\nfoo();\n",
		"src/more.ts":  "export function more() {\n  foo(); Cls();\n}\n",
	})
	got, err := r.BlastRadius(t.Context(), repoID, []string{"src/decl.ts", "src/missing.ts"})
	if err != nil {
		t.Fatalf("BlastRadius() error = %v", err)
	}
	want := domain.BlastResult{
		// walk then extraction order; Cls.run and run are both changed symbols in the scan path
		ChangedSymbols: []domain.BlastSymbol{
			{File: "src/decl.ts", Name: "foo", Kind: "function"}, {File: "src/decl.ts", Name: "Cls", Kind: "class"},
			{File: "src/decl.ts", Name: "Cls.run", Kind: "method"}, {File: "src/decl.ts", Name: "run", Kind: "method"},
		},
		Callers: []domain.BlastCaller{
			// foo: files in order, one row per (file, enclosing symbol); the comment line and the
			// import line of use.ts are no references
			{File: "src/more.ts", Symbol: "more", ViaSymbol: "foo", Line: 2},
			{File: "src/other.ts", Symbol: "other.ts", ViaSymbol: "foo", Line: 1},
			{File: "src/use.ts", Symbol: "useFoo", ViaSymbol: "foo", Line: 4},
			// Cls: the call in more.ts and "new Cls" in use.ts
			{File: "src/more.ts", Symbol: "more", ViaSymbol: "Cls", Line: 2},
			{File: "src/use.ts", Symbol: "useCls", ViaSymbol: "Cls", Line: 8},
			// Cls.run searches for the member run: other.ts calls Cls.run(); then run, whose row
			// has another changed-symbol name and so is a row of its own
			{File: "src/other.ts", Symbol: "other.ts", ViaSymbol: "Cls.run", Line: 2},
			{File: "src/other.ts", Symbol: "other.ts", ViaSymbol: "run", Line: 2},
		},
		Degraded: true, Reason: domain.ReasonFlagOff,
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("BlastRadius() mismatch (-want +got):\n%s", diff)
	}
}

// R-I1 in the blast scan: a Go func New( line and a Java public Order findById( declaration in
// another file are no callers of the changed symbols of the same names, while the call on the
// next line is (the row's line is the call's); a file that only declares them has no row. A
// TypeScript caller file keeps Node's rules.
func TestBlastRadius_ScanSkipsGoAndJavaDeclarationLines(t *testing.T) {
	r, _, _ := scanEnv(map[string]string{
		"src/decl.ts":         "export function New() {}\nexport function findById() {}\n",
		"pkg/server.go":       "func New(cfg Config) *Server {\n\treturn New(cfg)\n}\n",
		"pkg/only.go":         "func New() *Server { return nil }\n",
		"src/OrderStore.java": "public Order findById(long id) {\n  return findById(id, false);\n}\n",
		"src/OnlyDecl.java":   "  public Order findById(long id);\n",
		"src/same.ts":         "func New(cfg Config) *Server {\n",
	})
	got, err := r.BlastRadius(t.Context(), repoID, []string{"src/decl.ts"})
	if err != nil {
		t.Fatalf("BlastRadius() error = %v", err)
	}
	want := []domain.BlastCaller{
		{File: "pkg/server.go", Symbol: "server.go", ViaSymbol: "New", Line: 2},
		{File: "src/same.ts", Symbol: "same.ts", ViaSymbol: "New", Line: 1},
		{File: "src/OrderStore.java", Symbol: "OrderStore.java", ViaSymbol: "findById", Line: 2},
	}
	if diff := cmp.Diff(want, got.Callers); diff != "" {
		t.Errorf("BlastRadius(decl.ts) callers mismatch (-want +got):\n%s", diff)
	}
}

// Two changed symbols with one name: the first in order keeps the rows both would find, the
// second adds only the rows of the first one's own file (Node's symbol-major loop).
func TestBlastRadius_ScanSameNameInTwoChangedFiles(t *testing.T) {
	r, _, _ := scanEnv(map[string]string{
		"src/a.ts": "export function dup() {}\nfunction ua() {\n  dup();\n}\n",
		"src/b.ts": "export function dup() {}\nfunction ub() {\n  dup();\n}\n",
		"src/c.ts": "function uc() {\n  dup();\n}\n",
	})
	got, err := r.BlastRadius(t.Context(), repoID, []string{"src/a.ts", "src/b.ts"})
	if err != nil {
		t.Fatalf("BlastRadius() error = %v", err)
	}
	want := []domain.BlastCaller{
		// dup of a.ts: b.ts and c.ts call it
		{File: "src/b.ts", Symbol: "ub", ViaSymbol: "dup", Line: 3},
		{File: "src/c.ts", Symbol: "uc", ViaSymbol: "dup", Line: 2},
		// dup of b.ts: a.ts is the only row not found already
		{File: "src/a.ts", Symbol: "ua", ViaSymbol: "dup", Line: 3},
	}
	if diff := cmp.Diff(want, got.Callers); diff != "" {
		t.Errorf("BlastRadius() callers mismatch (-want +got):\n%s", diff)
	}
	if len(got.ChangedSymbols) != 4 { // dup and the user function of each file
		t.Errorf("BlastRadius() changed symbols = %v, want dup, ua, dup, ub", got.ChangedSymbols)
	}
}

func TestBlastRadius_ScanEmptyAnswers(t *testing.T) {
	empty := func(reason domain.DegradedReason) domain.BlastResult {
		return domain.BlastResult{ChangedSymbols: []domain.BlastSymbol{}, Callers: []domain.BlastCaller{}, Degraded: true, Reason: reason}
	}
	files := map[string]string{"src/decl.ts": declTS, "src/use.ts": useTS}
	tests := []struct {
		name    string
		changed []string
		setup   func(s *fakeStore, cf *callerFiles)
		want    domain.BlastResult
	}{
		{name: "no changed file", setup: func(*fakeStore, *callerFiles) {}, want: empty(domain.ReasonNoData)},
		{name: "unknown repo", changed: []string{"src/decl.ts"}, setup: func(s *fakeStore, _ *callerFiles) { delete(s.repos, repoID) }, want: empty(domain.ReasonNoData)},
		{name: "no clone path", changed: []string{"src/decl.ts"}, setup: func(s *fakeStore, _ *callerFiles) { s.repos[repoID] = domain.Repo{ID: repoID} }, want: empty(domain.ReasonNoData)},
		{name: "clone missing", changed: []string{"src/decl.ts"}, setup: func(_ *fakeStore, cf *callerFiles) {
			cf.scanErr = fmt.Errorf("scan clone: %w", domain.ErrCloneMissing)
		}, want: empty(domain.ReasonNoData)},
		{name: "changed file not in the walk", changed: []string{"src/gone.ts", "README.md"}, setup: func(*fakeStore, *callerFiles) {}, want: empty(domain.ReasonNoData)},
		{name: "changed file without declarations", changed: []string{"src/use.ts2"}, setup: func(_ *fakeStore, cf *callerFiles) { cf.files["src/use.ts2"] = "const x = 1;\n" }, want: empty(domain.ReasonNoData)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := newFakeStore()
			s.repos[repoID] = domain.Repo{ID: repoID, ClonePath: "/clone"}
			cf := &callerFiles{files: cloneFiles(files)}
			tc.setup(s, cf)
			r := app.NewReader(s, true, app.WithCallerScan(app.CallerScan{Repos: s, Files: cf, Analyzer: &fakeAnalyzer{}}))
			got, err := r.BlastRadius(t.Context(), repoID, tc.changed)
			if err != nil {
				t.Fatalf("BlastRadius() error = %v", err)
			}
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("BlastRadius() mismatch (-want +got):\n%s", diff)
			}
		})
	}

	t.Run("reader without the caller scan", func(t *testing.T) {
		s := newFakeStore()
		got, err := app.NewReader(s, false).BlastRadius(t.Context(), repoID, []string{"src/decl.ts"})
		if err != nil {
			t.Fatalf("BlastRadius() error = %v", err)
		}
		if diff := cmp.Diff(empty(domain.ReasonFlagOff), got); diff != "" {
			t.Errorf("BlastRadius() mismatch (-want +got):\n%s", diff)
		}
	})
	t.Run("repo read fails", func(t *testing.T) {
		boom := errors.New("boom")
		s := newFakeStore()
		s.hooks["GetRepo"] = func(context.Context) error { return boom }
		r := app.NewReader(s, true, app.WithCallerScan(app.CallerScan{Repos: s, Files: &callerFiles{files: files}, Analyzer: &fakeAnalyzer{}}))
		if _, err := r.BlastRadius(t.Context(), repoID, []string{"src/decl.ts"}); !errors.Is(err, boom) {
			t.Errorf("BlastRadius() error = %v, want %v", err, boom)
		}
	})
}

func cloneFiles(m map[string]string) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// The scan ends at its own time limit with the empty result, and at the end of the caller's
// context with an error.
func TestBlastRadius_ScanTimeAndCancel(t *testing.T) {
	r, _, cf := scanEnv(map[string]string{"src/decl.ts": declTS}, func(cs *app.CallerScan) { cs.Timeout = 50 * time.Millisecond })
	cf.block = true
	start := time.Now()
	got, err := r.BlastRadius(t.Context(), repoID, []string{"src/decl.ts"})
	if err != nil {
		t.Fatalf("BlastRadius() at the scan limit error = %v, want the empty result", err)
	}
	want := domain.BlastResult{ChangedSymbols: []domain.BlastSymbol{}, Callers: []domain.BlastCaller{}, Degraded: true, Reason: domain.ReasonFlagOff}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("BlastRadius() at the scan limit mismatch (-want +got):\n%s", diff)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Errorf("BlastRadius() with a 50 ms limit took %v", d)
	}

	r, _, cf = scanEnv(map[string]string{"src/decl.ts": declTS})
	cf.block = true
	ctx, cancel := context.WithCancel(t.Context())
	time.AfterFunc(50*time.Millisecond, cancel)
	if _, err := r.BlastRadius(ctx, repoID, []string{"src/decl.ts"}); !errors.Is(err, context.Canceled) {
		t.Errorf("BlastRadius() cancelled error = %v, want context.Canceled", err)
	}
}

// The bounds of the scan: 1,000 changed symbols, 5,000 caller rows (the first rows in symbol-major
// order), and a symbol whose rows fill the bound drops the later symbols.
func TestBlastRadius_ScanBounds(t *testing.T) {
	t.Run("changed symbols", func(t *testing.T) {
		var decl strings.Builder
		for i := range 1500 {
			fmt.Fprintf(&decl, "function s%04d() {}\n", i)
		}
		r, _, _ := scanEnv(map[string]string{"src/decl.ts": decl.String()})
		got, err := r.BlastRadius(t.Context(), repoID, []string{"src/decl.ts"})
		if err != nil {
			t.Fatalf("BlastRadius() error = %v", err)
		}
		if len(got.ChangedSymbols) != domain.MaxBlastScanSymbols || got.ChangedSymbols[999].Name != "s0999" {
			t.Errorf("BlastRadius() kept %d changed symbols (last %v), want the first %d", len(got.ChangedSymbols), got.ChangedSymbols[len(got.ChangedSymbols)-1], domain.MaxBlastScanSymbols)
		}
	})
	t.Run("caller rows", func(t *testing.T) {
		files := map[string]string{"src/decl.ts": "function one() {}\nfunction two() {}\n"}
		// one is called from 10 files, two from 6,000 files, each in a function of its own
		for i := range 6000 {
			body := "two();"
			if i < 10 {
				body = "one(); two();"
			}
			files[fmt.Sprintf("src/c%04d.ts", i)] = fmt.Sprintf("function f%04d() { %s }\n", i, body)
		}
		r, _, _ := scanEnv(files)
		got, err := r.BlastRadius(t.Context(), repoID, []string{"src/decl.ts"})
		if err != nil {
			t.Fatalf("BlastRadius() error = %v", err)
		}
		if len(got.Callers) != domain.MaxBlastScanCallers {
			t.Fatalf("BlastRadius() returned %d callers, want %d", len(got.Callers), domain.MaxBlastScanCallers)
		}
		for i, c := range got.Callers {
			wantVia, wantFile := "one", fmt.Sprintf("src/c%04d.ts", i)
			if i >= 10 {
				wantVia, wantFile = "two", fmt.Sprintf("src/c%04d.ts", i-10)
			}
			if c.ViaSymbol != wantVia || c.File != wantFile {
				t.Fatalf("Callers[%d] = %s via %s, want %s via %s (first rows of the symbol-major order)", i, c.File, c.ViaSymbol, wantFile, wantVia)
			}
		}
	})
	t.Run("a first symbol filling the bound drops the rest", func(t *testing.T) {
		files := map[string]string{"src/decl.ts": "function one() {}\nfunction two() {}\n"}
		for i := range 5200 {
			files[fmt.Sprintf("src/c%04d.ts", i)] = fmt.Sprintf("function f%04d() { one(); two(); }\n", i)
		}
		r, _, _ := scanEnv(files)
		got, err := r.BlastRadius(t.Context(), repoID, []string{"src/decl.ts"})
		if err != nil {
			t.Fatalf("BlastRadius() error = %v", err)
		}
		if len(got.Callers) != domain.MaxBlastScanCallers {
			t.Fatalf("BlastRadius() returned %d callers, want %d", len(got.Callers), domain.MaxBlastScanCallers)
		}
		for _, c := range got.Callers {
			if c.ViaSymbol != "one" {
				t.Fatalf("a caller of %q was returned, want only the rows of the first symbol", c.ViaSymbol)
			}
		}
	})
}

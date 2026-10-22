package app_test

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/repointel/app"
	"dev-digest-go/backend/internal/repointel/domain"
)

// callerFiles is an in-memory CallerFiles: ScanCallers lists the paths in byte order.
type callerFiles struct {
	files map[string]string
	// scanErr is returned by ScanCallers; block makes it wait for ctx instead.
	scanErr error
	block   bool
	// reads records the files ReadCallerFile served.
	reads []string
}

func (c *callerFiles) ScanCallers(ctx context.Context, root string) ([]string, error) {
	if c.block {
		<-ctx.Done()
		return nil, fmt.Errorf("scan: %w", ctx.Err())
	}
	if c.scanErr != nil {
		return nil, c.scanErr
	}
	var out []string
	for p := range c.files {
		out = append(out, p)
	}
	slices.Sort(out)
	return out, nil
}

func (c *callerFiles) ReadCallerFile(root, rel string) ([]byte, error) {
	c.reads = append(c.reads, rel)
	return c.read(rel)
}

func (c *callerFiles) ReadFile(root, rel string) ([]byte, error) { return c.read(rel) }

func (c *callerFiles) read(rel string) ([]byte, error) {
	s, ok := c.files[rel]
	if !ok {
		return nil, fmt.Errorf("read %s: %w", rel, fs.ErrNotExist)
	}
	return []byte(s), nil
}

func sym(name, kind string, line int, sig string) domain.Symbol {
	return domain.Symbol{Name: name, Kind: kind, Line: line, EndLine: line, Signature: sig}
}

// callerEnv is a repo with a changed file src/a.ts declaring foo, Bar (and its method baz) and
// an interface, and callers whose symbols the fake analyzer knows.
func callerEnv(t *testing.T, enabled bool, opts ...func(*app.CallerScan)) (*app.Reader, *callerFiles, *fakeStore) {
	t.Helper()
	store := newFakeStore()
	store.repos[repoID] = domain.Repo{ID: repoID, ClonePath: "/clone"}
	files := &callerFiles{files: map[string]string{
		"src/a.ts": "function foo() {}\nclass Bar { baz() {} }\ninterface Shape {}\n",
		"src/b.ts": "function useFoo(x: number) {\n  return foo(x);\n}\nfunction other() {\n  return foo(2) + foo(3);\n}\n",
		"src/c.ts": "export const make = () =>\n  new Bar();\nconst top = foo();\n",
		"src/d.ts": "// foo()\nimport { foo } from './a';\nfunction foo() {}\n",
		"src/e.ts": "foo();\n",
	}}
	an := &fakeAnalyzer{ex: map[string]domain.Extraction{
		"src/a.ts": {Symbols: []domain.Symbol{
			sym("foo", "function", 1, "function foo()"), sym("Bar", "class", 2, "class Bar"),
			sym("Bar.baz", "method", 2, "baz()"), sym("baz", "method", 2, "baz()"), sym("Shape", "interface", 3, "interface Shape"),
		}},
		"src/b.ts": {Symbols: []domain.Symbol{sym("useFoo", "function", 1, "function useFoo(x: number)"), sym("other", "function", 4, "function other()")}},
		"src/c.ts": {Symbols: []domain.Symbol{sym("make", "function", 1, "const make = () =>"), sym("top", "function", 3, "")}},
		"src/e.ts": {},
	}}
	cs := app.CallerScan{Repos: store, Files: files, Analyzer: an}
	for _, o := range opts {
		o(&cs)
	}
	return app.NewReader(store, enabled, app.WithCallerScan(cs)), files, store
}

func TestCallerSignatures_RowsRulesAndOrder(t *testing.T) {
	r, _, _ := callerEnv(t, true)
	got, err := r.CallerSignatures(t.Context(), repoID, []string{"src/a.ts"}, 10)
	if err != nil {
		t.Fatalf("CallerSignatures() error = %v", err)
	}
	// foo: b.ts useFoo (lines 2) and other (line 5, once for two calls), c.ts line 3 sits in top
	// (no signature, skipped), d.ts declares foo (comment, import and declaration lines), e.ts has
	// no symbol; Bar: c.ts make. baz has no caller. Interfaces are not callable.
	want := []domain.CallerSignature{
		{File: "src/b.ts", Symbol: "useFoo", Signature: "function useFoo(x: number)"},
		{File: "src/b.ts", Symbol: "other", Signature: "function other()"},
		{File: "src/c.ts", Symbol: "make", Signature: "const make = () =>"},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("CallerSignatures() mismatch (-want +got):\n%s", diff)
	}
}

func TestCallerSignatures_LimitStopsTheScan(t *testing.T) {
	r, files, _ := callerEnv(t, true)
	got, err := r.CallerSignatures(t.Context(), repoID, []string{"src/a.ts"}, 1)
	if err != nil {
		t.Fatalf("CallerSignatures() error = %v", err)
	}
	if diff := cmp.Diff([]domain.CallerSignature{{File: "src/b.ts", Symbol: "useFoo", Signature: "function useFoo(x: number)"}}, got); diff != "" {
		t.Errorf("CallerSignatures(limit 1) mismatch (-want +got):\n%s", diff)
	}
	if slices.Contains(files.reads, "src/e.ts") {
		t.Errorf("CallerSignatures(limit 1) read %v, want the scan to stop once the first name holds the limit", files.reads)
	}
}

func TestCallerSignatures_DefaultLimitIs20(t *testing.T) {
	store := newFakeStore()
	store.repos[repoID] = domain.Repo{ID: repoID, ClonePath: "/clone"}
	files := &callerFiles{files: map[string]string{"src/a.ts": "function foo() {}\n"}}
	an := &fakeAnalyzer{ex: map[string]domain.Extraction{
		"src/a.ts": {Symbols: []domain.Symbol{sym("foo", "function", 1, "function foo()")}},
	}}
	var b strings.Builder
	var syms []domain.Symbol
	for i := range 30 {
		fmt.Fprintf(&b, "function f%d() {\n  foo();\n}\n", i)
		syms = append(syms, sym(fmt.Sprintf("f%d", i), "function", 3*i+1, fmt.Sprintf("function f%d()", i)))
	}
	files.files["src/many.ts"] = b.String()
	an.ex["src/many.ts"] = domain.Extraction{Symbols: syms}
	r := app.NewReader(store, true, app.WithCallerScan(app.CallerScan{Repos: store, Files: files, Analyzer: an}))

	got, err := r.CallerSignatures(t.Context(), repoID, []string{"src/a.ts"}, 0)
	if err != nil || len(got) != domain.DefaultCallerLimit {
		t.Errorf("CallerSignatures(limit 0) = %d rows, %v, want %d rows", len(got), err, domain.DefaultCallerLimit)
	}
}

func TestCallerSignatures_RankOrdersTheRows(t *testing.T) {
	r, _, store := callerEnv(t, true)
	store.data.ranks = []domain.FileRankRow{{Path: "src/c.ts", Percentile: 90}, {Path: "src/b.ts", Percentile: 40}}
	got, err := r.CallerSignatures(t.Context(), repoID, []string{"src/a.ts"}, 10)
	if err != nil {
		t.Fatalf("CallerSignatures() error = %v", err)
	}
	want := []domain.CallerSignature{
		{File: "src/c.ts", Symbol: "make", Signature: "const make = () =>", Rank: 90},
		{File: "src/b.ts", Symbol: "useFoo", Signature: "function useFoo(x: number)", Rank: 40},
		{File: "src/b.ts", Symbol: "other", Signature: "function other()", Rank: 40},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("CallerSignatures() with ranks mismatch (-want +got):\n%s", diff)
	}

	store.data.ranks = []domain.FileRankRow{{Path: "src/c.ts", Percentile: 90}, {Path: "src/zzz.ts", Percentile: 99}}
	got, err = r.CallerSignatures(t.Context(), repoID, []string{"src/a.ts"}, 10)
	if err != nil || len(got) != 3 || got[0].File != "src/c.ts" || got[1].Rank != 0 {
		t.Errorf("CallerSignatures() with a partial rank = %+v, %v, want c.ts first and the others at rank 0", got, err)
	}
}

func TestCallerSignatures_EmptyAnswers(t *testing.T) {
	tests := []struct {
		name    string
		enabled bool
		changed []string
		setup   func(s *fakeStore, f *callerFiles)
	}{
		{name: "flag off", changed: []string{"src/a.ts"}},
		{name: "no changed file", enabled: true},
		{name: "unknown repo", enabled: true, changed: []string{"src/a.ts"}, setup: func(s *fakeStore, _ *callerFiles) { delete(s.repos, repoID) }},
		{name: "no clone path", enabled: true, changed: []string{"src/a.ts"}, setup: func(s *fakeStore, _ *callerFiles) {
			s.repos[repoID] = domain.Repo{ID: repoID}
		}},
		{name: "clone missing", enabled: true, changed: []string{"src/a.ts"}, setup: func(_ *fakeStore, f *callerFiles) {
			f.scanErr = fmt.Errorf("scan: %w", domain.ErrCloneMissing)
		}},
		{name: "changed file absent from the clone", enabled: true, changed: []string{"src/new.ts"}},
		{name: "changed file without a parser", enabled: true, changed: []string{"README.md"}},
		{name: "changed file declares nothing callable", enabled: true, changed: []string{"src/e.ts"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r, files, store := callerEnv(t, tc.enabled)
			if tc.setup != nil {
				tc.setup(store, files)
			}
			got, err := r.CallerSignatures(t.Context(), repoID, tc.changed, 10)
			if err != nil {
				t.Fatalf("CallerSignatures() error = %v", err)
			}
			if got == nil || len(got) != 0 {
				t.Errorf("CallerSignatures() = %#v, want an empty non-nil slice", got)
			}
		})
	}
}

func TestCallerSignatures_NotConfigured(t *testing.T) {
	r := app.NewReader(readerStore(), true)
	if _, err := r.CallerSignatures(t.Context(), repoID, []string{"src/a.ts"}, 10); err == nil {
		t.Error("CallerSignatures() without WithCallerScan error = nil, want an error")
	}
	// The flag and the empty input are answered before the configuration is looked at.
	if got, err := r.CallerSignatures(t.Context(), repoID, nil, 10); err != nil || len(got) != 0 {
		t.Errorf("CallerSignatures(no files) = %v, %v, want empty", got, err)
	}
}

func TestCallerSignatures_FailuresOfTheStoreAreReturned(t *testing.T) {
	r, _, store := callerEnv(t, true)
	boom := errors.New("db down")
	store.hooks["GetRepo"] = func(context.Context) error { return boom }
	if _, err := r.CallerSignatures(t.Context(), repoID, []string{"src/a.ts"}, 10); !errors.Is(err, boom) {
		t.Errorf("CallerSignatures() with GetRepo failing error = %v, want %v", err, boom)
	}
	delete(store.hooks, "GetRepo")
	store.hooks["FileRanks"] = func(context.Context) error { return boom }
	if _, err := r.CallerSignatures(t.Context(), repoID, []string{"src/a.ts"}, 10); !errors.Is(err, boom) {
		t.Errorf("CallerSignatures() with FileRanks failing error = %v, want %v", err, boom)
	}
}

func TestCallerSignatures_ParserPanicSkipsTheFile(t *testing.T) {
	store := newFakeStore()
	store.repos[repoID] = domain.Repo{ID: repoID, ClonePath: "/clone"}
	files := &callerFiles{files: map[string]string{
		"src/a.ts": "function foo() {}\n", "src/b.ts": "foo();\n", "src/c.ts": "function g() {\n foo();\n}\n",
	}}
	an := &fakeAnalyzer{
		ex: map[string]domain.Extraction{
			"src/a.ts": {Symbols: []domain.Symbol{sym("foo", "function", 1, "function foo()")}},
			"src/c.ts": {Symbols: []domain.Symbol{sym("g", "function", 1, "function g()")}},
		},
		panicOn: map[string]bool{"src/b.ts": true},
	}
	r := app.NewReader(store, true, app.WithCallerScan(app.CallerScan{Repos: store, Files: files, Analyzer: an}))
	got, err := r.CallerSignatures(t.Context(), repoID, []string{"src/a.ts"}, 10)
	if err != nil {
		t.Fatalf("CallerSignatures() error = %v", err)
	}
	if diff := cmp.Diff([]domain.CallerSignature{{File: "src/c.ts", Symbol: "g", Signature: "function g()"}}, got); diff != "" {
		t.Errorf("CallerSignatures() mismatch (-want +got):\n%s", diff)
	}
}

func TestCallerSignatures_FirstDeclaringFileWinsAndDuplicatesAreRead(t *testing.T) {
	store := newFakeStore()
	store.repos[repoID] = domain.Repo{ID: repoID, ClonePath: "/clone"}
	files := &callerFiles{files: map[string]string{
		"src/a.ts": "function foo() {}\n", "src/a2.ts": "function foo() {}\n",
		"src/u.ts": "function user() {\n foo();\n}\n",
	}}
	an := &fakeAnalyzer{ex: map[string]domain.Extraction{
		"src/a.ts":  {Symbols: []domain.Symbol{sym("foo", "function", 1, "function foo()")}},
		"src/a2.ts": {Symbols: []domain.Symbol{sym("foo", "function", 1, "function foo()")}},
		"src/u.ts":  {Symbols: []domain.Symbol{sym("user", "function", 1, "function user()")}},
	}}
	r := app.NewReader(store, true, app.WithCallerScan(app.CallerScan{Repos: store, Files: files, Analyzer: an}))
	got, err := r.CallerSignatures(t.Context(), repoID, []string{"src/a.ts", "src/a.ts", "src/a2.ts"}, 10)
	if err != nil {
		t.Fatalf("CallerSignatures() error = %v", err)
	}
	// foo is collected once, from a.ts; a2.ts declares it too but its declaration line is no
	// reference, so u.ts is the only caller.
	if diff := cmp.Diff([]domain.CallerSignature{{File: "src/u.ts", Symbol: "user", Signature: "function user()"}}, got); diff != "" {
		t.Errorf("CallerSignatures() mismatch (-want +got):\n%s", diff)
	}
}

func TestCallerSignatures_TieBreakIsTheFirstExtracted(t *testing.T) {
	store := newFakeStore()
	store.repos[repoID] = domain.Repo{ID: repoID, ClonePath: "/clone"}
	files := &callerFiles{files: map[string]string{"src/a.ts": "function foo() {}\n", "src/u.ts": "x\nfoo();\n"}}
	an := &fakeAnalyzer{ex: map[string]domain.Extraction{
		"src/a.ts": {Symbols: []domain.Symbol{sym("foo", "function", 1, "function foo()")}},
		"src/u.ts": {Symbols: []domain.Symbol{
			sym("Cls.m", "method", 2, "m()"), sym("first", "method", 2, "first()"), sym("second", "method", 2, "second()"), sym("early", "function", 1, "early()"),
		}},
	}}
	r := app.NewReader(store, true, app.WithCallerScan(app.CallerScan{Repos: store, Files: files, Analyzer: an}))
	got, err := r.CallerSignatures(t.Context(), repoID, []string{"src/a.ts"}, 10)
	if err != nil || len(got) != 1 || got[0].Symbol != "first" {
		t.Errorf("CallerSignatures() = %+v, %v, want the single row enclosed by first", got, err)
	}
}

func TestCallerSignatures_TimeoutAndCancel(t *testing.T) {
	t.Run("deadline", func(t *testing.T) {
		r, files, _ := callerEnv(t, true, func(cs *app.CallerScan) { cs.Timeout = 30 * time.Millisecond })
		files.block = true
		start := time.Now()
		_, err := r.CallerSignatures(t.Context(), repoID, []string{"src/a.ts"}, 10)
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("CallerSignatures() error = %v, want context.DeadlineExceeded", err)
		}
		if d := time.Since(start); d > 5*time.Second {
			t.Errorf("CallerSignatures() took %v, want to end at the timeout", d)
		}
	})
	t.Run("caller cancels", func(t *testing.T) {
		r, _, _ := callerEnv(t, true)
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		_, err := r.CallerSignatures(ctx, repoID, []string{"src/a.ts"}, 10)
		if !errors.Is(err, context.Canceled) {
			t.Errorf("CallerSignatures(canceled) error = %v, want context.Canceled", err)
		}
	})
	t.Run("canceled between files", func(t *testing.T) {
		_, files, _ := callerEnv(t, true)
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		// Cancel once the scan has begun reading callers.
		wrapped := &cancelOnRead{callerFiles: files, cancel: cancel}
		store := newFakeStore()
		store.repos[repoID] = domain.Repo{ID: repoID, ClonePath: "/clone"}
		an := &fakeAnalyzer{ex: map[string]domain.Extraction{"src/a.ts": {Symbols: []domain.Symbol{sym("foo", "function", 1, "function foo()")}}}}
		r := app.NewReader(store, true, app.WithCallerScan(app.CallerScan{Repos: store, Files: wrapped, Analyzer: an}))
		if _, err := r.CallerSignatures(ctx, repoID, []string{"src/a.ts"}, 10); !errors.Is(err, context.Canceled) {
			t.Errorf("CallerSignatures() canceled during the scan error = %v, want context.Canceled", err)
		}
	})
}

type cancelOnRead struct {
	*callerFiles
	cancel func()
}

func (c *cancelOnRead) ReadCallerFile(root, rel string) ([]byte, error) {
	c.cancel()
	return c.callerFiles.ReadCallerFile(root, rel)
}

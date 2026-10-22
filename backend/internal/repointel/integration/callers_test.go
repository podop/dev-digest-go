package integration_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/repointel/adapters/clonetree"
	"dev-digest-go/backend/internal/repointel/adapters/treesitter"
	"dev-digest-go/backend/internal/repointel/app"
	"dev-digest-go/backend/internal/repointel/domain"
)

// callerLog wraps the clone adapter and records every path the caller scan opens.
type callerLog struct {
	clonetree.FS
	mu    sync.Mutex
	paths []string
}

func (l *callerLog) ReadCallerFile(root, rel string) ([]byte, error) {
	l.record(rel)
	return l.FS.ReadCallerFile(root, rel)
}

func (l *callerLog) ReadFile(root, rel string) ([]byte, error) {
	l.record(rel)
	return l.FS.ReadFile(root, rel)
}

func (l *callerLog) record(rel string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.paths = append(l.paths, rel)
}

func (e *env) callerReader(timeout time.Duration) (*app.Reader, *callerLog) {
	e.t.Helper()
	files := &callerLog{}
	return app.NewReader(e.store, true, app.WithCallerScan(app.CallerScan{
		Repos: e.store, Files: files, Analyzer: treesitter.New(), Timeout: timeout,
	})), files
}

func writeClone(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for p, content := range files {
		full := filepath.Join(dir, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// AC4060: callers come from a scan of the clone with the real parser; node_modules and symlinks
// are not scanned; file_rank orders the rows when the repo is indexed.
func TestCallerSignatures_AC4060(t *testing.T) {
	e := newEnv(t, withFiles(map[string]string{
		"src/a.ts":            "export function foo(): number {\n  return 1;\n}\n\nexport class Bar {\n  baz(): void {}\n}\n",
		"src/b.ts":            "import { foo } from './a';\n\nexport function useFoo(x: number): number {\n  return foo() + x;\n}\n",
		"src/c.ts":            "import { Bar } from './a';\nimport { useFoo } from './b';\n\nexport const make = () =>\n  new Bar();\nexport function useBoth(): number {\n  return useFoo(1);\n}\n",
		"src/z.ts":            "export function zed(): number {\n  return 0;\n}\n",
		"src/y.ts":            "import { zed } from './z';\nexport function viaZ(): number {\n  return zed();\n}\n",
		"node_modules/x/x.js": "function lib() {\n  return foo();\n}\n",
		"dist/out.js":         "function built() {\n  return foo();\n}\n",
	}))
	secret := t.TempDir()
	writeClone(t, secret, map[string]string{"s.ts": "export function stolen(): number {\n  return foo();\n}\n"})
	if err := os.Symlink(filepath.Join(e.dir, "src", "b.ts"), filepath.Join(e.dir, "src", "l.ts")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, filepath.Join(e.dir, "linked")); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(e.dir, "src", "pipe.ts"), 0o644); err != nil {
		t.Fatal(err)
	}
	r, files := e.callerReader(0)
	ctx := t.Context()

	t.Run("not indexed: scan order", func(t *testing.T) {
		got, err := r.CallerSignatures(ctx, e.repo, []string{"src/a.ts"}, 10)
		if err != nil {
			t.Fatalf("CallerSignatures() error = %v", err)
		}
		want := []domain.CallerSignature{
			{File: "src/b.ts", Symbol: "useFoo", Signature: "function useFoo(x: number): number"},
			{File: "src/c.ts", Symbol: "make", Signature: "const make = ()"},
		}
		if diff := cmp.Diff(want, got); diff != "" {
			t.Errorf("CallerSignatures() mismatch (-want +got):\n%s", diff)
		}
		for _, p := range files.paths {
			if strings.HasPrefix(p, "node_modules/") || strings.HasPrefix(p, "dist/") || strings.HasPrefix(p, "linked/") || p == "src/l.ts" || p == "src/pipe.ts" {
				t.Errorf("the scan opened %q, want only regular files outside the excluded directories", p)
			}
		}
	})

	t.Run("indexed: rank order", func(t *testing.T) {
		e.mustFull()
		ranks, err := e.reader.FileRank(ctx, e.repo, []string{"src/b.ts", "src/c.ts"})
		if err != nil || len(ranks) != 2 {
			t.Fatalf("FileRank() = %v, %v, want two ranks", ranks, err)
		}
		got, err := r.CallerSignatures(ctx, e.repo, []string{"src/a.ts"}, 10)
		if err != nil {
			t.Fatalf("CallerSignatures() error = %v", err)
		}
		if len(got) != 2 {
			t.Fatalf("CallerSignatures() = %+v, want two rows", got)
		}
		byFile := map[string]int{}
		for _, p := range ranks {
			byFile[p.Path] = p.Percentile
		}
		for _, row := range got {
			if row.Rank != byFile[row.File] {
				t.Errorf("row %s has rank %d, want file_rank percentile %d", row.File, row.Rank, byFile[row.File])
			}
		}
		if !slices.IsSortedFunc(got, func(a, b domain.CallerSignature) int { return b.Rank - a.Rank }) {
			t.Errorf("CallerSignatures() = %+v, want rank descending", got)
		}
	})

	t.Run("empty answers", func(t *testing.T) {
		off := app.NewReader(e.store, false, app.WithCallerScan(app.CallerScan{Repos: e.store, Files: clonetree.New(), Analyzer: treesitter.New()}))
		for name, call := range map[string]func() ([]domain.CallerSignature, error){
			"flag off": func() ([]domain.CallerSignature, error) {
				return off.CallerSignatures(ctx, e.repo, []string{"src/a.ts"}, 10)
			},
			"empty input": func() ([]domain.CallerSignature, error) { return r.CallerSignatures(ctx, e.repo, nil, 10) },
			"unknown repo": func() ([]domain.CallerSignature, error) {
				return r.CallerSignatures(ctx, "00000000-0000-0000-0000-000000000000", []string{"src/a.ts"}, 10)
			},
		} {
			if got, err := call(); err != nil || len(got) != 0 {
				t.Errorf("%s: CallerSignatures() = %v, %v, want empty", name, got, err)
			}
		}
	})
}

func TestCallerSignatures_NoClone(t *testing.T) {
	e := newEnv(t, withoutClone())
	r, _ := e.callerReader(0)
	if got, err := r.CallerSignatures(t.Context(), e.repo, []string{"src/a.ts"}, 10); err != nil || len(got) != 0 {
		t.Errorf("CallerSignatures(repo without clone) = %v, %v, want empty", got, err)
	}
}

// A changed file that is new in the PR (absent from the clone) contributes no symbols (EC4036);
// a changed file over the Phase 3 size limit is skipped.
func TestCallerSignatures_ChangedFileRules(t *testing.T) {
	big := "export function huge(): number {\n  return 1;\n}\n" + strings.Repeat("// pad\n", domain.MaxFileBytes/7+10)
	e := newEnv(t, withFiles(map[string]string{
		"src/big.ts":  big,
		"src/user.ts": "export function usesHuge(): number {\n  return huge();\n}\n",
	}))
	r, _ := e.callerReader(0)
	got, err := r.CallerSignatures(t.Context(), e.repo, []string{"src/new-in-pr.ts", "src/big.ts", "notes.md"}, 10)
	if err != nil || len(got) != 0 {
		t.Errorf("CallerSignatures() = %v, %v, want empty: new, oversized and unsupported files declare nothing", got, err)
	}
}

// NFR4010 / AC4061: 5,000 files and 20 names without callers finish well inside the bound,
// nothing outside the clone is opened and a symlink to a file with callers is not followed.
func TestCallerSignatures_5000FilesNoCallers(t *testing.T) {
	var decl strings.Builder
	for i := range 20 {
		fmt.Fprintf(&decl, "export function target%d(): number {\n  return %d;\n}\n", i, i)
	}
	e := newEnv(t, withFiles(map[string]string{"src/changed.ts": decl.String()}))
	files := map[string]string{}
	for i := range 5000 {
		files[fmt.Sprintf("pkg%02d/f%04d.ts", i%50, i)] = fmt.Sprintf("export function own%d(): number {\n  return %d;\n}\n", i, i)
	}
	writeClone(t, e.dir, files)
	outside := t.TempDir()
	writeClone(t, outside, map[string]string{"caller.ts": "export function stolen(): number {\n  return target1();\n}\n"})
	if err := os.Symlink(filepath.Join(outside, "caller.ts"), filepath.Join(e.dir, "pkg00", "link.ts")); err != nil {
		t.Fatal(err)
	}
	r, log := e.callerReader(0)

	start := time.Now()
	got, err := r.CallerSignatures(t.Context(), e.repo, []string{"src/changed.ts"}, 20)
	elapsed := time.Since(start)
	if err != nil || len(got) != 0 {
		t.Fatalf("CallerSignatures() = %v, %v, want empty", got, err)
	}
	if elapsed > 10*time.Second {
		t.Errorf("CallerSignatures() took %v, want at most 10 s", elapsed)
	}
	if len(log.paths) < 5000 {
		t.Errorf("the scan read %d files, want all 5,001", len(log.paths))
	}
	for _, p := range log.paths {
		if strings.Contains(p, "link.ts") || strings.HasPrefix(p, "/") || strings.Contains(p, "..") {
			t.Errorf("the scan opened %q, want only regular files inside the clone", p)
		}
	}
}

// A hostile clone: huge hit count in one file and a deadline. The call returns an error at the
// bound instead of running on.
func TestCallerSignatures_DeadlineEndsTheCall(t *testing.T) {
	e := newEnv(t, withFiles(map[string]string{"src/changed.ts": "export function target(): number {\n  return 1;\n}\n"}))
	files := map[string]string{}
	for i := range 3000 {
		files[fmt.Sprintf("pkg%02d/f%04d.ts", i%30, i)] = strings.Repeat("// padding line\n", 2000)
	}
	writeClone(t, e.dir, files)
	r, _ := e.callerReader(time.Nanosecond)
	_, err := r.CallerSignatures(t.Context(), e.repo, []string{"src/changed.ts"}, 10)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("CallerSignatures() with a 1 ns bound error = %v, want context.DeadlineExceeded", err)
	}
}

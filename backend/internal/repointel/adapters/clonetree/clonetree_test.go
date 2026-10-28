package clonetree_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/platform/clonefs"
	"dev-digest-go/backend/internal/repointel/adapters/clonetree"
	"dev-digest-go/backend/internal/repointel/domain"
	"dev-digest-go/backend/internal/repointel/domain/importgraph"
)

func write(t *testing.T, root, rel string, size int) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, bytes.Repeat([]byte("x"), size), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeText(t *testing.T, root, rel, content string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func symlink(t *testing.T, target, root, rel string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, p); err != nil {
		t.Fatal(err)
	}
}

// outside creates a directory next to the clone with a secret file the clone must never reach.
func outside(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	writeText(t, dir, "secret.ts", "export const secret = 1;\n")
	writeText(t, dir, "secret.txt", "top secret")
	return dir
}

func TestWalk_AC3024(t *testing.T) {
	root := t.TempDir()
	out := outside(t)
	write(t, root, "node_modules/a.ts", 5)
	write(t, root, "dist/b.js", 5)
	write(t, root, "src/.next/c.ts", 5)
	write(t, root, "src/Big.TS", domain.MaxFileBytes+1)
	write(t, root, "src/ok.TSX", 5)
	write(t, root, "src/x.d.ts", 5)
	write(t, root, "src/notes.md", 5)
	symlink(t, filepath.Join(out, "secret.ts"), root, "src/link.ts")
	symlink(t, out, root, "src/linkdir")
	symlink(t, filepath.Join(root, "src"), root, "alias") // inside the clone, still not followed

	got, err := clonetree.New().Walk(t.Context(), root)
	if err != nil {
		t.Fatalf("Walk() error = %v", err)
	}
	want := domain.WalkResult{
		Files: []string{"src/ok.TSX", "src/x.d.ts"},
		Stats: domain.WalkStats{TotalCandidates: 3, SkippedTooLarge: 1},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Walk() mismatch (-want +got):\n%s", diff)
	}
}

func TestWalk_SizeLimitIsInclusive(t *testing.T) {
	root := t.TempDir()
	write(t, root, "a.ts", domain.MaxFileBytes)
	write(t, root, "b.ts", domain.MaxFileBytes+1)
	got, err := clonetree.New().Walk(t.Context(), root)
	if err != nil {
		t.Fatalf("Walk() error = %v", err)
	}
	if diff := cmp.Diff([]string{"a.ts"}, got.Files); diff != "" {
		t.Errorf("Walk().Files mismatch (-want +got):\n%s", diff)
	}
	if got.Stats.SkippedTooLarge != 1 || got.Stats.TotalCandidates != 2 {
		t.Errorf("Walk().Stats = %+v, want TotalCandidates 2 and SkippedTooLarge 1", got.Stats)
	}
}

func TestWalk_ExcludedAtAnyDepthAndByteOrder(t *testing.T) {
	root := t.TempDir()
	for _, p := range []string{
		"z.ts", "B.ts", "a.ts", "pkg/vendor/x.ts", "pkg/a/build/y.js", "pkg/a/coverage/z.js", "out/o.ts",
		".git/hooks/h.js", "pkg/Out/keep.ts", "pkg/b/c/d/deep.mjs", ".hidden.cjs",
	} {
		write(t, root, p, 1)
	}
	got, err := clonetree.New().Walk(t.Context(), root)
	if err != nil {
		t.Fatalf("Walk() error = %v", err)
	}
	want := []string{".hidden.cjs", "B.ts", "a.ts", "pkg/Out/keep.ts", "pkg/b/c/d/deep.mjs", "z.ts"}
	if diff := cmp.Diff(want, got.Files); diff != "" {
		t.Errorf("Walk().Files mismatch (-want +got):\n%s", diff)
	}
}

func TestWalk_BoundIsAppliedAfterTheSort(t *testing.T) {
	root := t.TempDir()
	// Created in an order unrelated to byte order, with the early names in the deepest directory.
	for i := range domain.MaxIndexedFiles + 3 {
		write(t, root, fmt.Sprintf("d%d/f%04d.ts", i%3, i), 1)
	}
	got, err := clonetree.New().Walk(t.Context(), root)
	if err != nil {
		t.Fatalf("Walk() error = %v", err)
	}
	if len(got.Files) != domain.MaxIndexedFiles || got.Stats.Bounded != 3 || got.Stats.TotalCandidates != domain.MaxIndexedFiles+3 {
		t.Fatalf("Walk() = %d files, stats %+v; want %d files, Bounded 3", len(got.Files), got.Stats, domain.MaxIndexedFiles)
	}
	if got.Files[0] != "d0/f0000.ts" || got.Files[len(got.Files)-1] != "d2/f4991.ts" {
		t.Errorf("Walk().Files ends %q ... %q, want the first 5,000 of the byte order", got.Files[0], got.Files[len(got.Files)-1])
	}
}

func TestWalk_UnreadableDirectoryIsSkipped(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads every directory")
	}
	root := t.TempDir()
	write(t, root, "ok.ts", 1)
	write(t, root, "locked/x.ts", 1)
	if err := os.Chmod(filepath.Join(root, "locked"), 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(filepath.Join(root, "locked"), 0o755) })
	got, err := clonetree.New().Walk(t.Context(), root)
	if err != nil {
		t.Fatalf("Walk() error = %v", err)
	}
	if diff := cmp.Diff([]string{"ok.ts"}, got.Files); diff != "" {
		t.Errorf("Walk().Files mismatch (-want +got):\n%s", diff)
	}
}

func TestWalk_FifoAndEmpty(t *testing.T) {
	root := t.TempDir()
	if err := syscall.Mkfifo(filepath.Join(root, "pipe.ts"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := clonetree.New().Walk(t.Context(), root)
	if err != nil {
		t.Fatalf("Walk() error = %v", err)
	}
	if len(got.Files) != 0 || got.Stats != (domain.WalkStats{}) {
		t.Errorf("Walk(fifo only) = %+v, want nothing", got)
	}
}

func TestWalk_RootErrors(t *testing.T) {
	dir := t.TempDir()
	writeText(t, dir, "file.ts", "x")
	tests := []struct {
		name string
		root string
	}{
		{"missing", filepath.Join(dir, "nope")},
		{"a file", filepath.Join(dir, "file.ts")},
		{"empty path", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := clonetree.New().Walk(t.Context(), tc.root)
			if !errors.Is(err, domain.ErrCloneMissing) {
				t.Errorf("Walk(%q) error = %v, want ErrCloneMissing", tc.root, err)
			}
		})
	}
}

func TestWalk_RootMayBeALink(t *testing.T) {
	// The clone directory is operator configured and may be a link; only what is below it may not.
	real := t.TempDir()
	write(t, real, "a.ts", 1)
	link := filepath.Join(t.TempDir(), "clone")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	got, err := clonetree.New().Walk(t.Context(), link)
	if err != nil {
		t.Fatalf("Walk() error = %v", err)
	}
	if diff := cmp.Diff([]string{"a.ts"}, got.Files); diff != "" {
		t.Errorf("Walk().Files mismatch (-want +got):\n%s", diff)
	}
}

func TestWalk_Canceled(t *testing.T) {
	root := t.TempDir()
	write(t, root, "a.ts", 1)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := clonetree.New().Walk(ctx, root)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("Walk(canceled) error = %v, want context.Canceled", err)
	}
	if errors.Is(err, domain.ErrCloneMissing) {
		t.Errorf("Walk(canceled) error = %v, must not read as a missing clone", err)
	}
}

func TestReadFile(t *testing.T) {
	root := t.TempDir()
	out := outside(t)
	writeText(t, root, "src/a.ts", "export const a = 1;\n")
	writeText(t, root, "tsconfig.json", `{"compilerOptions":{}}`)
	write(t, root, "src/big.ts", clonetree.MaxReadBytes+1)
	write(t, root, "src/edge.ts", clonetree.MaxReadBytes)
	symlink(t, filepath.Join(out, "secret.ts"), root, "src/link.ts")
	symlink(t, "/etc/passwd", root, "src/p.ts")
	symlink(t, out, root, "linkdir")
	symlink(t, filepath.Join(root, "src"), root, "inner")
	if err := os.Mkdir(filepath.Join(root, "adir"), 0o755); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name    string
		rel     string
		want    string // content; "" with wantErr
		wantErr error
	}{
		{"source", "src/a.ts", "export const a = 1;\n", nil},
		{"tsconfig is not a source file", "tsconfig.json", `{"compilerOptions":{}}`, nil},
		{"exactly the cap", "src/edge.ts", strings.Repeat("x", clonetree.MaxReadBytes), nil},
		{"over the cap", "src/big.ts", "", clonefs.ErrTooLarge},
		{"missing", "src/none.ts", "", clonefs.ErrNotFound},
		{"symlink to outside file", "src/link.ts", "", clonefs.ErrNotFound},
		{"symlink to /etc/passwd", "src/p.ts", "", clonefs.ErrNotFound},
		{"behind an outside directory link", "linkdir/secret.ts", "", clonefs.ErrNotFound},
		{"behind an inside directory link", "inner/a.ts", "", clonefs.ErrNotFound},
		{"directory", "adir", "", clonefs.ErrNotFound},
		{"parent escape", "../" + filepath.Base(out) + "/secret.ts", "", clonefs.ErrNotFound},
		{"dot dot inside", "src/../src/a.ts", "", clonefs.ErrNotFound},
		{"absolute", filepath.Join(out, "secret.ts"), "", clonefs.ErrNotFound},
		{"backslash", `src\a.ts`, "", clonefs.ErrNotFound},
		{"nul", "src/a.ts\x00.png", "", clonefs.ErrNotFound},
		{"empty", "", "", clonefs.ErrNotFound},
		{"dot", ".", "", clonefs.ErrNotFound},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := clonetree.New().ReadFile(root, tc.rel)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("ReadFile(%q) error = %v, want %v", tc.rel, err, tc.wantErr)
				}
				if got != nil {
					t.Errorf("ReadFile(%q) returned %d bytes with an error", tc.rel, len(got))
				}
				if strings.Contains(err.Error(), root) {
					t.Errorf("ReadFile(%q) error %q names the clone path", tc.rel, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("ReadFile(%q) error = %v", tc.rel, err)
			}
			if string(got) != tc.want {
				t.Errorf("ReadFile(%q) = %.40q, want %.40q", tc.rel, got, tc.want)
			}
		})
	}
}

func TestReadFile_MissingRoot(t *testing.T) {
	_, err := clonetree.New().ReadFile(filepath.Join(t.TempDir(), "nope"), "a.ts")
	if !errors.Is(err, clonefs.ErrNotFound) {
		t.Errorf("ReadFile(missing root) error = %v, want ErrNotFound", err)
	}
}

// TestReadFile_NotFoundSatisfiesFSErrNotExist pins the domain.SourceFS contract the import graph
// relies on: a file that cannot be reached is both clonefs.ErrNotFound and fs.ErrNotExist.
func TestReadFile_NotFoundSatisfiesFSErrNotExist(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.ts"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{"missing.json", "../outside.json", "a.ts/x"} {
		_, err := clonetree.New().ReadFile(root, rel)
		if !errors.Is(err, clonefs.ErrNotFound) || !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("ReadFile(%q) error = %v, want both clonefs.ErrNotFound and fs.ErrNotExist", rel, err)
		}
	}
	if _, err := clonetree.New().ReadFile(root, "a.ts"); err != nil {
		t.Errorf("ReadFile(existing) error = %v", err)
	}
}

// TestImportGraph_NoTSConfigRaisesNoAliasError runs the import graph over a real clone directory
// without a tsconfig: no alias warning, relative edges built.
func TestImportGraph_NoTSConfigRaisesNoAliasError(t *testing.T) {
	root := t.TempDir()
	for name, body := range map[string]string{"a.ts": "", "b.ts": ""} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	imports := func(_ context.Context, file string) domain.FileImports {
		if file == "a.ts" {
			return domain.FileImports{Specs: []string{"./b"}}
		}
		return domain.FileImports{}
	}
	g, aliasErr, err := importgraph.Build(t.Context(), clonetree.New(), root, []string{"a.ts", "b.ts"}, imports)
	if err != nil || aliasErr != nil {
		t.Fatalf("Build() = %v, %v, want no errors for a clone without tsconfig.json", aliasErr, err)
	}
	if diff := cmp.Diff([]domain.Edge{{From: "a.ts", To: "b.ts"}}, g.Edges); diff != "" {
		t.Errorf("Build() edges mismatch (-want +got):\n%s", diff)
	}
}

// TestGoResolver_DeepChainOverARealClone is S-M1 over the real file system: one .go file at the
// bottom of a 1,000-deep directory chain made NewGoResolver probe go.mod in every ancestor, each
// probe O(depth) (depth 500: 46 s, 1,000: over 9 min). Only the first 64 levels are probed now.
func TestGoResolver_DeepChainOverARealClone(t *testing.T) {
	root := t.TempDir()
	rel := strings.Repeat("a/", 1000) + "x.go"
	writeText(t, root, rel, "package a\n")
	writeText(t, root, "go.mod", "module m\n")
	fs := clonetree.New()
	walk, err := fs.Walk(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff([]string{rel}, walk.Files); diff != "" {
		t.Fatalf("Walk() files mismatch (-want +got):\n%s", diff)
	}
	start := time.Now()
	_, err = importgraph.NewGoResolver(t.Context(), importgraph.NewTree(fs, root, walk.Files), walk.Files)
	took := time.Since(start)
	if err != nil {
		t.Fatalf("NewGoResolver() error = %v", err)
	}
	t.Logf("NewGoResolver(1,000-deep file) took %v", took)
	if took > time.Second {
		t.Errorf("NewGoResolver(1,000-deep file) took %v, want under 1s", took)
	}
}

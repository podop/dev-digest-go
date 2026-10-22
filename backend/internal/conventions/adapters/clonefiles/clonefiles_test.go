package clonefiles_test

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/conventions/adapters/clonefiles"
	"dev-digest-go/backend/internal/conventions/domain"
)

func write(t *testing.T, root, rel, content string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// fixture mirrors the AC265 clone.
func fixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, rel := range []string{
		"package.json", "tsconfig.base.json", "server/AGENTS.md", "a/b/c/tsconfig.json", ".github/x.ts",
		"node_modules/y.js", "src/a.ts", "src/a.test.ts", "src/b.min.js", "lib/c.go", "vite.config.ts",
	} {
		write(t, root, rel, "content of "+rel+"\n")
	}
	write(t, root, "src/big.ts", strings.Repeat("x", 600*1024))
	if err := os.Symlink(filepath.Join(root, "src/a.ts"), filepath.Join(root, "src/l.ts")); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestList_SkipsSymlinksHiddenAndVendoredDirectories(t *testing.T) {
	got, err := clonefiles.New().List(t.Context(), fixture(t))
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	slices.Sort(got)
	want := []string{
		"a/b/c/tsconfig.json", "lib/c.go", "package.json", "server/AGENTS.md", "src/a.test.ts", "src/a.ts",
		"src/b.min.js", "src/big.ts", "tsconfig.base.json", "vite.config.ts",
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("List() mismatch (-want +got):\n%s", diff)
	}
}

func TestList_PicksTheSampleOfAC265(t *testing.T) {
	root := fixture(t)
	f := clonefiles.New()
	paths, err := f.List(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff([]string{"package.json", "tsconfig.base.json", "server/AGENTS.md"}, domain.PickConfigFiles(paths)); diff != "" {
		t.Errorf("config files mismatch (-want +got):\n%s", diff)
	}
	var readable []string
	for _, p := range domain.PickFallbackSources(paths, domain.SampleTopN) {
		if _, ok := f.Read(root, p); ok {
			readable = append(readable, p)
		}
	}
	if diff := cmp.Diff([]string{"lib/c.go", "src/a.ts"}, readable); diff != "" {
		t.Errorf("readable source files mismatch (-want +got):\n%s", diff)
	}
}

func TestList_MissingCloneIsErrCloneMissing(t *testing.T) {
	_, err := clonefiles.New().List(t.Context(), filepath.Join(t.TempDir(), "gone"))
	if !errors.Is(err, domain.ErrCloneMissing) {
		t.Errorf("List(missing dir) error = %v, want domain.ErrCloneMissing", err)
	}
}

func TestList_StopsAtWalkLimits(t *testing.T) {
	root := t.TempDir()
	deep := strings.Repeat("d/", domain.WalkMaxDepth+1)
	write(t, root, deep+"too-deep.ts", "x")
	write(t, root, strings.Repeat("d/", domain.WalkMaxDepth)+"deepest.ts", "x")
	got, err := clonefiles.New().List(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff([]string{strings.Repeat("d/", domain.WalkMaxDepth) + "deepest.ts"}, got); diff != "" {
		t.Errorf("List() mismatch (-want +got):\n%s", diff)
	}
}

func TestRead(t *testing.T) {
	root := fixture(t)
	outside := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(outside, []byte("top secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "src/out.ts")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Dir(outside), filepath.Join(root, "linkdir")); err != nil {
		t.Fatal(err)
	}
	write(t, root, "bin.ts", "a\x00b")
	write(t, root, "bad.ts", "ok \xff end")
	tests := []struct {
		name   string
		rel    string
		want   string
		wantOK bool
	}{
		{"regular file", "src/a.ts", "content of src/a.ts\n", true},
		{"backslash path", `src\a.ts`, "content of src/a.ts\n", true},
		{"missing", "src/none.ts", "", false},
		{"symlinked file inside", "src/l.ts", "", false},
		{"symlinked file outside", "src/out.ts", "", false},
		{"file behind a symlinked directory", "linkdir/secret.txt", "", false},
		{"parent traversal", "../x", "", false},
		{"absolute path", "/etc/passwd", "", false},
		{"empty", "", "", false},
		{"directory", "src", "", false},
		{"over the size cap", "src/big.ts", "", false},
		{"NUL byte", "bin.ts", "", false},
		{"invalid UTF-8 is replaced", "bad.ts", "ok � end", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := clonefiles.New().Read(root, tc.rel)
			if ok != tc.wantOK || got != tc.want {
				t.Errorf("Read(%q) = %q, %v, want %q, %v", tc.rel, got, ok, tc.want, tc.wantOK)
			}
		})
	}
}

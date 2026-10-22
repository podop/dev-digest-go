package clonefs_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/platform/clonefs"
)

// write creates a file (and its directories) under dir.
func write(t *testing.T, dir, rel, content string) {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func symlink(t *testing.T, target, dir, rel string) {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, p); err != nil {
		t.Fatal(err)
	}
}

// fixture builds a clone with ordinary files and every kind of trap, next to an outside
// directory holding a secret the clone must never expose.
func fixture(t *testing.T) (clone, outside string) {
	t.Helper()
	base := t.TempDir()
	clone, outside = filepath.Join(base, "clone"), filepath.Join(base, "outside")
	write(t, outside, "secret.md", "TOP SECRET")
	write(t, clone, "README.md", "hello")
	write(t, clone, "docs/guide.md", "guide")
	write(t, clone, "docs/deep/er/x.md", "x")
	write(t, clone, "big.md", strings.Repeat("a", 100))
	symlink(t, filepath.Join(outside, "secret.md"), clone, "docs/link.md")
	symlink(t, outside, clone, "docs/out")
	symlink(t, filepath.Join(clone, "docs"), clone, "alias") // inside the clone, still not followed
	if err := syscall.Mkfifo(filepath.Join(clone, "pipe.md"), 0o644); err != nil {
		t.Fatal(err)
	}
	return clone, outside
}

func TestRead(t *testing.T) {
	clone, _ := fixture(t)
	tests := []struct {
		name    string
		rel     string
		max     int64
		want    string
		wantErr error
	}{
		{name: "regular file", rel: "README.md", max: 1024, want: "hello"},
		{name: "nested file", rel: "docs/guide.md", max: 1024, want: "guide"},
		{name: "exactly at the cap", rel: "README.md", max: 5, want: "hello"},
		{name: "over the cap", rel: "big.md", max: 99, wantErr: clonefs.ErrTooLarge},
		{name: "symlinked file to outside", rel: "docs/link.md", max: 1024, wantErr: clonefs.ErrNotFound},
		{name: "through symlinked directory to outside", rel: "docs/out/secret.md", max: 1024, wantErr: clonefs.ErrNotFound},
		{name: "through symlinked directory inside the clone", rel: "alias/guide.md", max: 1024, wantErr: clonefs.ErrNotFound},
		{name: "fifo", rel: "pipe.md", max: 1024, wantErr: clonefs.ErrNotFound},
		{name: "directory", rel: "docs", max: 1024, wantErr: clonefs.ErrNotFound},
		{name: "missing", rel: "nope.md", max: 1024, wantErr: clonefs.ErrNotFound},
		{name: "dot dot", rel: "../outside/secret.md", max: 1024, wantErr: clonefs.ErrNotFound},
		{name: "dot dot inside", rel: "docs/../README.md", max: 1024, wantErr: clonefs.ErrNotFound},
		{name: "absolute", rel: "/etc/passwd", max: 1024, wantErr: clonefs.ErrNotFound},
		{name: "backslash", rel: `docs\guide.md`, max: 1024, wantErr: clonefs.ErrNotFound},
		{name: "NUL", rel: "README.md\x00.txt", max: 1024, wantErr: clonefs.ErrNotFound},
		{name: "empty", rel: "", max: 1024, wantErr: clonefs.ErrNotFound},
		{name: "dot", rel: ".", max: 1024, wantErr: clonefs.ErrNotFound},
		{name: "empty segment", rel: "docs//guide.md", max: 1024, wantErr: clonefs.ErrNotFound},
		{name: "trailing slash", rel: "README.md/", max: 1024, wantErr: clonefs.ErrNotFound},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := clonefs.Read(clone, tc.rel, tc.max)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("Read(%q) error = %v, want %v", tc.rel, err, tc.wantErr)
			}
			if tc.wantErr == nil {
				if string(got.Data) != tc.want || got.Size != int64(len(tc.want)) {
					t.Errorf("Read(%q) = %q (size %d), want %q", tc.rel, got.Data, got.Size, tc.want)
				}
				if got.ModTime.IsZero() {
					t.Errorf("Read(%q) ModTime is zero", tc.rel)
				}
			}
		})
	}
}

func TestRead_TooLargeReportsSize(t *testing.T) {
	clone, _ := fixture(t)
	got, err := clonefs.Read(clone, "big.md", 10)
	if !errors.Is(err, clonefs.ErrTooLarge) || got.Size != 100 || got.Data != nil {
		t.Errorf("Read(big.md, 10) = (size %d, data %v), %v; want size 100, no data, ErrTooLarge", got.Size, got.Data, err)
	}
}

func TestRead_MissingRoot(t *testing.T) {
	if _, err := clonefs.Read(filepath.Join(t.TempDir(), "gone"), "a.md", 10); !errors.Is(err, clonefs.ErrNotFound) {
		t.Errorf("Read(missing root) error = %v, want ErrNotFound", err)
	}
	file := filepath.Join(t.TempDir(), "f")
	if err := os.WriteFile(file, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := clonefs.Read(file, "a.md", 10); !errors.Is(err, clonefs.ErrNotFound) {
		t.Errorf("Read(root is a file) error = %v, want ErrNotFound", err)
	}
}

func TestRead_RootMayBeALink(t *testing.T) {
	clone, _ := fixture(t)
	link := filepath.Join(t.TempDir(), "root-link")
	if err := os.Symlink(clone, link); err != nil {
		t.Fatal(err)
	}
	got, err := clonefs.Read(link, "README.md", 10)
	if err != nil || string(got.Data) != "hello" {
		t.Errorf("Read(linked root) = %q, %v; want hello", got.Data, err)
	}
}

func TestStat(t *testing.T) {
	clone, _ := fixture(t)
	info, err := clonefs.Stat(clone, "big.md")
	if err != nil || info.Size != 100 || info.ModTime.IsZero() {
		t.Errorf("Stat(big.md) = %+v, %v; want size 100", info, err)
	}
	for _, rel := range []string{"docs/link.md", "docs/out/secret.md", "pipe.md", "docs", "../x"} {
		if _, err := clonefs.Stat(clone, rel); !errors.Is(err, clonefs.ErrNotFound) {
			t.Errorf("Stat(%q) error = %v, want ErrNotFound", rel, err)
		}
	}
}

func paths(entries []clonefs.Entry) []string {
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.Path)
	}
	return out
}

func TestWalk(t *testing.T) {
	clone, _ := fixture(t)
	md := func(rel string) bool { return strings.HasSuffix(rel, ".md") }
	tests := []struct {
		name          string
		opts          clonefs.WalkOptions
		want          []string
		wantTruncated bool
	}{
		{
			name: "breadth first, no symlinks, no fifo",
			opts: clonefs.WalkOptions{MaxDepth: -1},
			want: []string{"README.md", "big.md", "docs/guide.md", "docs/deep/er/x.md"},
		},
		{
			name: "root only",
			opts: clonefs.WalkOptions{MaxDepth: 0},
			want: []string{"README.md", "big.md"},
		},
		{
			name: "depth one",
			opts: clonefs.WalkOptions{MaxDepth: 1},
			want: []string{"README.md", "big.md", "docs/guide.md"},
		},
		{
			name: "skip a directory",
			opts: clonefs.WalkOptions{MaxDepth: -1, SkipDir: func(name, _ string) bool { return name == "deep" }},
			want: []string{"README.md", "big.md", "docs/guide.md"},
		},
		{
			name: "skip by relative path",
			opts: clonefs.WalkOptions{MaxDepth: -1, SkipDir: func(_, rel string) bool { return rel == "docs" }},
			want: []string{"README.md", "big.md"},
		},
		{
			name: "include filter",
			opts: clonefs.WalkOptions{MaxDepth: -1, Include: func(rel string) bool { return strings.HasPrefix(rel, "docs/") && md(rel) }},
			want: []string{"docs/guide.md", "docs/deep/er/x.md"},
		},
		{
			name:          "file cap",
			opts:          clonefs.WalkOptions{MaxDepth: -1, MaxFiles: 3},
			want:          []string{"README.md", "big.md", "docs/guide.md"},
			wantTruncated: true,
		},
		{
			name: "cap equal to the count is not truncated",
			opts: clonefs.WalkOptions{MaxDepth: -1, MaxFiles: 4},
			want: []string{"README.md", "big.md", "docs/guide.md", "docs/deep/er/x.md"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, truncated, err := clonefs.Walk(t.Context(), clone, tc.opts)
			if err != nil {
				t.Fatalf("Walk() error = %v", err)
			}
			if diff := cmp.Diff(tc.want, paths(got)); diff != "" {
				t.Errorf("Walk() paths mismatch (-want +got):\n%s", diff)
			}
			if truncated != tc.wantTruncated {
				t.Errorf("Walk() truncated = %v, want %v", truncated, tc.wantTruncated)
			}
		})
	}
}

// AC285: a directory symlink to /etc-like content is never entered or listed.
func TestWalk_NeverEntersSymlinkedDirectories(t *testing.T) {
	clone, _ := fixture(t)
	got, _, err := clonefs.Walk(t.Context(), clone, clonefs.WalkOptions{MaxDepth: -1})
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range got {
		if strings.HasPrefix(e.Path, "docs/out") || strings.HasPrefix(e.Path, "alias") || e.Path == "docs/link.md" {
			t.Errorf("Walk() listed %q, which is behind a symlink", e.Path)
		}
	}
}

func TestWalk_EntryFields(t *testing.T) {
	clone, _ := fixture(t)
	got, _, err := clonefs.Walk(t.Context(), clone, clonefs.WalkOptions{
		MaxDepth: -1,
		Include:  func(rel string) bool { return rel == "big.md" || rel == "docs/deep/er/x.md" },
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("Walk() = %d entries, want 2", len(got))
	}
	if got[0].Path != "big.md" || got[0].Depth != 0 || got[0].Size != 100 || got[0].ModTime.IsZero() {
		t.Errorf("entry big.md = %+v", got[0])
	}
	if got[1].Path != "docs/deep/er/x.md" || got[1].Depth != 3 || got[1].Size != 1 {
		t.Errorf("entry x.md = %+v", got[1])
	}
}

func TestWalk_UnreadableSubdirectoryIsSkipped(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	clone, _ := fixture(t)
	locked := filepath.Join(clone, "docs", "deep")
	if err := os.Chmod(locked, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })
	got, _, err := clonefs.Walk(t.Context(), clone, clonefs.WalkOptions{MaxDepth: -1})
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff([]string{"README.md", "big.md", "docs/guide.md"}, paths(got)); diff != "" {
		t.Errorf("Walk() paths mismatch (-want +got):\n%s", diff)
	}
}

func TestWalk_RootErrors(t *testing.T) {
	file := filepath.Join(t.TempDir(), "f")
	if err := os.WriteFile(file, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	for name, root := range map[string]string{"missing": filepath.Join(t.TempDir(), "gone"), "a file": file} {
		if _, _, err := clonefs.Walk(t.Context(), root, clonefs.WalkOptions{MaxDepth: -1}); !errors.Is(err, clonefs.ErrNotFound) {
			t.Errorf("Walk(%s) error = %v, want ErrNotFound", name, err)
		}
	}
}

func TestWalk_ContextCancelled(t *testing.T) {
	clone, _ := fixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, _, err := clonefs.Walk(ctx, clone, clonefs.WalkOptions{MaxDepth: -1}); !errors.Is(err, context.Canceled) {
		t.Errorf("Walk(cancelled) error = %v, want context.Canceled", err)
	}
}

func TestWalk_DoesNotWrite(t *testing.T) {
	clone, _ := fixture(t)
	before := snapshot(t, clone)
	if _, _, err := clonefs.Walk(t.Context(), clone, clonefs.WalkOptions{MaxDepth: -1}); err != nil {
		t.Fatal(err)
	}
	if _, err := clonefs.Read(clone, "README.md", 100); err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(before, snapshot(t, clone)); diff != "" {
		t.Errorf("clone changed (-before +after):\n%s", diff)
	}
}

func snapshot(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		out = append(out, p+"|"+info.Mode().String()+"|"+info.ModTime().String())
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

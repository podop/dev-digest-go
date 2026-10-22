package clonedocs_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/projectcontext/adapters/clonedocs"
	"dev-digest-go/backend/internal/projectcontext/app"
	"dev-digest-go/backend/internal/projectcontext/domain"
)

func globs(t *testing.T) *domain.GlobSet {
	t.Helper()
	g, err := domain.NewGlobSet(domain.DefaultGlobs)
	if err != nil {
		t.Fatal(err)
	}
	return g
}

func write(t *testing.T, root, rel, content string) {
	t.Helper()
	full := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func link(t *testing.T, target, root, rel string) {
	t.Helper()
	full := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, full); err != nil {
		t.Fatal(err)
	}
}

func paths(l app.ClonedList) []string {
	out := make([]string, len(l.Docs))
	for i, d := range l.Docs {
		out[i] = d.Path
	}
	return out
}

func TestList(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	write(t, outside, "secret.md", "secret")
	write(t, root, "docs/b.md", "bb")
	write(t, root, "docs/a.md", "# héllo😀")
	write(t, root, "a/specs/deep/c.md", "c")
	write(t, root, "docs/node_modules/x.md", "x")
	write(t, root, "vendor/docs/x.md", "x")
	write(t, root, "docs/readme.txt", "x")
	write(t, root, "Docs/upper.md", "x")
	write(t, root, "docs/big.md", strings.Repeat("z", 262_145))
	link(t, filepath.Join(outside, "secret.md"), root, "docs/file-link.md")
	link(t, outside, root, "docs/dir-link")
	link(t, ".", root, "docs/self")
	if err := syscall.Mkfifo(filepath.Join(root, "docs", "pipe.md"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, ok, err := clonedocs.Docs{}.List(t.Context(), root, globs(t), 500)
	if err != nil || !ok {
		t.Fatalf("List() = ok %v, err %v, want ok", ok, err)
	}
	want := []string{"a/specs/deep/c.md", "docs/a.md", "docs/b.md", "docs/big.md"}
	if diff := cmp.Diff(want, paths(got)); diff != "" {
		t.Errorf("List() paths mismatch (-want +got):\n%s", diff)
	}
	byPath := map[string]app.ClonedDoc{}
	for _, d := range got.Docs {
		byPath[d.Path] = d
	}
	if d := byPath["docs/a.md"]; d.Tokens != 3 || d.SizeBytes != int64(len("# héllo😀")) || d.UpdatedAt.IsZero() {
		t.Errorf("docs/a.md = %+v, want 3 tokens (8 UTF-16 units), byte size and a time", d)
	}
	if d := byPath["docs/big.md"]; d.Tokens != 65_537 || d.SizeBytes != 262_145 {
		t.Errorf("docs/big.md = %+v, want tokens ceil(size/4) = 65537 for a file that is not read", d)
	}
}

func TestList_CapAndOrder(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 12; i++ {
		write(t, root, fmt.Sprintf("docs/d%02d.md", 11-i), "x")
	}
	got, ok, err := clonedocs.Docs{}.List(t.Context(), root, globs(t), 10)
	if err != nil || !ok || !got.Truncated || len(got.Docs) != 10 {
		t.Fatalf("List(limit 10) = %d docs truncated %v ok %v err %v, want 10 and truncated", len(got.Docs), got.Truncated, ok, err)
	}
	if first, last := got.Docs[0].Path, got.Docs[9].Path; first != "docs/d00.md" || last != "docs/d09.md" {
		t.Errorf("first and last = %q, %q, want the lowest 10 by path", first, last)
	}
}

func TestList_NoDirectory(t *testing.T) {
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	for name, root := range map[string]string{"missing": filepath.Join(t.TempDir(), "nope"), "a file": file} {
		if _, ok, err := (clonedocs.Docs{}).List(t.Context(), root, globs(t), 500); ok || err != nil {
			t.Errorf("List(%s) = ok %v, err %v, want not ok and no error", name, ok, err)
		}
	}
}

func TestList_CanceledContext(t *testing.T) {
	root := t.TempDir()
	write(t, root, "docs/a.md", "x")
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, _, err := (clonedocs.Docs{}).List(ctx, root, globs(t), 500); err == nil {
		t.Error("List() with a canceled context = nil error, want one")
	}
}

func TestRead_Confinement(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	write(t, outside, "passwd.md", "root")
	write(t, root, "docs/ok.md", "fine")
	write(t, root, "docs/big.md", strings.Repeat("z", 262_145))
	link(t, filepath.Join(outside, "passwd.md"), root, "docs/file-link.md")
	link(t, outside, root, "docs/dir-link")
	link(t, "../ok.md", root, "docs/sub/inside-link.md") // a link that stays inside is refused too
	link(t, outside, root, "docs/ext")

	tests := []struct {
		name   string
		path   string
		status app.ClonedStatus
	}{
		{"regular file", "docs/ok.md", app.ClonedOK},
		{"too large", "docs/big.md", app.ClonedTooLarge},
		{"missing", "docs/none.md", app.ClonedNotFound},
		{"file symlink", "docs/file-link.md", app.ClonedNotFound},
		{"directory symlink", "docs/dir-link/passwd.md", app.ClonedNotFound},
		{"symlink pointing inside", "docs/sub/inside-link.md", app.ClonedNotFound},
		{"parent traversal", "docs/../../passwd.md", app.ClonedNotFound},
		{"absolute path", filepath.Join(outside, "passwd.md"), app.ClonedNotFound},
		{"backslash", `docs\ok.md`, app.ClonedNotFound},
		{"NUL", "docs/ok.md\x00.md", app.ClonedNotFound},
		{"directory", "docs", app.ClonedNotFound},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := clonedocs.Docs{}.Read(root, tc.path)
			if got.Status != tc.status {
				t.Errorf("Read(%q) status = %v, want %v", tc.path, got.Status, tc.status)
			}
			if tc.status != app.ClonedOK && got.Content != "" {
				t.Errorf("Read(%q) leaked content %q", tc.path, got.Content)
			}
		})
	}
	if got := (clonedocs.Docs{}).Read(root, "docs/ok.md"); got.Content != "fine" || got.SizeBytes != 4 || got.Tokens != 1 {
		t.Errorf("Read(docs/ok.md) = %+v, want content, size 4, 1 token", got)
	}
	if got := (clonedocs.Docs{}).Read(root, "docs/big.md"); got.SizeBytes != 262_145 {
		t.Errorf("Read(docs/big.md) size = %d, want 262145", got.SizeBytes)
	}
}

func TestRead_InvalidUTF8(t *testing.T) {
	root := t.TempDir()
	write(t, root, "docs/bad.md", "a\xe2\x82b")
	got := clonedocs.Docs{}.Read(root, "docs/bad.md")
	if got.Content != "a�b" || got.Tokens != 1 {
		t.Errorf("Read() = %q (%d tokens), want one U+FFFD for the truncated sequence", got.Content, got.Tokens)
	}
}

func TestExists(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	write(t, outside, "x.md", "x")
	write(t, root, ".devdigest/specs/a.md", "a")
	link(t, filepath.Join(outside, "x.md"), root, ".devdigest/specs/l.md")
	link(t, outside, root, ".devdigest/specs/dir")
	for path, want := range map[string]bool{
		".devdigest/specs/a.md":     true,
		".devdigest/specs/none.md":  false,
		".devdigest/specs/l.md":     false,
		".devdigest/specs/dir/x.md": false,
		"../x.md":                   false,
	} {
		if got := (clonedocs.Docs{}).Exists(root, path); got != want {
			t.Errorf("Exists(%q) = %v, want %v", path, got, want)
		}
	}
	if (clonedocs.Docs{}).Exists(filepath.Join(root, "missing"), "a.md") {
		t.Error("Exists() in a missing clone = true, want false")
	}
}

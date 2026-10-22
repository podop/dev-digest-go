package clonetree_test

import (
	"context"
	"errors"
	"io/fs"
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/repointel/adapters/clonetree"
	"dev-digest-go/backend/internal/repointel/domain"
)

func TestScanCallers_ListsByTheCallerRules(t *testing.T) {
	root := t.TempDir()
	out := outside(t)
	for _, p := range []string{
		"src/b.ts", "src/a.tsx", "src/c.js", "src/d.jsx", "src/e.mjs", "src/f.cjs", "Z.ts", "a/.hidden/h.ts",
		"out/o.ts", "vendor/v.ts", // excluded by the index walk, not by the caller scan
		"src/notes.md", "src/g.mts", "src/H.TS",
		"node_modules/n.ts", "dist/d.ts", "build/b.ts", "coverage/c.ts", "src/.next/x.ts", "src/.git/x.ts",
	} {
		write(t, root, p, 5)
	}
	write(t, root, "src/big.ts", domain.MaxCallerFileBytes-1)
	write(t, root, "src/toobig.ts", domain.MaxCallerFileBytes)
	write(t, root, "src/over-index-limit.ts", domain.MaxFileBytes+1) // below the scan limit: scanned
	symlink(t, filepath.Join(out, "secret.ts"), root, "src/link.ts")
	symlink(t, out, root, "src/linkdir")
	symlink(t, filepath.Join(root, "src"), root, "alias")

	got, err := clonetree.New().ScanCallers(t.Context(), root)
	if err != nil {
		t.Fatalf("ScanCallers() error = %v", err)
	}
	want := []string{
		"Z.ts", "a/.hidden/h.ts", "out/o.ts", "src/a.tsx", "src/b.ts", "src/big.ts", "src/c.js", "src/d.jsx",
		"src/e.mjs", "src/f.cjs", "src/over-index-limit.ts", "vendor/v.ts",
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("ScanCallers() mismatch (-want +got):\n%s", diff)
	}
}

func TestScanCallers_MissingRootAndCancel(t *testing.T) {
	_, err := clonetree.New().ScanCallers(t.Context(), filepath.Join(t.TempDir(), "absent"))
	if !errors.Is(err, domain.ErrCloneMissing) {
		t.Errorf("ScanCallers(missing root) error = %v, want ErrCloneMissing", err)
	}

	root := t.TempDir()
	write(t, root, "a.ts", 5)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := clonetree.New().ScanCallers(ctx, root); !errors.Is(err, context.Canceled) {
		t.Errorf("ScanCallers(canceled ctx) error = %v, want context.Canceled", err)
	}
}

func TestReadCallerFile(t *testing.T) {
	root := t.TempDir()
	out := outside(t)
	writeText(t, root, "src/a.ts", "foo();\n")
	write(t, root, "src/big.ts", domain.MaxCallerFileBytes-1)
	write(t, root, "src/toobig.ts", domain.MaxCallerFileBytes)
	symlink(t, filepath.Join(out, "secret.ts"), root, "src/link.ts")
	symlink(t, out, root, "linkdir")

	fsys := clonetree.New()
	if got, err := fsys.ReadCallerFile(root, "src/a.ts"); err != nil || string(got) != "foo();\n" {
		t.Errorf("ReadCallerFile(src/a.ts) = %q, %v, want the content", got, err)
	}
	if got, err := fsys.ReadCallerFile(root, "src/big.ts"); err != nil || len(got) != domain.MaxCallerFileBytes-1 {
		t.Errorf("ReadCallerFile(src/big.ts) = %d bytes, %v, want %d bytes", len(got), err, domain.MaxCallerFileBytes-1)
	}
	for _, rel := range []string{"src/toobig.ts", "src/link.ts", "linkdir/secret.ts", "../x.ts", "src/missing.ts"} {
		if got, err := fsys.ReadCallerFile(root, rel); err == nil {
			t.Errorf("ReadCallerFile(%q) = %d bytes, nil error, want an error", rel, len(got))
		}
	}
	if _, err := fsys.ReadCallerFile(root, "src/missing.ts"); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("ReadCallerFile(missing) error = %v, want fs.ErrNotExist", err)
	}
}

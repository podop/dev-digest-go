package domain

import (
	"fmt"
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestIsSupported(t *testing.T) {
	tests := []struct {
		path string
		want bool
	}{
		{"a.ts", true}, {"a.tsx", true}, {"a.js", true}, {"a.jsx", true}, {"a.mjs", true}, {"a.cjs", true},
		{"src/Big.TS", true}, {"src/ok.TSX", true}, {"src/x.d.ts", true}, {"a.b.c.Js", true},
		{"a.json", false}, {"a.py", false}, {"a.md", false}, {"a.mts", false}, {"a.cts", false},
		{"ts", false}, {"", false}, {"src/", false},
		// Node's path.extname gives "" for a name whose only dot is the first character.
		{".ts", false}, {"src/.js", false}, {"src/..ts", true},
	}
	for _, tc := range tests {
		t.Run(tc.path, func(t *testing.T) {
			if got := IsSupported(tc.path); got != tc.want {
				t.Errorf("IsSupported(%q) = %v, want %v", tc.path, got, tc.want)
			}
		})
	}
}

func TestIsExcludedDir(t *testing.T) {
	for _, n := range []string{"node_modules", "dist", "build", "coverage", ".next", "out", "vendor", ".git"} {
		if !IsExcludedDir(n) {
			t.Errorf("IsExcludedDir(%q) = false, want true", n)
		}
	}
	for _, n := range []string{"src", "Node_Modules", "outer", "builds", ".github", ""} {
		if IsExcludedDir(n) {
			t.Errorf("IsExcludedDir(%q) = true, want false", n)
		}
	}
}

func TestSelectFiles_Rules(t *testing.T) {
	// AC3024 at the rule level: the symlinks never reach this function (the walk drops them).
	in := []FileEntry{
		{"src/x.d.ts", 10},
		{"node_modules/a.ts", 10},
		{"dist/b.js", 10},
		{"src/.next/c.ts", 10},
		{"src/Big.TS", MaxFileBytes + 1},
		{"src/ok.TSX", 10},
		{"readme.md", 10},
		{"deep/vendor/x/y.ts", 10},
		{"build.ts", 10}, // a file called build is not a directory
	}
	got := SelectFiles(in)
	want := WalkResult{
		Files: []string{"build.ts", "src/ok.TSX", "src/x.d.ts"},
		Stats: WalkStats{TotalCandidates: 4, SkippedTooLarge: 1},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("SelectFiles mismatch (-want +got):\n%s", diff)
	}
}

func TestSelectFiles_SizeLimit(t *testing.T) {
	got := SelectFiles([]FileEntry{{"a.ts", MaxFileBytes}, {"b.ts", MaxFileBytes + 1}, {"c.ts", 0}})
	want := WalkResult{Files: []string{"a.ts", "c.ts"}, Stats: WalkStats{TotalCandidates: 3, SkippedTooLarge: 1}}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("SelectFiles mismatch (-want +got):\n%s", diff)
	}
}

func TestSelectFiles_ByteOrderAndBound(t *testing.T) {
	var in []FileEntry
	for i := MaxIndexedFiles + 2; i >= 0; i-- { // 5,003 files, reverse order
		in = append(in, FileEntry{Path: fmt.Sprintf("src/f%05d.ts", i), Size: 1})
	}
	in = append(in, FileEntry{Path: "SRC/z.ts", Size: 1}) // upper case sorts before lower case
	got := SelectFiles(in)
	if len(got.Files) != MaxIndexedFiles {
		t.Fatalf("len(SelectFiles().Files) = %d, want %d", len(got.Files), MaxIndexedFiles)
	}
	if got.Stats.Bounded != 4 || got.Stats.TotalCandidates != MaxIndexedFiles+4 {
		t.Errorf("Stats = %+v, want Bounded 4 and TotalCandidates %d", got.Stats, MaxIndexedFiles+4)
	}
	if got.Files[0] != "SRC/z.ts" || got.Files[1] != "src/f00000.ts" || got.Files[MaxIndexedFiles-1] != "src/f04998.ts" {
		t.Errorf("Files = %q ... %q, want byte order cut at 5,000", got.Files[:2], got.Files[MaxIndexedFiles-1])
	}
}

func TestSelectFiles_Empty(t *testing.T) {
	got := SelectFiles(nil)
	if len(got.Files) != 0 || got.Stats != (WalkStats{}) {
		t.Errorf("SelectFiles(nil) = %+v, want empty", got)
	}
}

func TestSelectFiles_PathsThatCannotBeStoredAreSkipped(t *testing.T) {
	in := []FileEntry{
		{"src/ok.ts", 10},
		{"src/\xff.ts", 10},         // a Latin-1 style name
		{"src/trunc\xe2\x82.js", 5}, // a cut multi-byte sequence
		{"src/café.ts", 10},         // valid non-ASCII stays
		{"src/readme.md", 10},       // not a candidate at all
	}
	got := SelectFiles(in)
	want := WalkResult{
		Files: []string{"src/café.ts", "src/ok.ts"},
		Stats: WalkStats{TotalCandidates: 4, SkippedTooLarge: 2},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("SelectFiles(invalid UTF-8 names) mismatch (-want +got):\n%s", diff)
	}
}

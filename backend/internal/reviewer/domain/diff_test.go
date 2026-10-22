package domain_test

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	"dev-digest-go/backend/internal/reviewer/domain"
)

func TestParseUnifiedDiff(t *testing.T) {
	const raw = "diff --git a/src/config.ts b/src/config.ts\n--- a/src/config.ts\n+++ b/src/config.ts\n" +
		"@@ -10,3 +10,4 @@\n   port: 3000,\n+  stripeKey: \"sk_live_xxx\",\n   redisUrl: x,\n" +
		"-old\n" +
		"diff --git a/gone.ts b/gone.ts\n--- a/gone.ts\n+++ /dev/null\n@@ -1,2 +0,0 @@\n-a\n-b"
	got := domain.ParseUnifiedDiff(raw)
	want := []domain.DiffFile{
		{
			Path: "src/config.ts", Additions: 1, Deletions: 1,
			Hunks: []domain.Hunk{{
				File: "src/config.ts", OldStart: 10, OldLines: 3, NewStart: 10, NewLines: 4,
				NewLineNumbers: []int{10, 11, 12},
			}},
		},
		// "+++ /dev/null" keeps the previous path, which is empty here, so the file is dropped.
	}
	if diff := cmp.Diff(want, got.Files, cmpopts.EquateEmpty()); diff != "" {
		t.Errorf("ParseUnifiedDiff(...).Files mismatch (-want +got):\n%s", diff)
	}
	if got.Raw != raw {
		t.Errorf("ParseUnifiedDiff(...).Raw changed")
	}
}

func TestParseUnifiedDiff_Cases(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want []domain.DiffFile
	}{
		{"empty text has no files", "", nil},
		{"text without a diff has no files", "just\nsome text", nil},
		{
			"hunk header without counts means one line",
			"diff --git a/a b/a\n+++ b/a\n@@ -5 +7 @@\n+x",
			[]domain.DiffFile{{Path: "a", Additions: 1, Hunks: []domain.Hunk{{
				File: "a", OldStart: 5, OldLines: 1, NewStart: 7, NewLines: 1, NewLineNumbers: []int{7},
			}}}},
		},
		{
			"deletions consume no new line, context lines do",
			"diff --git a/a b/a\n+++ b/a\n@@ -1,4 +1,3 @@\n a\n-b\n c\n+d",
			[]domain.DiffFile{{Path: "a", Additions: 1, Deletions: 1, Hunks: []domain.Hunk{{
				File: "a", OldStart: 1, OldLines: 4, NewStart: 1, NewLines: 3, NewLineNumbers: []int{1, 2, 3},
			}}}},
		},
		{
			"+++ /dev/null keeps the path of the previous +++ line",
			"diff --git a/a b/a\n+++ b/a\n+++ /dev/null\n@@ -1 +1 @@\n+x",
			[]domain.DiffFile{{Path: "a", Additions: 1, Hunks: []domain.Hunk{{
				File: "a", OldStart: 1, OldLines: 1, NewStart: 1, NewLines: 1, NewLineNumbers: []int{1},
			}}}},
		},
		{
			"+++ and --- lines inside a hunk are never changed lines",
			"diff --git a/a b/a\n+++ b/a\n@@ -1 +1 @@\n+++x\n---y\n+z",
			// "+++x" and "---y" count as context lines (they match neither +/- test nor a header).
			[]domain.DiffFile{{Path: "a", Additions: 1, Hunks: []domain.Hunk{{
				File: "a", OldStart: 1, OldLines: 1, NewStart: 1, NewLines: 1, NewLineNumbers: []int{1, 2, 3},
			}}}},
		},
		{
			"path is trimmed like JavaScript trim",
			"diff --git a/a b/a\n+++ b/a \r\n@@ -1 +1 @@\n+x",
			[]domain.DiffFile{{Path: "a", Additions: 1, Hunks: []domain.Hunk{{
				File: "a", OldStart: 1, OldLines: 1, NewStart: 1, NewLines: 1, NewLineNumbers: []int{1},
			}}}},
		},
		{
			"a hunk header before any file starts no hunk",
			"@@ -1 +1 @@\n+x\ndiff --git a/a b/a\n+++ b/a",
			[]domain.DiffFile{{Path: "a"}},
		},
		{
			"two files, the one without a path is dropped",
			"diff --git a/x b/x\n+++ b/x\n@@ -1 +1 @@\n+1\ndiff --git a/y b/y\n@@ -1 +1 @@\n+2",
			[]domain.DiffFile{{Path: "x", Additions: 1, Hunks: []domain.Hunk{{
				File: "x", OldStart: 1, OldLines: 1, NewStart: 1, NewLines: 1, NewLineNumbers: []int{1},
			}}}},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := domain.ParseUnifiedDiff(tc.raw)
			if diff := cmp.Diff(tc.want, got.Files, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("ParseUnifiedDiff(%q).Files mismatch (-want +got):\n%s", tc.raw, diff)
			}
		})
	}
}

func TestParseUnifiedDiff_HugeHunkNumbersDoNotOverflow(t *testing.T) {
	got := domain.ParseUnifiedDiff("diff --git a/a b/a\n+++ b/a\n@@ -1 +99999999999999999999999999,2 @@\n+x")
	if len(got.Files) != 1 || len(got.Files[0].Hunks) != 1 {
		t.Fatalf("ParseUnifiedDiff = %+v, want one file with one hunk", got.Files)
	}
	if n := got.Files[0].Hunks[0].NewStart; n <= 0 {
		t.Errorf("NewStart = %d, want a positive saturated value", n)
	}
}

func TestRawDiff(t *testing.T) {
	files := []domain.PatchFile{
		{Path: "src/b.ts", Patch: "@@ -1 +1 @@\n+b"},
		{Path: "bin.png", Patch: ""},
		{Path: "Z.ts", Patch: "@@ -1 +1 @@\n+z"},
		{Path: "src/a.ts", Patch: "@@ -1 +1 @@\n+a"},
	}
	want := "diff --git a/Z.ts b/Z.ts\n--- a/Z.ts\n+++ b/Z.ts\n@@ -1 +1 @@\n+z\n" +
		"diff --git a/src/a.ts b/src/a.ts\n--- a/src/a.ts\n+++ b/src/a.ts\n@@ -1 +1 @@\n+a\n" +
		"diff --git a/src/b.ts b/src/b.ts\n--- a/src/b.ts\n+++ b/src/b.ts\n@@ -1 +1 @@\n+b"
	if got := domain.RawDiff(files); got != want {
		t.Errorf("RawDiff(...) = %q, want %q", got, want)
	}
	if files[0].Path != "src/b.ts" {
		t.Errorf("RawDiff reordered its input")
	}
	if got := domain.RawDiff(nil); got != "" {
		t.Errorf("RawDiff(nil) = %q, want empty", got)
	}
	// What RawDiff writes parses back into the same files (the diff of a run, spec 7.7.1).
	parsed := domain.ParseUnifiedDiff(want)
	var paths []string
	for _, f := range parsed.Files {
		paths = append(paths, f.Path)
	}
	if diff := cmp.Diff([]string{"Z.ts", "src/a.ts", "src/b.ts"}, paths); diff != "" {
		t.Errorf("parsed paths mismatch (-want +got):\n%s", diff)
	}
}

func TestDiff_ChangedLines(t *testing.T) {
	d := domain.Diff{Files: []domain.DiffFile{{Additions: 3, Deletions: 1}, {Additions: 2}}}
	if got := d.ChangedLines(); got != 6 {
		t.Errorf("ChangedLines() = %d, want 6", got)
	}
}

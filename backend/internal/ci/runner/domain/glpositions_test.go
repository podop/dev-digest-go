package domain_test

import (
	"strings"
	"testing"

	"dev-digest-go/backend/internal/ci/runner/domain"
)

// positionsDiff is `git diff --no-ext-diff --no-textconv --no-color -M` of a repository with a
// deleted file, a renamed file (name with a space, edited), a renamed file whose name needs git's
// quoting, a modified file with two hunks, and an added file.
const positionsDiff = "diff --git a/mod.txt b/mod.txt\n" +
	"deleted file mode 100644\n" +
	"index 01f84f8..0000000\n" +
	"--- a/mod.txt\n" +
	"+++ /dev/null\n" +
	"@@ -1,3 +0,0 @@\n" +
	"-l1\n-l2\n-l3\n" +
	"diff --git a/old name.txt b/new name.txt\n" +
	"similarity index 90%\n" +
	"rename from old name.txt\n" +
	"rename to new name.txt\n" +
	"index 92dfa21..d5a68b2 100644\n" +
	"--- a/old name.txt\t\n" +
	"+++ b/new name.txt\t\n" +
	"@@ -2,7 +2,7 @@ a\n" +
	" b\n c\n d\n-e\n+E\n f\n g\n h\n" +
	"diff --git \"a/sp\\\\tx.txt\" \"b/sp\\\\ty.txt\"\n" +
	"similarity index 90%\n" +
	"rename from \"sp\\\\tx.txt\"\n" +
	"rename to \"sp\\\\ty.txt\"\n" +
	"--- \"a/sp\\\\tx.txt\"\n" +
	"+++ \"b/sp\\\\ty.txt\"\n" +
	"@@ -2,3 +2,3 @@ a\n" +
	" b\n-c\n+C\n d\n" +
	"diff --git a/src/app.go b/src/app.go\n" +
	"index 1111111..2222222 100644\n" +
	"--- a/src/app.go\n" +
	"+++ b/src/app.go\n" +
	"@@ -1,4 +1,5 @@\n" +
	" one\n--- two\n+++ two!\n+three\n four\n" +
	"\\ No newline at end of file\n" +
	"@@ -20,2 +21,3 @@ func x() {\n" +
	" twenty\n+inserted\n twenty-one\n" +
	"diff --git a/pkg/new.go b/pkg/new.go\n" +
	"new file mode 100644\n" +
	"--- /dev/null\n" +
	"+++ b/pkg/new.go\n" +
	"@@ -0,0 +1,2 @@\n" +
	"+package pkg\n+var X = 1\n"

func TestPositionIndex_At(t *testing.T) {
	idx := domain.PositionIndex(positionsDiff)
	tests := []struct {
		name     string
		file     string
		line     int
		wantPath string
		wantLine int
	}{
		{"context line of a modified file", "src/app.go", 1, "src/app.go", 1},
		{"added line has no old line", "src/app.go", 2, "src/app.go", 0},
		{"added line after a removed one", "src/app.go", 3, "src/app.go", 0},
		{"context after additions keeps the old numbering", "src/app.go", 4, "src/app.go", 3},
		{"second hunk context", "src/app.go", 21, "src/app.go", 20},
		{"second hunk added", "src/app.go", 22, "src/app.go", 0},
		{"second hunk context after insert", "src/app.go", 23, "src/app.go", 21},
		{"line outside every hunk", "src/app.go", 10, "src/app.go", 0},
		{"line zero", "src/app.go", 0, "src/app.go", 0},
		{"negative line", "src/app.go", -4, "src/app.go", 0},
		{"renamed file, context line", "new name.txt", 2, "old name.txt", 2},
		{"renamed file, context after the edit", "new name.txt", 6, "old name.txt", 6},
		{"renamed file, added line", "new name.txt", 5, "old name.txt", 0},
		{"renamed file with a quoted name (raw key)", "\"b/sp\\\\ty.txt\"", 2, "sp\\tx.txt", 2},
		{"renamed file with a quoted name (real key)", "sp\\ty.txt", 3, "sp\\tx.txt", 0},
		{"added file", "pkg/new.go", 2, "pkg/new.go", 0},
		{"deleted file has no new side", "mod.txt", 1, "mod.txt", 0},
		{"file not in the diff", "other.go", 5, "other.go", 0},
		{"empty file name", "", 1, "", 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gotPath, gotLine := idx.At(tc.file, tc.line)
			if gotPath != tc.wantPath || gotLine != tc.wantLine {
				t.Errorf("At(%q, %d) = (%q, %d), want (%q, %d)", tc.file, tc.line, gotPath, gotLine, tc.wantPath, tc.wantLine)
			}
		})
	}
}

func TestPositionIndex_RemovedLinesLookLikeHeaders(t *testing.T) {
	// The removed lines "--- a/x" and "-- b/y" appear in a hunk as "---- a/x" and "--- b/y"; the
	// added line "++ c" appears as "+++ c". None of them is a file header.
	raw := "diff --git a/f b/f\n--- a/f\n+++ b/f\n@@ -1,3 +1,3 @@\n----- a/x\n--- b/y\n+++ c\n keep\n"
	idx := domain.PositionIndex(raw)
	if p, l := idx.At("f", 1); p != "f" || l != 0 {
		t.Errorf("At(f, 1) = (%q, %d), want (f, 0)", p, l)
	}
	if p, l := idx.At("f", 2); p != "f" || l != 3 {
		t.Errorf("At(f, 2) = (%q, %d), want (f, 3)", p, l)
	}
	if p, l := idx.At("c", 1); p != "c" || l != 0 {
		t.Errorf("At(c, 1) = (%q, %d), want (c, 0): a content line became a file", p, l)
	}
}

func TestPositionIndex_WrongCountsEndTheHunk(t *testing.T) {
	// The first hunk declares 5 lines but holds 2: the next file header must still be read.
	raw := "diff --git a/a b/a\n--- a/a\n+++ b/a\n@@ -1,5 +1,5 @@\n x\n y\n" +
		"diff --git a/b b/b\n--- a/b\n+++ b/b\n@@ -1 +1 @@\n-q\n+r\n"
	idx := domain.PositionIndex(raw)
	if p, l := idx.At("a", 2); p != "a" || l != 2 {
		t.Errorf("At(a, 2) = (%q, %d), want (a, 2)", p, l)
	}
	if p, l := idx.At("b", 1); p != "b" || l != 0 {
		t.Errorf("At(b, 1) = (%q, %d), want (b, 0)", p, l)
	}
}

func TestPositionIndex_LastFileOfARepeatedPathWins(t *testing.T) {
	raw := "diff --git a/a b/a\n--- a/a\n+++ b/a\n@@ -1 +1 @@\n x\n" +
		"diff --git a/a b/a\n--- a/a\n+++ b/a\n@@ -5 +5 @@\n y\n"
	idx := domain.PositionIndex(raw)
	if _, l := idx.At("a", 1); l != 0 {
		t.Errorf("At(a, 1) old line = %d, want 0: the earlier section was replaced", l)
	}
	if _, l := idx.At("a", 5); l != 5 {
		t.Errorf("At(a, 5) old line = %d, want 5", l)
	}
}

func TestPositionIndex_HugeNumbersDoNotOverflow(t *testing.T) {
	raw := "diff --git a/a b/a\n--- a/a\n+++ b/a\n@@ -99999999999999999999,99999999999999999999 +99999999999999999999 @@\n x\n"
	idx := domain.PositionIndex(raw)
	idx.At("a", 1)
	idx.At("a", int(^uint(0)>>1))
}

func TestPositionIndex_NoDiff(t *testing.T) {
	for _, raw := range []string{"", "\n", "garbage", "@@ -1 +1 @@\n x\n"} {
		if p, l := domain.PositionIndex(raw).At("a", 1); p != "a" || l != 0 {
			t.Errorf("PositionIndex(%q).At(a, 1) = (%q, %d), want (a, 0)", raw, p, l)
		}
	}
}

func FuzzPositionIndex(f *testing.F) {
	f.Add(positionsDiff, "src/app.go", 3)
	f.Add("diff --git a/a b/a\n--- a/a\n+++ b/a\n@@ -1,2 +1,2 @@\n-x\n+y\n z\n", "a", 2)
	f.Add("@@ -0,0 +1 @@\n+x", "", 0)
	f.Add("diff --git \"a/q\" \"b/q\"\n--- \"a/\\\"\n+++ \"b/\\\"\n@@ -1 +1 @@\n x\n", "\"b/\\\"", 1)
	f.Fuzz(func(t *testing.T, raw, file string, line int) {
		idx := domain.PositionIndex(raw)
		oldPath, oldLine := idx.At(file, line)
		if oldLine < 0 {
			t.Fatalf("At(%q, %d) old line = %d, want >= 0", file, line, oldLine)
		}
		if oldPath == "" && file != "" && !strings.Contains(raw, "rename from") {
			t.Fatalf("At(%q, %d) old path is empty for a non-empty file", file, line)
		}
		if again, againLine := idx.At(file, line); again != oldPath || againLine != oldLine {
			t.Fatalf("At(%q, %d) is not stable: (%q, %d) then (%q, %d)", file, line, oldPath, oldLine, again, againLine)
		}
	})
}

package domain_test

import (
	"strconv"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/reviewer/domain"
)

const (
	sliceATS = "diff --git a/src/a.ts b/src/a.ts\nindex 111..222 100644\n--- a/src/a.ts\n+++ b/src/a.ts\n@@ -1,1 +1,2 @@\n const a = 1;\n+const b = 2;"
	sliceATX = "diff --git a/src/a.tsx b/src/a.tsx\n--- a/src/a.tsx\n+++ b/src/a.tsx\n@@ -1 +1 @@\n-old tsx\n+new tsx"
	sliceRen = "diff --git a/src/old.ts b/src/new.ts\nsimilarity index 90%\nrename from src/old.ts\nrename to src/new.ts\n@@ -3 +3 @@\n-x\n+y"
	sliceDel = "diff --git a/src/gone.ts b/src/gone.ts\ndeleted file mode 100644\n--- a/src/gone.ts\n+++ /dev/null\n@@ -1,2 +0,0 @@\n-line1\n-line2"
)

func TestSliceDiff(t *testing.T) {
	full := domain.Diff{Raw: strings.Join([]string{sliceATX, sliceATS, sliceRen, sliceDel}, "\n")}
	quoted := "diff --git \"a/my dir/f.ts\" \"b/my dir/f.ts\"\n@@ -1 +1 @@\n-a\n+b"
	tests := []struct {
		name string
		raw  string
		path string
		want string
	}{
		{"exact path: src/a.ts does not capture src/a.tsx", full.Raw, "src/a.ts", sliceATS},
		{"the other way round", full.Raw, "src/a.tsx", sliceATX},
		{"renamed file by its new path", full.Raw, "src/new.ts", sliceRen},
		{"renamed file by its old path", full.Raw, "src/old.ts", sliceRen},
		{"deleted file", full.Raw, "src/gone.ts", sliceDel},
		{"quoted header with spaces", sliceATS + "\n" + quoted, "my dir/f.ts", quoted},
		{"unquoted header with spaces", "diff --git a/my dir/f.ts b/my dir/f.ts\n@@ -1 +1 @@\n+x", "my dir/f.ts", "diff --git a/my dir/f.ts b/my dir/f.ts\n@@ -1 +1 @@\n+x"},
		{"quoted header with an escaped quote", "diff --git \"a/q\\\"x.ts\" \"b/q\\\"x.ts\"\n+1", "q\"x.ts", "diff --git \"a/q\\\"x.ts\" \"b/q\\\"x.ts\"\n+1"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := domain.SliceDiff(domain.Diff{Raw: tc.raw}, tc.path); got != tc.want {
				t.Errorf("SliceDiff(%q, %q) = %q, want %q", tc.raw, tc.path, got, tc.want)
			}
		})
	}
}

func TestSliceDiff_RebuildsHunkHeadersWhenRawLacksTheFile(t *testing.T) {
	d := domain.Diff{
		Raw: sliceATS,
		Files: []domain.DiffFile{{
			Path: "src/z.ts",
			Hunks: []domain.Hunk{
				{OldStart: 3, OldLines: 2, NewStart: 3, NewLines: 4},
				{OldStart: 20, OldLines: 1, NewStart: 22, NewLines: 1},
			},
		}},
	}
	want := "diff --git a/src/z.ts b/src/z.ts\n--- a/src/z.ts\n+++ b/src/z.ts\n@@ -3,2 +3,4 @@\n@@ -20,1 +22,1 @@"
	if got := domain.SliceDiff(d, "src/z.ts"); got != want {
		t.Errorf("SliceDiff(rebuild) = %q, want %q", got, want)
	}
	if got := domain.SliceDiff(d, "unknown.ts"); got != d.Raw {
		t.Errorf("SliceDiff(unknown path) = %q, want the whole raw diff", got)
	}
}

func TestFileChunks(t *testing.T) {
	d := domain.ParseUnifiedDiff(sliceATX + "\n" + sliceATS)
	got := domain.FileChunks(d)
	want := []domain.Chunk{
		{Label: "src/a.tsx", DiffText: sliceATX},
		{Label: "src/a.ts", DiffText: sliceATS},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("FileChunks mismatch (-want +got):\n%s", diff)
	}
}

func TestGuardChunkSize_ChunksWithinTheCapAreKept(t *testing.T) {
	in := []domain.Chunk{{Label: "all files", DiffText: sliceATS}}
	got, notes := domain.GuardChunkSize(in, domain.MaxChunkUnits)
	if diff := cmp.Diff(in, got); diff != "" || len(notes) != 0 {
		t.Errorf("GuardChunkSize(small) = %v, %v; want the input and no notes\n%s", got, notes, diff)
	}
}

// AC4043: the labels, the notes and the run's mode.
func TestGuardChunkSize_SplitAndTruncated(t *testing.T) {
	hunks := func(starts ...int) string {
		var parts []string
		for _, s := range starts {
			parts = append(parts, bigHunk(s, 150_000))
		}
		return strings.Join(parts, "\n")
	}
	split := testHeader + "\n" + hunks(1, 1000, 2000)
	trunc := "diff --git a/one.ts b/one.ts\n--- a/one.ts\n+++ b/one.ts\n" + bigHunk(1, 250_000)
	small := "diff --git a/s.ts b/s.ts\n@@ -1 +1 @@\n+x"

	got, notes := domain.GuardChunkSize([]domain.Chunk{
		{Label: "big.ts", DiffText: split},
		{Label: "one.ts", DiffText: trunc},
		{Label: "s.ts", DiffText: small},
	}, domain.MaxChunkUnits)

	var labels []string
	for _, c := range got {
		labels = append(labels, c.Label)
	}
	wantLabels := []string{"big.ts (part 1/3)", "big.ts (part 2/3)", "big.ts (part 3/3)", "one.ts", "s.ts"}
	if diff := cmp.Diff(wantLabels, labels); diff != "" {
		t.Errorf("chunk labels mismatch (-want +got):\n%s", diff)
	}
	if got[4].DiffText != small {
		t.Errorf("a chunk within the cap changed: %q", got[4].DiffText)
	}
	if !strings.Contains(got[3].DiffText, "[diff truncated by reviewer-core: ") {
		t.Errorf("the 250,000-unit chunk was not truncated")
	}

	wantNotes := []domain.Oversize{
		{Label: "big.ts", Units: domain.Len16(split), Parts: 3, Truncated: false, Max: 200_000},
		{Label: "one.ts", Units: domain.Len16(trunc), Parts: 1, Truncated: true, Max: 200_000},
	}
	if diff := cmp.Diff(wantNotes, notes); diff != "" {
		t.Fatalf("oversize notes mismatch (-want +got):\n%s", diff)
	}
	wantMsg := "warning: big.ts diff (" + strconv.Itoa(domain.Len16(split)) + " chars) exceeds maxDiffChars=200000 → split into 3 part(s)"
	if m := notes[0].Message(); m != wantMsg {
		t.Errorf("notes[0].Message() = %q, want %q", m, wantMsg)
	}
	wantMsg = "warning: one.ts diff (" + strconv.Itoa(domain.Len16(trunc)) + " chars) exceeds maxDiffChars=200000 → split into 1 part(s); oversize hunk(s) truncated"
	if m := notes[1].Message(); m != wantMsg {
		t.Errorf("notes[1].Message() = %q, want %q", m, wantMsg)
	}
	if mode := domain.EffectiveMode(domain.ModeSinglePass, len(got)); mode != domain.ModeMapReduce {
		t.Errorf("EffectiveMode(single-pass, %d chunks) = %q, want map-reduce", len(got), mode)
	}
}

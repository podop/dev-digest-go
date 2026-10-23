package domain_test

import (
	"strconv"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/evals/domain"
)

func same(l string) domain.DiffLine { return domain.DiffLine{Op: domain.OpSame, Line: l} }
func add(l string) domain.DiffLine  { return domain.DiffLine{Op: domain.OpAdd, Line: l} }
func del(l string) domain.DiffLine  { return domain.DiffLine{Op: domain.OpDel, Line: l} }

func TestLineDiff(t *testing.T) {
	tests := []struct {
		name          string
		before, after string
		want          []domain.DiffLine
	}{
		{"both empty", "", "", []domain.DiffLine{}},
		{"identical", "a\nb", "a\nb", []domain.DiffLine{same("a"), same("b")}},
		{"empty before: all added", "", "a\nb", []domain.DiffLine{add("a"), add("b")}},
		{"empty after: all deleted", "a\nb", "", []domain.DiffLine{del("a"), del("b")}},
		{"single empty line is one line", "\n", "", []domain.DiffLine{del(""), del("")}},
		{"one empty line to nothing", "", "\n", []domain.DiffLine{add(""), add("")}},
		{"change in the middle", "a\nb\nc", "a\nx\nc", []domain.DiffLine{same("a"), del("b"), add("x"), same("c")}},
		{"append", "a", "a\nb", []domain.DiffLine{same("a"), add("b")}},
		{"prepend", "b", "a\nb", []domain.DiffLine{add("a"), same("b")}},
		{
			"lcs picks the common subsequence",
			"a\nb\nc\nd\ne", "x\nb\ny\nd\nz",
			[]domain.DiffLine{del("a"), add("x"), same("b"), del("c"), add("y"), same("d"), del("e"), add("z")},
		},
		{
			// A B vs B A: LCS length 1; on the tie a deletion comes first, so "B" is kept.
			"tie prefers deletion",
			"A\nB", "B\nA",
			[]domain.DiffLine{del("A"), same("B"), add("A")},
		},
		{
			"head and tail are trimmed before the lcs",
			"h\nm1\nm2\nt", "h\nn1\nt",
			[]domain.DiffLine{same("h"), del("m1"), del("m2"), add("n1"), same("t")},
		},
		{
			"overlapping head and tail",
			"a\na", "a\na\na",
			[]domain.DiffLine{same("a"), same("a"), add("a")},
		},
		{"trailing newline is an empty last line", "a\n", "a", []domain.DiffLine{same("a"), del("")}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := domain.LineDiff(tc.before, tc.after)
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("LineDiff(%q, %q) mismatch (-want +got):\n%s", tc.before, tc.after, diff)
			}
		})
	}
}

func TestLineDiff_TooBigDegrades(t *testing.T) {
	// Middles of 2000 lines each, no common line: (2001)(2001) > 4,000,000 cells.
	var a, b []string
	for i := range 2000 {
		a = append(a, "a"+strconv.Itoa(i))
		b = append(b, "b"+strconv.Itoa(i))
	}
	// A common line inside the middles would be found by an LCS, but the degrade ignores it.
	a[1000], b[1000] = "same", "same"
	got := domain.LineDiff("head\n"+strings.Join(a, "\n")+"\ntail", "head\n"+strings.Join(b, "\n")+"\ntail")

	if len(got) != 2+2*2000 {
		t.Fatalf("len(LineDiff) = %d, want %d", len(got), 2+2*2000)
	}
	if got[0] != same("head") || got[len(got)-1] != same("tail") {
		t.Errorf("head, tail = %v, %v, want same lines", got[0], got[len(got)-1])
	}
	for i := 1; i <= 2000; i++ {
		if got[i].Op != domain.OpDel {
			t.Fatalf("line %d op = %s, want del (all deletions first)", i, got[i].Op)
		}
	}
	for i := 2001; i <= 4000; i++ {
		if got[i].Op != domain.OpAdd {
			t.Fatalf("line %d op = %s, want add", i, got[i].Op)
		}
	}
}

func TestLineDiff_LargestLCSStillDiffs(t *testing.T) {
	// 1999 x 1999 middles: (2000)(2000) = 4,000,000, not above the limit, so the LCS runs.
	var a, b []string
	for i := range 1999 {
		a = append(a, "a"+strconv.Itoa(i))
		b = append(b, "b"+strconv.Itoa(i))
	}
	a[1000], b[1000] = "same", "same"
	got := domain.LineDiff("h\n"+strings.Join(a, "\n")+"\nt", "h\n"+strings.Join(b, "\n")+"\nt")
	sames := 0
	for _, l := range got {
		if l.Op == domain.OpSame && l.Line == "same" {
			sames++
		}
	}
	if sames != 1 {
		t.Errorf("LineDiff kept %d common middle lines, want 1 (the LCS runs at exactly 4,000,000 cells)", sames)
	}
}

// replay applies the diff to rebuild both texts: a property every LineDiff must keep.
func TestLineDiff_ReplaysBothSides(t *testing.T) {
	texts := []string{"", "a", "a\nb\nc", "c\nb\na", "x\na\ny\nb\nz", "a\na\nb\nb", "b\nb\na\na", "\n\n", "q"}
	for _, before := range texts {
		for _, after := range texts {
			var gotA, gotB []string
			for _, l := range domain.LineDiff(before, after) {
				if l.Op != domain.OpAdd {
					gotA = append(gotA, l.Line)
				}
				if l.Op != domain.OpDel {
					gotB = append(gotB, l.Line)
				}
			}
			join := func(l []string) string { return strings.Join(l, "\n") }
			if join(gotA) != before || join(gotB) != after {
				t.Errorf("LineDiff(%q, %q) replays to %q, %q", before, after, join(gotA), join(gotB))
			}
		}
	}
}

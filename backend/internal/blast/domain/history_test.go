package domain_test

import (
	"fmt"
	"testing"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/blast/domain"
)

func TestSelectPriorPRs_Selection(t *testing.T) {
	candidates := []domain.MergedPR{
		{Number: 10, Title: "this PR", MergedAt: "2026-10-01T00:00:00Z", Author: "me", Files: []string{"a.ts"}},
		{Number: 11, Title: "touches two", MergedAt: "2026-09-01T10:00:00Z", Author: "ann", Files: []string{"z.ts", "a.ts", "other.ts", "a.ts", "b.ts"}},
		{Number: 12, Title: "no overlap", MergedAt: "2026-09-02T10:00:00Z", Author: "bob", Files: []string{"other.ts"}},
		{Number: 13, Title: "no files", MergedAt: "2026-09-03T10:00:00Z", Author: "unknown"},
	}
	got := domain.SelectPriorPRs(candidates, 10, []string{"a.ts", "b.ts", "z.ts"})
	want := []domain.PriorPR{{
		PRNumber: 11, Title: "touches two", MergedAt: "2026-09-01T10:00:00Z", Author: "ann",
		FilesOverlap: []string{"a.ts", "b.ts", "z.ts"},
	}}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("SelectPriorPRs() mismatch (-want +got):\n%s", diff)
	}
}

func TestSelectPriorPRs_OverlapIsInCodeUnitOrder(t *testing.T) {
	// UTF-8 byte order would put U+10000 after U+E000; JavaScript's default sort (UTF-16 code
	// units) puts the surrogate pair first. English collation would order them differently again.
	files := []string{".ts", "\U00010000.ts", "b.ts", "B.ts", "a.ts"}
	got := domain.SelectPriorPRs([]domain.MergedPR{{Number: 1, Files: files}}, 2, files)
	want := []string{"B.ts", "a.ts", "b.ts", "\U00010000.ts", ".ts"}
	if len(got) != 1 {
		t.Fatalf("SelectPriorPRs() = %d items, want 1", len(got))
	}
	if diff := cmp.Diff(want, got[0].FilesOverlap); diff != "" {
		t.Errorf("FilesOverlap mismatch (-want +got):\n%s", diff)
	}
}

func TestSelectPriorPRs_Order(t *testing.T) {
	pr := func(n int, mergedAt string) domain.MergedPR {
		return domain.MergedPR{Number: n, MergedAt: mergedAt, Files: []string{"a.ts"}}
	}
	tests := []struct {
		name string
		in   []domain.MergedPR
		want []int
	}{
		{"newest merge first", []domain.MergedPR{pr(1, "2026-01-01T00:00:00Z"), pr(2, "2026-03-01T00:00:00Z"), pr(3, "2026-02-01T00:00:00Z")}, []int{2, 3, 1}},
		{"equal time: higher number first", []domain.MergedPR{pr(5, "2026-01-01T00:00:00Z"), pr(9, "2026-01-01T00:00:00Z"), pr(7, "2026-01-01T00:00:00Z")}, []int{9, 7, 5}},
		{"offsets are compared as instants", []domain.MergedPR{pr(1, "2026-01-01T12:00:00+02:00"), pr(2, "2026-01-01T11:00:00Z"), pr(3, "2026-01-01T09:30:00-01:00")}, []int{2, 3, 1}},
		{"milliseconds count", []domain.MergedPR{pr(1, "2026-01-01T00:00:00.100Z"), pr(2, "2026-01-01T00:00:00.900Z")}, []int{2, 1}},
		{"unparseable counts as the oldest", []domain.MergedPR{pr(1, "yesterday"), pr(2, "1999-01-01T00:00:00Z"), pr(3, "")}, []int{2, 3, 1}},
		{"date only is UTC midnight", []domain.MergedPR{pr(1, "2026-01-01"), pr(2, "2026-01-01T00:00:01Z")}, []int{2, 1}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var got []int
			for _, p := range domain.SelectPriorPRs(tc.in, 0, []string{"a.ts"}) {
				got = append(got, p.PRNumber)
			}
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("SelectPriorPRs() order mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestSelectPriorPRs_KeepsAtMostTenAfterOrdering(t *testing.T) {
	var cands []domain.MergedPR
	for n := 1; n <= 25; n++ {
		cands = append(cands, domain.MergedPR{
			Number: n, MergedAt: fmt.Sprintf("2026-01-%02dT00:00:00Z", n), Files: []string{"a.ts"},
		})
	}
	got := domain.SelectPriorPRs(cands, 0, []string{"a.ts"})
	if len(got) != domain.HistoryMax {
		t.Fatalf("SelectPriorPRs() = %d items, want %d", len(got), domain.HistoryMax)
	}
	if got[0].PRNumber != 25 || got[9].PRNumber != 16 {
		t.Errorf("SelectPriorPRs() = #%d ... #%d, want the ten newest, #25 ... #16", got[0].PRNumber, got[9].PRNumber)
	}
}

func TestSelectPriorPRs_NeverNil(t *testing.T) {
	for name, got := range map[string][]domain.PriorPR{
		"no candidates": domain.SelectPriorPRs(nil, 1, []string{"a.ts"}),
		"no files":      domain.SelectPriorPRs([]domain.MergedPR{{Number: 2, Files: []string{"a.ts"}}}, 1, nil),
	} {
		if got == nil || len(got) != 0 {
			t.Errorf("%s: SelectPriorPRs() = %#v, want an empty non-nil slice", name, got)
		}
	}
}

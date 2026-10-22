package domain_test

import (
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/reviewer/domain"
)

func groundDiff() domain.Diff {
	return domain.Diff{Files: []domain.DiffFile{{
		Path: "a.ts",
		Hunks: []domain.Hunk{{
			File: "a.ts", NewStart: 10, NewLines: 5, NewLineNumbers: []int{10, 11, 12, 13, 14},
		}},
	}}}
}

// AC4045
func TestGroundFindings(t *testing.T) {
	tests := []struct {
		name       string
		f          domain.Finding
		wantKept   bool
		wantReason string
	}{
		{"inside the range", finding("1", domain.SeverityWarning, "a.ts", 12, 12), true, ""},
		{"range runs past the hunk but includes a line", finding("2", domain.SeverityWarning, "a.ts", 14, 30), true, ""},
		{"reversed range includes a line", finding("3", domain.SeverityWarning, "a.ts", 30, 10), true, ""},
		{"range after the hunk", finding("4", domain.SeverityWarning, "a.ts", 15, 20), false, "lines 15-20 do not intersect any diff hunk in 'a.ts'"},
		{"range before the hunk", finding("5", domain.SeverityWarning, "a.ts", 1, 9), false, "lines 1-9 do not intersect any diff hunk in 'a.ts'"},
		{"file not in the diff", finding("6", domain.SeverityWarning, "b.ts", 12, 12), false, "file 'b.ts' not present in diff"},
		{"both ends below 1", finding("7", domain.SeverityWarning, "a.ts", -5, 0), false, "lines -5-0 do not intersect any diff hunk in 'a.ts'"},
		{"a negative start with an end inside", finding("8", domain.SeverityWarning, "a.ts", -5, 11), true, ""},
		{"a huge end", finding("9", domain.SeverityWarning, "a.ts", 1, 1_000_000_000_000), true, ""},
		{"a huge range after the hunk", finding("10", domain.SeverityWarning, "a.ts", 20, 1_000_000_000_000), false, "lines 20-1000000000000 do not intersect any diff hunk in 'a.ts'"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := domain.GroundFindings([]domain.Finding{tc.f}, groundDiff())
			if kept := len(got.Kept) == 1; kept != tc.wantKept {
				t.Fatalf("GroundFindings(%d-%d in %s) kept = %v, want %v", tc.f.StartLine, tc.f.EndLine, tc.f.File, kept, tc.wantKept)
			}
			if !tc.wantKept && got.Dropped[0].Reason != tc.wantReason {
				t.Errorf("dropped reason = %q, want %q", got.Dropped[0].Reason, tc.wantReason)
			}
		})
	}
}

// EC4011: a full-file kind needs only the file.
func TestGroundFindings_FullFileKinds(t *testing.T) {
	for _, kind := range []domain.Kind{domain.KindSecretLeak, domain.KindLethalTrifecta, domain.KindPhantom, domain.KindHook} {
		f := finding("1", domain.SeverityCritical, "a.ts", 99, 99)
		f.Kind = kind
		if got := domain.GroundFindings([]domain.Finding{f}, groundDiff()); len(got.Kept) != 1 {
			t.Errorf("GroundFindings(%s on a.ts line 99) dropped it, want kept", kind)
		}
		f.File = "b.ts"
		got := domain.GroundFindings([]domain.Finding{f}, groundDiff())
		if len(got.Dropped) != 1 || got.Dropped[0].Reason != "file 'b.ts' not present in diff" {
			t.Errorf("GroundFindings(%s on b.ts) = %+v, want dropped: file not in diff", kind, got)
		}
	}
	f := finding("1", domain.SeverityCritical, "a.ts", 99, 99)
	f.Kind = domain.KindFinding
	if got := domain.GroundFindings([]domain.Finding{f}, groundDiff()); len(got.Kept) != 0 {
		t.Errorf("kind finding on a line outside the hunks was kept")
	}
}

func TestGroundFindings_SummaryAndOrder(t *testing.T) {
	in := []domain.Finding{
		finding("keep1", domain.SeverityWarning, "a.ts", 10, 10),
		finding("drop1", domain.SeverityWarning, "x.ts", 1, 1),
		finding("keep2", domain.SeverityWarning, "a.ts", 11, 11),
		finding("drop2", domain.SeverityWarning, "a.ts", 50, 50),
	}
	got := domain.GroundFindings(in, groundDiff())
	if s := got.Summary(); s != "2/4 passed" {
		t.Errorf("Summary() = %q, want %q", s, "2/4 passed")
	}
	var kept, dropped []string
	for _, f := range got.Kept {
		kept = append(kept, f.ID)
	}
	for _, d := range got.Dropped {
		dropped = append(dropped, d.Finding.ID)
	}
	if diff := cmp.Diff([]string{"keep1", "keep2"}, kept); diff != "" {
		t.Errorf("kept order mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff([]string{"drop1", "drop2"}, dropped); diff != "" {
		t.Errorf("dropped order mismatch (-want +got):\n%s", diff)
	}
	if s := (domain.GroundingResult{}).Summary(); s != "0/0 passed" {
		t.Errorf("empty Summary() = %q, want %q", s, "0/0 passed")
	}
}

// EC4008: a PR without files drops every finding, and an empty diff is not an error.
func TestGroundFindings_EmptyDiff(t *testing.T) {
	got := domain.GroundFindings([]domain.Finding{finding("1", domain.SeverityWarning, "a.ts", 1, 1)}, domain.ParseUnifiedDiff(""))
	if len(got.Kept) != 0 || len(got.Dropped) != 1 {
		t.Errorf("GroundFindings(empty diff) = %+v, want everything dropped", got)
	}
}

func TestGroundFindings_HunkWithoutListedLinesCoversItsDeclaredRange(t *testing.T) {
	d := domain.Diff{Files: []domain.DiffFile{{Path: "a.ts", Hunks: []domain.Hunk{
		{NewStart: 5, NewLines: 3},                   // 5..7
		{NewStart: 20, NewLines: 0},                  // a pure deletion still covers line 20
		{NewStart: 100, NewLines: 1_000_000_000_000}, // a huge declared range costs nothing
	}}}}
	tests := []struct {
		line int
		want bool
	}{{4, false}, {5, true}, {7, true}, {8, false}, {20, true}, {21, false}, {50_000_000_000, true}}
	for _, tc := range tests {
		got := domain.GroundFindings([]domain.Finding{finding("1", domain.SeverityWarning, "a.ts", tc.line, tc.line)}, d)
		if kept := len(got.Kept) == 1; kept != tc.want {
			t.Errorf("GroundFindings(line %d) kept = %v, want %v", tc.line, kept, tc.want)
		}
	}
}

func TestGroundFindings_LastFileOfARepeatedPathWins(t *testing.T) {
	d := domain.Diff{Files: []domain.DiffFile{
		{Path: "a.ts", Hunks: []domain.Hunk{{NewLineNumbers: []int{1}}}},
		{Path: "a.ts", Hunks: []domain.Hunk{{NewLineNumbers: []int{2}}}},
	}}
	if got := domain.GroundFindings([]domain.Finding{finding("1", domain.SeverityWarning, "a.ts", 1, 1)}, d); len(got.Kept) != 0 {
		t.Errorf("a line of the first of two same-path files was kept; the Node index keeps only the last")
	}
	if got := domain.GroundFindings([]domain.Finding{finding("1", domain.SeverityWarning, "a.ts", 2, 2)}, d); len(got.Kept) != 1 {
		t.Errorf("a line of the last of two same-path files was dropped")
	}
}

// EC4010: a model-cited range of 10^12 lines must not turn into work.
func TestGroundFindings_HugeRangeIsBoundedByTheHunk(t *testing.T) {
	in := make([]domain.Finding, 1000)
	for i := range in {
		in[i] = finding("x", domain.SeverityWarning, "a.ts", -1_000_000_000_000, 1_000_000_000_000)
	}
	start := time.Now()
	got := domain.GroundFindings(in, groundDiff())
	if len(got.Kept) != 1000 {
		t.Fatalf("kept %d, want 1000", len(got.Kept))
	}
	if d := time.Since(start); d > time.Second {
		t.Errorf("GroundFindings with 10^12-line ranges took %v, want well under a second", d)
	}
}

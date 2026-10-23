package domain_test

import (
	"strconv"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/evals/domain"
)

func TestNormalizeCaseDiff(t *testing.T) {
	hunk := "@@ -1 +1 @@\n-a\n+b"
	wrapped := "diff --git a/f.go b/f.go\n--- a/f.go\n+++ b/f.go\n"
	tests := []struct {
		name, raw, want string
	}{
		{"headerless paste is wrapped", hunk, wrapped + hunk},
		{"crlf becomes lf", "@@ -1 +1 @@\r\n-a\r\n+b", wrapped + hunk},
		{"diff --git header is kept", "diff --git a/x b/x\n" + hunk, "diff --git a/x b/x\n" + hunk},
		{"+++ header is kept", "--- a/x\n+++ b/x\n" + hunk, "--- a/x\n+++ b/x\n" + hunk},
		{"+++ header on a later line", hunk + "\n+++ b/x", hunk + "\n+++ b/x"},
		{"+++ without the space is no header", "+++b/x\n" + hunk, wrapped + "+++b/x\n" + hunk},
		{"header only counts at a line start", " diff --git a/x b/x\n" + hunk, wrapped + " diff --git a/x b/x\n" + hunk},
		{"lone CR starts a line as in JavaScript", "a\r+++ b/x", "a\r+++ b/x"},
		{"U+2028 starts a line as in JavaScript", "a\u2028diff --git x", "a\u2028diff --git x"},
		{"U+2029 starts a line as in JavaScript", "a\u2029+++ x", "a\u2029+++ x"},
		{"NEL does not", "a\u0085+++ x", wrapped + "a\u0085+++ x"},
		{"empty text", "", wrapped},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := domain.NormalizeCaseDiff(tc.raw, "f.go"); got != tc.want {
				t.Errorf("NormalizeCaseDiff(%q) = %q, want %q", tc.raw, got, tc.want)
			}
		})
	}
}

// twoFileDiff has a.go with new lines 1-3 and 10, b.go with 5-6.
const twoFileDiff = "diff --git a/a.go b/a.go\n--- a/a.go\n+++ b/a.go\n" +
	"@@ -1,2 +1,3 @@\n x\n+y\n z\n" +
	"@@ -9,1 +10,1 @@\n-q\n+r\n" +
	"diff --git a/b.go b/b.go\n--- a/b.go\n+++ b/b.go\n" +
	"@@ -5,1 +5,2 @@\n k\n+l"

func exp(file string, start, end int) domain.Expectation {
	return domain.Expectation{Kind: domain.MustFind, File: file, StartLine: start, EndLine: end}
}

func TestCheckCaseContent_Passes(t *testing.T) {
	tests := []struct {
		name string
		e    domain.Expectation
	}{
		{"inside a run", exp("a.go", 2, 2)},
		{"touching the run end", exp("a.go", 3, 9)},
		{"touching the second run", exp("a.go", 10, 20)},
		{"spanning the gap", exp("a.go", 3, 10)},
		{"other file", exp("b.go", 6, 6)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := domain.CheckCaseContent(twoFileDiff, tc.e); err != nil {
				t.Errorf("CheckCaseContent(%+v) = %v, want nil", tc.e, err)
			}
		})
	}
}

func TestCheckCaseContent_Failures(t *testing.T) {
	manyFiles := ""
	var wantFiles []string
	for i := range 60 {
		p := "f" + string(rune('A'+i%26)) + string(rune('a'+i/26)) + ".go"
		wantFiles = append(wantFiles, p)
		manyFiles += "diff --git a/" + p + " b/" + p + "\n--- a/" + p + "\n+++ b/" + p + "\n@@ -1 +1 @@\n-a\n+b\n"
	}
	// 70 isolated new-side lines: 2, 4, 6 ... 140.
	var manyRanges strings.Builder
	manyRanges.WriteString("diff --git a/r.go b/r.go\n--- a/r.go\n+++ b/r.go\n")
	var wantRanges []domain.LineRange
	for i := range 70 {
		n := 2 + 2*i
		manyRanges.WriteString("@@ -" + strconv.Itoa(n) + ",1 +" + strconv.Itoa(n) + ",1 @@\n+x\n")
		wantRanges = append(wantRanges, domain.LineRange{Start: n, End: n})
	}

	tests := []struct {
		name        string
		diff        string
		e           domain.Expectation
		wantCode    string
		wantMsg     string
		wantDetails map[string]any
	}{
		{
			"too long", strings.Repeat("x", 200_001), exp("a.go", 1, 1),
			"patch_too_large", "The diff exceeds 200000 characters", nil,
		},
		{
			"no hunk", "diff --git a/a.go b/a.go\n--- a/a.go\n+++ b/a.go\n", exp("a.go", 1, 1),
			"diff_invalid", "The diff has no file with a hunk (@@ header)", nil,
		},
		{
			"empty", "", exp("a.go", 1, 1),
			"diff_invalid", "The diff has no file with a hunk (@@ header)", nil,
		},
		{
			"file missing", twoFileDiff, exp("c.go", 1, 1),
			"expectation_file_not_in_diff", "The expectation file is not among the diff files",
			map[string]any{"files": []string{"a.go", "b.go"}},
		},
		{
			"path match is exact", twoFileDiff, exp("./a.go", 1, 1),
			"expectation_file_not_in_diff", "The expectation file is not among the diff files",
			map[string]any{"files": []string{"a.go", "b.go"}},
		},
		{
			"files detail capped at 50", manyFiles, exp("nope.go", 1, 1),
			"expectation_file_not_in_diff", "The expectation file is not among the diff files",
			map[string]any{"files": wantFiles[:50]},
		},
		{
			"lines outside", twoFileDiff, exp("a.go", 4, 9),
			"expectation_lines_not_in_diff", "The expectation lines do not intersect a changed or context line of the file",
			map[string]any{"ranges": []domain.LineRange{{Start: 1, End: 3}, {Start: 10, End: 10}}},
		},
		{
			"ranges detail capped at 50", manyRanges.String(), exp("r.go", 1, 1),
			"expectation_lines_not_in_diff", "The expectation lines do not intersect a changed or context line of the file",
			map[string]any{"ranges": wantRanges[:50]},
		},
		{
			// a file header with a hunk elsewhere: the hunks of other files do not count
			"file present without lines", "diff --git a/a.go b/a.go\n--- a/a.go\n+++ b/a.go\ndiff --git a/b.go b/b.go\n--- a/b.go\n+++ b/b.go\n@@ -1 +1 @@\n+x", exp("a.go", 1, 1),
			"expectation_lines_not_in_diff", "The expectation lines do not intersect a changed or context line of the file",
			map[string]any{"ranges": []domain.LineRange{}},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := domain.CheckCaseContent(tc.diff, tc.e)
			re := ruleErr(t, err)
			if re.Kind != domain.RuleUnprocessable || re.Code != tc.wantCode || re.Message != tc.wantMsg {
				t.Errorf("CheckCaseContent error = %d %s %q, want 422 %s %q", re.Kind, re.Code, re.Message, tc.wantCode, tc.wantMsg)
			}
			if diff := cmp.Diff(tc.wantDetails, re.Details); diff != "" {
				t.Errorf("CheckCaseContent details mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestCheckCaseContent_TrailingNewlineAddsAContextLine(t *testing.T) {
	// The server's parser counts the empty last line as a context line (spec 7.4.2 step 3).
	diff := "diff --git a/a.go b/a.go\n--- a/a.go\n+++ b/a.go\n@@ -1 +1 @@\n+x\n"
	if err := domain.CheckCaseContent(diff, exp("a.go", 2, 2)); err != nil {
		t.Errorf("CheckCaseContent(line 2 after a trailing newline) = %v, want nil", err)
	}
}

func TestBuildManualCase(t *testing.T) {
	base := domain.CreateInput{
		Name:  "My case",
		Notes: sp("n"),
		Expectation: domain.Expectation{
			Kind: domain.MustNotFlag, File: "a.go", StartLine: 1, EndLine: 4, Category: sp("bug"),
		},
	}
	tests := []struct {
		name      string
		mutate    func(*domain.CreateInput)
		wantTitle string
		wantDesc  *string
		wantNotes *string
	}{
		{"defaults", func(in *domain.CreateInput) {}, "My case", nil, sp("n")},
		{"title and description kept verbatim", func(in *domain.CreateInput) { in.PRTitle = sp(" T "); in.PRDescription = sp(" D\n") }, " T ", sp(" D\n"), sp("n")},
		{"blank title is the name", func(in *domain.CreateInput) { in.PRTitle = sp(" \u00A0"); in.PRDescription = sp("\n ") }, "My case", nil, sp("n")},
		{"no notes", func(in *domain.CreateInput) { in.Notes = nil }, "My case", nil, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			in := base
			tc.mutate(&in)
			got := domain.BuildManualCase("ws", domain.OwnerSkill, "sk1", in, "DIFF")
			want := domain.NewCase{
				WorkspaceID: "ws", OwnerKind: domain.OwnerSkill, OwnerID: "sk1", Name: "My case", Notes: tc.wantNotes,
				Expectation: domain.Expectation{Kind: domain.MustNotFlag, File: "a.go", StartLine: 1, EndLine: 4, Category: sp("bug")},
				InputDiff:   "DIFF",
				InputMeta:   domain.InputMeta{PRTitle: tc.wantTitle, PRDescription: tc.wantDesc},
			}
			if diff := cmp.Diff(want, got); diff != "" {
				t.Errorf("BuildManualCase mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

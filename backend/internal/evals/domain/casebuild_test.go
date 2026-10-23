package domain_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/evals/domain"
)

func sp(s string) *string { return &s }
func ip(n int) *int       { return &n }

func ruleErr(t *testing.T, err error) *domain.RuleError {
	t.Helper()
	var re *domain.RuleError
	if !errors.As(err, &re) {
		t.Fatalf("error = %v, want *domain.RuleError", err)
	}
	return re
}

func TestBuildSingleFileDiff(t *testing.T) {
	got := domain.BuildSingleFileDiff("a/b.go", "@@ -1 +1 @@\n-x\n+y")
	want := "diff --git a/a/b.go b/a/b.go\n--- a/a/b.go\n+++ b/a/b.go\n@@ -1 +1 @@\n-x\n+y"
	if got != want {
		t.Errorf("BuildSingleFileDiff = %q, want %q", got, want)
	}
}

func TestCaseName(t *testing.T) {
	tests := []struct {
		name, title, file string
		start             int
		want              string
	}{
		{"title is trimmed", "  SQL injection \n", "a.go", 3, "SQL injection"},
		{"blank title falls back to location", " \t\u00A0\uFEFF", "src/a.go", 12, "src/a.go:12"},
		{"cut at 120 units", strings.Repeat("a", 130), "a.go", 1, strings.Repeat("a", 120)},
		{"cut counts utf-16 units", strings.Repeat("😀", 70), "a.go", 1, strings.Repeat("😀", 60)},
		{"cut inside a pair keeps U+FFFD", "a" + strings.Repeat("😀", 70), "a.go", 1, "a" + strings.Repeat("😀", 59) + "\uFFFD"},
		{"location is cut too", "", strings.Repeat("f", 150), 7, strings.Repeat("f", 120)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := domain.CaseName(tc.title, tc.file, tc.start); got != tc.want {
				t.Errorf("CaseName(%q, %q, %d) = %q, want %q", tc.title, tc.file, tc.start, got, tc.want)
			}
		})
	}
}

func finding() domain.FindingSource {
	acc := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	return domain.FindingSource{
		FindingID: "f1", ReviewID: "r1", PRID: "p1", AgentID: sp("ag1"),
		AcceptedAt: &acc,
		File:       "src/a.go", StartLine: 10, EndLine: 12,
		Severity: "high", Category: "bug", Title: "  Nil deref ",
		Patch:   sp("@@ -1,2 +1,2 @@\n a\n-b\n+c"),
		PRTitle: "Fix", PRBody: sp("body"), RepoFullName: "o/r", PRNumber: 7, HeadSHA: "abc",
	}
}

func TestBuildFromFinding(t *testing.T) {
	got, err := domain.BuildFromFinding("ws", finding())
	if err != nil {
		t.Fatalf("BuildFromFinding: %v", err)
	}
	want := domain.NewCase{
		WorkspaceID: "ws", OwnerKind: domain.OwnerAgent, OwnerID: "ag1", Name: "Nil deref",
		Expectation: domain.Expectation{
			Kind: domain.MustFind, File: "src/a.go", StartLine: 10, EndLine: 12,
			Severity: sp("high"), Category: sp("bug"), Title: sp("  Nil deref "),
		},
		InputDiff: "diff --git a/src/a.go b/src/a.go\n--- a/src/a.go\n+++ b/src/a.go\n@@ -1,2 +1,2 @@\n a\n-b\n+c",
		InputMeta: domain.InputMeta{
			PRTitle: "Fix", PRDescription: sp("body"), Repo: sp("o/r"), PRNumber: ip(7), HeadSHA: sp("abc"),
		},
		SourceFindingID: sp("f1"), SourceReviewID: sp("r1"), SourcePRID: sp("p1"),
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("BuildFromFinding mismatch (-want +got):\n%s", diff)
	}
}

func TestBuildFromFinding_Variants(t *testing.T) {
	dismissed := time.Now()
	tests := []struct {
		name     string
		mutate   func(*domain.FindingSource)
		wantKind domain.ExpectationKind
		wantEnd  int
	}{
		{"dismissed is must_not_flag", func(s *domain.FindingSource) { s.AcceptedAt = nil; s.DismissedAt = &dismissed }, domain.MustNotFlag, 12},
		{"accepted wins over dismissed", func(s *domain.FindingSource) { s.DismissedAt = &dismissed }, domain.MustFind, 12},
		{"end below start is raised", func(s *domain.FindingSource) { s.EndLine = 3 }, domain.MustFind, 10},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			src := finding()
			tc.mutate(&src)
			got, err := domain.BuildFromFinding("ws", src)
			if err != nil {
				t.Fatalf("BuildFromFinding: %v", err)
			}
			if got.Expectation.Kind != tc.wantKind || got.Expectation.EndLine != tc.wantEnd {
				t.Errorf("kind, end = %s, %d, want %s, %d", got.Expectation.Kind, got.Expectation.EndLine, tc.wantKind, tc.wantEnd)
			}
		})
	}
}

func TestBuildFromFinding_Rules(t *testing.T) {
	tests := []struct {
		name     string
		mutate   func(*domain.FindingSource)
		wantCode string
		wantMsg  string
	}{
		{"undecided", func(s *domain.FindingSource) { s.AcceptedAt = nil }, "finding_undecided", "Accept or dismiss the finding first"},
		{"no agent", func(s *domain.FindingSource) { s.AgentID = nil }, "finding_no_agent", "The review of this finding has no agent"},
		{"undecided is checked before the agent", func(s *domain.FindingSource) { s.AcceptedAt = nil; s.AgentID = nil }, "finding_undecided", "Accept or dismiss the finding first"},
		{"nil patch", func(s *domain.FindingSource) { s.Patch = nil }, "patch_unavailable", "The PR has no stored patch for this file"},
		{"blank patch", func(s *domain.FindingSource) { s.Patch = sp(" \n\u2028\t") }, "patch_unavailable", "The PR has no stored patch for this file"},
		{"patch of 200001 units", func(s *domain.FindingSource) { s.Patch = sp(strings.Repeat("x", 200_001)) }, "patch_too_large", "The patch exceeds 200000 characters"},
		{"patch length counts utf-16 units", func(s *domain.FindingSource) { s.Patch = sp(strings.Repeat("😀", 100_001)) }, "patch_too_large", "The patch exceeds 200000 characters"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			src := finding()
			tc.mutate(&src)
			_, err := domain.BuildFromFinding("ws", src)
			re := ruleErr(t, err)
			if re.Kind != domain.RuleUnprocessable || re.Code != tc.wantCode || re.Message != tc.wantMsg || re.Details != nil {
				t.Errorf("BuildFromFinding error = %+v, want 422 %s %q", re, tc.wantCode, tc.wantMsg)
			}
		})
	}

	t.Run("patch of exactly 200000 units passes", func(t *testing.T) {
		src := finding()
		src.Patch = sp(strings.Repeat("x", 200_000))
		if _, err := domain.BuildFromFinding("ws", src); err != nil {
			t.Errorf("BuildFromFinding: %v", err)
		}
	})
}

func TestBuildFromFinding_DoesNotAlias(t *testing.T) {
	src := finding()
	got, err := domain.BuildFromFinding("ws", src)
	if err != nil {
		t.Fatal(err)
	}
	*got.InputMeta.PRDescription = "changed"
	if *src.PRBody != "body" {
		t.Errorf("source PR body = %q after editing the case, want unchanged", *src.PRBody)
	}
}

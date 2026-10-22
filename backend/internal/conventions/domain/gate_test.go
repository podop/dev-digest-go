package domain_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/conventions/domain"
)

func linesFrom(files map[string][]string) func(string) ([]string, bool) {
	return func(path string) ([]string, bool) {
		l, ok := files[path]
		return l, ok
	}
}

func TestGateCandidates_DropReasons(t *testing.T) {
	files := map[string][]string{
		"src/a.ts": fileOf(20, map[int]string{10: "  export function getUser(id: string) {"}),
		"src/b.ts": fileOf(20, nil),
	}
	cands := []domain.Candidate{
		{Category: "naming", Rule: "a", Confidence: 0.9, Evidence: []domain.ClaimedEvidence{
			{Path: "src/a.ts", StartLine: 9, EndLine: 9, Snippet: "export function getUser(id: string) {"}}},
		{Category: "naming", Rule: "b", Evidence: []domain.ClaimedEvidence{
			{Path: "src/missing.ts", StartLine: 1, Snippet: "whatever it is"}}},
		{Category: "naming", Rule: "c", Evidence: []domain.ClaimedEvidence{
			{Path: "src/b.ts", StartLine: 500, Snippet: "no such text anywhere"}}},
		{Category: "naming", Rule: "d", Evidence: []domain.ClaimedEvidence{
			{Path: "src/b.ts", StartLine: 3, Snippet: "no such text anywhere"}}},
		{Category: "naming", Rule: "e", Evidence: []domain.ClaimedEvidence{
			{Path: "../etc/passwd", StartLine: 1, Snippet: "root:x:0:0"}}},
		{Category: "naming", Rule: "f"},
		{Category: "naming", Rule: "!!!", Evidence: []domain.ClaimedEvidence{{Path: "src/a.ts", StartLine: 1, Snippet: "x"}}},
	}
	got := domain.GateCandidates(cands, linesFrom(files), nil)

	wantKept := []domain.KeptConvention{{
		Category: "naming", Rule: "a", Confidence: 0.9,
		Evidence: []domain.Evidence{{Path: "src/a.ts", StartLine: 10, EndLine: 10, Snippet: "export function getUser(id: string) {"}},
	}}
	if diff := cmp.Diff(wantKept, got.Kept); diff != "" {
		t.Errorf("GateCandidates kept mismatch (-want +got):\n%s", diff)
	}
	wantDropped := []domain.Dropped{
		{Rule: "b", Path: "src/missing.ts", Reason: domain.ReasonFileNotFound},
		{Rule: "c", Path: "src/b.ts", Reason: domain.ReasonLineOutOfRange},
		{Rule: "d", Path: "src/b.ts", Reason: domain.ReasonSnippetMismatch},
		{Rule: "e", Path: "../etc/passwd", Reason: domain.ReasonFileNotFound},
		{Rule: "f", Path: "", Reason: domain.ReasonInvalid},
		{Rule: "!!!", Path: "src/a.ts", Reason: domain.ReasonInvalid},
	}
	if diff := cmp.Diff(wantDropped, got.Dropped); diff != "" {
		t.Errorf("GateCandidates dropped mismatch (-want +got):\n%s", diff)
	}
}

func TestGateCandidates_LimitsAndDuplicates(t *testing.T) {
	lines := []string{"the one line everybody cites"}
	files := map[string][]string{"a.ts": lines}
	ev := []domain.ClaimedEvidence{{Path: "a.ts", StartLine: 1, Snippet: "the one line everybody cites"}}

	var cands []domain.Candidate
	for i := range 20 {
		cands = append(cands, domain.Candidate{Category: "style", Rule: fmt.Sprintf("Rule number %d", i), Confidence: 0.5, Evidence: ev})
	}
	cands[1].Rule = "RULE number 0!" // same key as candidate 0
	cands[2].Rule = "Already accepted rule"
	cands[3].Confidence = 1.7
	cands[4].Rule = "  spaced \n  out\trule " + strings.Repeat("y", 600)

	known := map[string]struct{}{domain.RuleKey("already accepted, RULE"): {}}
	got := domain.GateCandidates(cands, linesFrom(files), known)

	if n := len(got.Kept) + len(got.Dropped); n != 15 {
		t.Errorf("GateCandidates considered %d candidates, want 15", n)
	}
	if diff := cmp.Diff([]domain.Dropped{
		{Rule: "RULE number 0!", Path: "a.ts", Reason: domain.ReasonDuplicate},
		{Rule: "Already accepted rule", Path: "a.ts", Reason: domain.ReasonDuplicate},
	}, got.Dropped); diff != "" {
		t.Errorf("GateCandidates dropped mismatch (-want +got):\n%s", diff)
	}
	if got.Kept[1].Confidence != 1 {
		t.Errorf("GateCandidates confidence of 1.7 = %v, want 1", got.Kept[1].Confidence)
	}
	rule := got.Kept[2].Rule
	if !strings.HasPrefix(rule, "spaced out rule yyy") || len(rule) != 500 {
		t.Errorf("GateCandidates rule = %q (len %d), want collapsed and cut to 500", rule, len(rule))
	}
}

func TestGateCandidates_EvidenceHandling(t *testing.T) {
	files := map[string][]string{"a.ts": {"alpha line one", "beta line two", "gamma line three", "delta line four"}}
	cands := []domain.Candidate{{
		Category: "api", Rule: "r", Confidence: 0.4,
		Evidence: []domain.ClaimedEvidence{
			{Path: "a.ts", StartLine: 1, Snippet: "alpha line one"},
			{Path: "./a.ts", StartLine: 1, Snippet: "alpha line one"}, // same place: collapses
			{Path: "a.ts", StartLine: 1, Snippet: "nope nope nope"},
			{Path: "a.ts", StartLine: 4, Snippet: "delta line four"}, // 4th item: ignored
		},
	}}
	got := domain.GateCandidates(cands, linesFrom(files), nil)
	want := []domain.Evidence{{Path: "a.ts", StartLine: 1, EndLine: 1, Snippet: "alpha line one"}}
	if len(got.Kept) != 1 {
		t.Fatalf("GateCandidates kept %d, want 1 (dropped %v)", len(got.Kept), got.Dropped)
	}
	if diff := cmp.Diff(want, got.Kept[0].Evidence); diff != "" {
		t.Errorf("GateCandidates evidence mismatch (-want +got):\n%s", diff)
	}
}

func TestCitedPaths(t *testing.T) {
	cands := []domain.Candidate{
		{Evidence: []domain.ClaimedEvidence{{Path: "./a.ts"}, {Path: "../x"}, {Path: "b.ts"}, {Path: "d.ts"}}},
		{Evidence: []domain.ClaimedEvidence{{Path: "a.ts"}, {Path: "c.ts"}}},
	}
	want := []string{"a.ts", "b.ts", "c.ts"}
	if diff := cmp.Diff(want, domain.CitedPaths(cands)); diff != "" {
		t.Errorf("CitedPaths mismatch (-want +got):\n%s", diff)
	}
}

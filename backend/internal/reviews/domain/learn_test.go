package domain_test

import (
	"testing"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/reviews/domain"
)

func TestLearnedMemoryContent(t *testing.T) {
	tests := []struct {
		name string
		f    domain.FindingRecord
		want string
	}{
		{"with suggestion", domain.FindingRecord{Title: "T", Rationale: "R", Suggestion: ptr("Do X")}, "T\n\nR\n\nSuggested fix: Do X"},
		{"without suggestion", domain.FindingRecord{Title: "T", Rationale: "R"}, "T\n\nR"},
		{"empty suggestion is none", domain.FindingRecord{Title: "T", Rationale: "R", Suggestion: ptr("")}, "T\n\nR"},
		{"multi-line texts are kept", domain.FindingRecord{Title: "T", Rationale: "R1\nR2", Suggestion: ptr("S1\nS2")}, "T\n\nR1\nR2\n\nSuggested fix: S1\nS2"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := domain.LearnedMemoryContent(tc.f); got != tc.want {
				t.Errorf("LearnedMemoryContent() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestLearnKeys(t *testing.T) {
	if got, want := domain.LearnContext("f1"), "finding:f1"; got != want {
		t.Errorf("LearnContext() = %q, want %q", got, want)
	}
	if got, want := domain.LearnLockKey("f1"), "learn:f1"; got != want {
		t.Errorf("LearnLockKey() = %q, want %q", got, want)
	}
}

func TestNewLearnMemory(t *testing.T) {
	target := domain.LearnTarget{
		Finding:  domain.FindingRecord{ID: "f1", Title: "T", Rationale: "R", Confidence: 0.9},
		RepoID:   "repo1",
		PRNumber: 482,
	}
	want := domain.NewMemory{WorkspaceID: "w1", RepoID: "repo1", FindingID: "f1", PRNumber: 482, Content: "T\n\nR", Confidence: 0.9}
	if diff := cmp.Diff(want, domain.NewLearnMemory("w1", target)); diff != "" {
		t.Errorf("NewLearnMemory() mismatch (-want +got):\n%s", diff)
	}
}

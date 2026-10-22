package domain_test

import (
	"testing"
	"time"

	"dev-digest-go/backend/internal/onboarding/domain"
)

func TestStaleness_EqualsNode(t *testing.T) {
	for i, c := range loadCases(t).Stale {
		cur := ""
		if c.CurrentSha != nil {
			cur = *c.CurrentSha
		}
		got := domain.Staleness(c.StoredSha, c.StoredPromptVersion, cur)
		want := domain.StaleReason(c.Out.Reason)
		if got != want || (got != domain.StaleNone) != c.Out.Stale {
			t.Errorf("case %d: Staleness(%q, %d, %q) = %q, want %q (stale %v)", i, c.StoredSha, c.StoredPromptVersion, cur, got, want, c.Out.Stale)
		}
	}
}

func TestStateOf(t *testing.T) {
	tour := domain.Tour{IndexedSHA: "abc", PromptVersion: domain.PromptVersion, GeneratedAt: time.Unix(0, 0)}
	tests := []struct {
		name       string
		mod        func(*domain.Tour)
		current    string
		wantStale  bool
		wantReason domain.StaleReason
	}{
		{"same commit and prompt", func(*domain.Tour) {}, "abc", false, domain.StaleNone},
		{"new commit", func(*domain.Tour) {}, "def", true, domain.StaleIndexChanged},
		{"new commit wins over old prompt", func(t *domain.Tour) { t.PromptVersion = 0 }, "def", true, domain.StaleIndexChanged},
		{"prompt version 0", func(t *domain.Tour) { t.PromptVersion = 0 }, "abc", true, domain.StalePromptChanged},
		{"no current commit is a change, as Node's null", func(*domain.Tour) {}, "", true, domain.StaleIndexChanged},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			stored := tour
			tc.mod(&stored)
			got := domain.StateOf(stored, tc.current)
			if got.Tour == nil || got.Tour.IndexedSHA != "abc" || got.Stale != tc.wantStale || got.StaleReason != tc.wantReason {
				t.Errorf("StateOf(.., %q) = %+v, want stale %v reason %q", tc.current, got, tc.wantStale, tc.wantReason)
			}
		})
	}
}

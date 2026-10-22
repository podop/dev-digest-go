package domain_test

import (
	"strings"
	"testing"

	"dev-digest-go/backend/internal/skills/domain"
)

func TestSlugify(t *testing.T) {
	tests := []struct {
		name, in, want string
	}{
		{"spaces and case", "My Skill", "my-skill"},
		{"extension dropped", "Review.MD", "review"},
		{"zip extension dropped", "pack.zip", "pack"},
		{"accents folded", "Crème Brûlée", "creme-brulee"},
		{"compatibility forms", "ﬁnal ＡＢＣ", "final-abc"},
		{"runs collapse", "a___b...c", "a-b-c"},
		{"edges trimmed", "--x--", "x"},
		{"empty", "", "imported-skill"},
		{"only symbols", "!!!", "imported-skill"},
		{"non-latin only", "技能", "imported-skill"},
		{"cut at 64", strings.Repeat("a", 70), strings.Repeat("a", 64)},
		{"cut then trailing dashes trimmed", strings.Repeat("a", 63) + "-b", strings.Repeat("a", 63)},
		{"already a slug", "no-then-chains", "no-then-chains"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := domain.Slugify(tc.in); got != tc.want {
				t.Errorf("Slugify(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

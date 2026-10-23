package domain_test

import (
	"testing"

	"dev-digest-go/backend/internal/reviewer/domain"
)

func TestSkillBlock(t *testing.T) {
	bom, nel := string(rune(0xFEFF)), string(rune(0x85))
	tests := []struct {
		name, skill, desc, body, want string
	}{
		{"blank description", "n1", "  ", " body ", "### n1\n\nbody"},
		{"description", "n2", "\n When X \t", "\nbody\n", "### n2\n_Applies when:_ When X\n\nbody"},
		{"BOM is trimmed like JavaScript", "n3", bom, bom + "b" + bom, "### n3\n\nb"},
		{"NEL is not trimmed like JavaScript", "n4", "", nel + "b", "### n4\n\n" + nel + "b"},
		{"empty body", "n5", "", "", "### n5\n\n"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := domain.SkillBlock(tc.skill, tc.desc, tc.body); got != tc.want {
				t.Errorf("SkillBlock(%q, %q, %q) = %q, want %q", tc.skill, tc.desc, tc.body, got, tc.want)
			}
		})
	}
}

package domain_test

import (
	"strings"
	"testing"

	"dev-digest-go/backend/internal/ci/domain"
)

func TestSlugify(t *testing.T) {
	tests := []struct {
		name, in, fallback, want string
	}{
		{"plain", "Security Reviewer", "agent", "security-reviewer"},
		{"accents", "Ünïcödé Revïewer", "agent", "unicode-reviewer"},
		{"cjk gives fallback", "日本語", "agent", "agent"},
		{"skill fallback", "***", "skill", "skill"},
		{"empty", "", "agent", "agent"},
		{"leading and trailing symbols", "  --Go / Lint!! ", "agent", "go-lint"},
		{"runs collapse", "a___b   c", "agent", "a-b-c"},
		{"digits first", "3d Review", "agent", "3d-review"},
		{"fullwidth folds under NFKD", "ＡＢＣ 123", "agent", "abc-123"},
		{"dotted capital I", "İstanbul", "agent", "istanbul"},
		{"kelvin sign", "Kelvin", "agent", "kelvin"},
		{"cut at 40 then trailing dash stripped", strings.Repeat("a", 39) + " bbb", "agent", strings.Repeat("a", 39)},
		{"cut at 40 exact", strings.Repeat("a", 45), "agent", strings.Repeat("a", 40)},
		{"error handling", "Error handling", "skill", "error-handling"},
		{"invalid utf-8", "a\xffb", "agent", "a-b"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := domain.Slugify(tc.in, tc.fallback)
			if got != tc.want {
				t.Errorf("Slugify(%q, %q) = %q, want %q", tc.in, tc.fallback, got, tc.want)
			}
			if got != tc.fallback && !domain.ValidSlug(got) {
				t.Errorf("Slugify(%q) = %q does not match the slug shape", tc.in, got)
			}
		})
	}
}

func TestAgentAndSkillSlugFallbacks(t *testing.T) {
	if got := domain.AgentSlug("日本語"); got != "agent" {
		t.Errorf("AgentSlug(日本語) = %q, want agent", got)
	}
	if got := domain.SkillSlug("!!!"); got != "skill" {
		t.Errorf("SkillSlug(!!!) = %q, want skill", got)
	}
}

func TestValidSlug(t *testing.T) {
	tests := []struct {
		in   string
		want bool
	}{
		{"a", true},
		{"security-reviewer", true},
		{"0", true},
		{strings.Repeat("a", 40), true},
		{strings.Repeat("a", 41), false},
		{"", false},
		{"-a", false},
		{"A", false},
		{"a_b", false},
		{"a\n", false},
	}
	for _, tc := range tests {
		if got := domain.ValidSlug(tc.in); got != tc.want {
			t.Errorf("ValidSlug(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestPathsAndNames(t *testing.T) {
	const slug = "security-reviewer"
	checks := []struct{ name, got, want string }{
		{"ManifestPath", domain.ManifestPath(slug), ".devdigest/agents/security-reviewer.yaml"},
		{"SkillPath", domain.SkillPath("error-handling"), ".devdigest/skills/error-handling.md"},
		{"WorkflowFile", domain.WorkflowFile(slug), "devdigest-review-security-reviewer.yml"},
		{"WorkflowPath", domain.WorkflowPath(slug), ".github/workflows/devdigest-review-security-reviewer.yml"},
		{"CheckName", domain.CheckName(slug), "DevDigest Review · security-reviewer"},
		{"ArtifactName", domain.ArtifactName(slug), "devdigest-result-security-reviewer"},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s = %q, want %q", c.name, c.got, c.want)
		}
	}
}

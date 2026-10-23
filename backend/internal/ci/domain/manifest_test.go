package domain_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"gopkg.in/yaml.v3"

	"dev-digest-go/backend/internal/ci/domain"
)

func exportAgent() domain.ExportAgent {
	return domain.ExportAgent{
		ID: "a1", Name: "Security Reviewer", Provider: "openrouter", Model: "deepseek/deepseek-chat",
		SystemPrompt: "Review the diff.", Strategy: "auto", CIFailOn: "critical", Version: 3,
	}
}

type parsedManifest struct {
	Name         string   `yaml:"name"`
	Provider     string   `yaml:"provider"`
	Model        string   `yaml:"model"`
	SystemPrompt string   `yaml:"system_prompt"`
	Skills       []string `yaml:"skills"`
	Strategy     string   `yaml:"strategy"`
	CIFailOn     string   `yaml:"ci_fail_on"`
	Version      int      `yaml:"version"`
}

func TestBuildManifest_Valid(t *testing.T) {
	m, err := domain.BuildManifest(exportAgent(), []string{"error-handling"})
	if err != nil {
		t.Fatalf("BuildManifest() error = %v", err)
	}
	want := domain.Manifest{
		Name: "Security Reviewer", Provider: "openrouter", Model: "deepseek/deepseek-chat",
		SystemPrompt: "Review the diff.", Skills: []string{"error-handling"},
		Strategy: "auto", CIFailOn: "critical", Version: 3,
	}
	if diff := cmp.Diff(want, m); diff != "" {
		t.Errorf("BuildManifest() mismatch (-want +got):\n%s", diff)
	}
}

func TestBuildManifest_Invalid(t *testing.T) {
	a := exportAgent()
	a.Name, a.Provider, a.Model, a.Strategy, a.CIFailOn = "", "gemini", "", "", "sometimes"
	_, err := domain.BuildManifest(a, nil)
	var me *domain.ManifestError
	if !errors.As(err, &me) {
		t.Fatalf("BuildManifest() error = %v, want *ManifestError", err)
	}
	want := []domain.ManifestIssue{
		{Path: "name", Message: "String must contain at least 1 character(s)"},
		{Path: "provider", Message: "Invalid enum value. Expected 'openai' | 'anthropic' | 'openrouter', received 'gemini'"},
		{Path: "model", Message: "String must contain at least 1 character(s)"},
		{Path: "strategy", Message: "Invalid enum value. Expected 'auto' | 'single-pass' | 'map-reduce', received ''"},
		{Path: "ci_fail_on", Message: "Invalid enum value. Expected 'never' | 'critical' | 'warning' | 'any', received 'sometimes'"},
	}
	if diff := cmp.Diff(want, me.Issues); diff != "" {
		t.Errorf("BuildManifest() issues mismatch (-want +got):\n%s", diff)
	}
}

func TestManifestYAML_KeyOrderAndEmptySkills(t *testing.T) {
	m, err := domain.BuildManifest(exportAgent(), nil)
	if err != nil {
		t.Fatal(err)
	}
	want := `name: "Security Reviewer"
provider: "openrouter"
model: "deepseek/deepseek-chat"
system_prompt: "Review the diff."
skills: []
strategy: "auto"
ci_fail_on: "critical"
version: 3
`
	if got := m.YAML(); got != want {
		t.Errorf("YAML() = %q, want %q", got, want)
	}
}

func TestManifestYAML_RoundTrip(t *testing.T) {
	prompts := map[string]string{
		"newlines":         "line one\nline two\n\nline four\n",
		"leading spaces":   "   indented\n  more",
		"trailing spaces":  "ends with spaces   ",
		"hash":             "# not a comment\nvalue # nor this",
		"colon":            "key: value\n- item\n? q",
		"quotes":           `he said "hi" and 'bye' \ done`,
		"tabs":             "a\tb\t\tc",
		"crlf":             "one\r\ntwo\r\n",
		"emoji":            "ok 👍 café ☕ 日本語",
		"controls":         "bell\x07 esc\x1b nul-free del\x7f",
		"c1 and nel":       "a\u0085b\u0080c\u009fd",
		"line separators":  "a\u2028b\u2029c",
		"bom":              "\ufeffstarts with a BOM",
		"noncharacters":    "x\ufffey\uffffz",
		"yaml-looking":     "true",
		"null-looking":     "null",
		"number-looking":   "0123",
		"empty":            "",
		"block-ish":        "|\n  folded >\n",
		"flow-ish":         "[a, b]: {c}",
		"anchor-ish":       "&a *b !!str !tag",
		"percent and dash": "% --- ... ",
	}
	for name, prompt := range prompts {
		t.Run(name, func(t *testing.T) {
			a := exportAgent()
			a.Name, a.SystemPrompt = prompt+"x", prompt
			skills := []string{"one", "two-" + strings.ToLower(name[:1])}
			m, err := domain.BuildManifest(a, skills)
			if err != nil {
				t.Fatal(err)
			}
			var got parsedManifest
			if err := yaml.Unmarshal([]byte(m.YAML()), &got); err != nil {
				t.Fatalf("yaml.Unmarshal(%q) error = %v", m.YAML(), err)
			}
			want := parsedManifest{
				Name: a.Name, Provider: a.Provider, Model: a.Model, SystemPrompt: prompt, Skills: skills,
				Strategy: a.Strategy, CIFailOn: a.CIFailOn, Version: a.Version,
			}
			if diff := cmp.Diff(want, got); diff != "" {
				t.Errorf("parsed manifest mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestManifestYAML_InvalidUTF8BecomesReplacement(t *testing.T) {
	a := exportAgent()
	a.SystemPrompt = "a\xffb"
	m, err := domain.BuildManifest(a, nil)
	if err != nil {
		t.Fatal(err)
	}
	var got parsedManifest
	if err := yaml.Unmarshal([]byte(m.YAML()), &got); err != nil {
		t.Fatal(err)
	}
	if want := "a�b"; got.SystemPrompt != want {
		t.Errorf("system_prompt = %q, want %q", got.SystemPrompt, want)
	}
}

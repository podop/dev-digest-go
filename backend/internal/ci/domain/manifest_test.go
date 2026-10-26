package domain_test

import (
	"errors"
	"math"
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

func strPtr(s string) *string { return &s }

func customAgent() domain.ExportAgent {
	a := exportAgent()
	a.Provider, a.Model = "custom:my-gw", "gpt-4.1-mini"
	a.Custom = &domain.CustomProviderConfig{
		Protocol: "openai", AuthMode: "bearer",
		ExtraHeaders:     []string{"X-Tenant", "X-Project"},
		StructuredOutput: "strict",
	}
	a.Pricing = &domain.Pricing{PromptPerM: 0.15, CompletionPerM: 0.6}
	return a
}

const manifestHead = `name: "Security Reviewer"
provider: "%s"
model: "%s"
system_prompt: "Review the diff."
skills: []
strategy: "auto"
ci_fail_on: "critical"
version: 3
`

func manifestText(t *testing.T, a domain.ExportAgent) string {
	t.Helper()
	m, err := domain.BuildManifest(a, nil)
	if err != nil {
		t.Fatalf("BuildManifest() error = %v", err)
	}
	return m.YAML()
}

func fillHead(provider, model string) string {
	return strings.Replace(strings.Replace(manifestHead, "%s", provider, 1), "%s", model, 1)
}

// FR27, AC30, EC25: custom_provider follows version, keys in order, pricing last.
func TestManifestYAML_CustomProvider(t *testing.T) {
	want := fillHead("custom:my-gw", "gpt-4.1-mini") + `custom_provider:
  protocol: "openai"
  auth_mode: "bearer"
  auth_header: null
  extra_headers:
    - "X-Tenant"
    - "X-Project"
  structured_output: "strict"
  pricing:
    promptPerM: 0.15
    completionPerM: 0.6
`
	if got := manifestText(t, customAgent()); got != want {
		t.Errorf("YAML() mismatch:\ngot:\n%s\nwant:\n%s", got, want)
	}
}

func TestManifestYAML_CustomProviderVariants(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(a *domain.ExportAgent)
		want   string
	}{
		{
			"anthropic header auth, no headers, no price",
			func(a *domain.ExportAgent) {
				a.Pricing = nil
				a.Custom = &domain.CustomProviderConfig{
					Protocol: "anthropic", AuthMode: "header", AuthHeader: strPtr("x-api-key"), StructuredOutput: "prompt_only",
				}
			},
			`custom_provider:
  protocol: "anthropic"
  auth_mode: "header"
  auth_header: "x-api-key"
  extra_headers: []
  structured_output: "prompt_only"
  pricing: null
`,
		},
		{
			"json_object with five headers (EC25)",
			func(a *domain.ExportAgent) {
				a.Custom.StructuredOutput = "json_object"
				a.Custom.ExtraHeaders = []string{"H1", "H2", "H3", "H4", "H5"}
				a.Pricing = &domain.Pricing{PromptPerM: 2, CompletionPerM: 8}
			},
			`custom_provider:
  protocol: "openai"
  auth_mode: "bearer"
  auth_header: null
  extra_headers:
    - "H1"
    - "H2"
    - "H3"
    - "H4"
    - "H5"
  structured_output: "json_object"
  pricing:
    promptPerM: 2
    completionPerM: 8
`,
		},
		{
			"bearer drops a stray auth header",
			func(a *domain.ExportAgent) { a.Custom.AuthHeader = strPtr("x-leak"); a.Custom.ExtraHeaders = nil },
			`custom_provider:
  protocol: "openai"
  auth_mode: "bearer"
  auth_header: null
  extra_headers: []
  structured_output: "strict"
  pricing:
    promptPerM: 0.15
    completionPerM: 0.6
`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			a := customAgent()
			tc.mutate(&a)
			want := fillHead("custom:my-gw", "gpt-4.1-mini") + tc.want
			if got := manifestText(t, a); got != want {
				t.Errorf("YAML() mismatch:\ngot:\n%s\nwant:\n%s", got, want)
			}
		})
	}
}

type parsedCustomManifest struct {
	parsedManifest `yaml:",inline"`
	CustomProvider *struct {
		Protocol         string   `yaml:"protocol"`
		AuthMode         string   `yaml:"auth_mode"`
		AuthHeader       *string  `yaml:"auth_header"`
		ExtraHeaders     []string `yaml:"extra_headers"`
		StructuredOutput string   `yaml:"structured_output"`
		Pricing          *struct {
			PromptPerM     float64 `yaml:"promptPerM"`
			CompletionPerM float64 `yaml:"completionPerM"`
		} `yaml:"pricing"`
	} `yaml:"custom_provider"`
	Pricing *struct {
		PromptPerM     float64 `yaml:"promptPerM"`
		CompletionPerM float64 `yaml:"completionPerM"`
	} `yaml:"pricing"`
}

// The YAML parses back to the values (hostile header names included), and never holds anything a
// custom provider keeps private.
func TestManifestYAML_CustomProviderRoundTrip(t *testing.T) {
	a := customAgent()
	a.Custom.AuthMode, a.Custom.AuthHeader = "header", strPtr("X-Key: \"1\"\n#")
	a.Custom.ExtraHeaders = []string{"Header: one", "a\tb", "日本"}
	var got parsedCustomManifest
	text := manifestText(t, a)
	if err := yaml.Unmarshal([]byte(text), &got); err != nil {
		t.Fatalf("yaml.Unmarshal(%q) error = %v", text, err)
	}
	if got.CustomProvider == nil || got.Pricing != nil {
		t.Fatalf("parsed = %+v, want custom_provider and no top-level pricing", got)
	}
	c := got.CustomProvider
	if c.AuthHeader == nil || *c.AuthHeader != *a.Custom.AuthHeader {
		t.Errorf("auth_header = %v, want %q", c.AuthHeader, *a.Custom.AuthHeader)
	}
	if diff := cmp.Diff(a.Custom.ExtraHeaders, c.ExtraHeaders); diff != "" {
		t.Errorf("extra_headers mismatch (-want +got):\n%s", diff)
	}
	if c.Pricing == nil || c.Pricing.PromptPerM != 0.15 || c.Pricing.CompletionPerM != 0.6 {
		t.Errorf("custom_provider.pricing = %+v, want 0.15 / 0.6", c.Pricing)
	}
}

// FR35, AC45, EC36: openai / anthropic carry a top-level pricing after version; a missing price
// omits the key; custom_provider is never there.
func TestManifestYAML_BuiltinPricing(t *testing.T) {
	for _, provider := range []string{"openai", "anthropic"} {
		t.Run(provider, func(t *testing.T) {
			a := exportAgent()
			a.Provider, a.Model = provider, "m-1"
			a.Custom = &domain.CustomProviderConfig{Protocol: "openai", AuthMode: "bearer", StructuredOutput: "strict"}
			a.Pricing = &domain.Pricing{PromptPerM: 3, CompletionPerM: 15}
			want := fillHead(provider, "m-1") + "pricing:\n  promptPerM: 3\n  completionPerM: 15\n"
			if got := manifestText(t, a); got != want {
				t.Errorf("YAML() mismatch:\ngot:\n%s\nwant:\n%s", got, want)
			}
			a.Pricing = nil
			if got, want := manifestText(t, a), fillHead(provider, "m-1"); got != want {
				t.Errorf("YAML() without a price mismatch:\ngot:\n%s\nwant:\n%s", got, want)
			}
		})
	}
}

// P12, NFR11: an openrouter manifest never gets pricing or custom_provider, whatever the agent
// struct holds.
func TestManifestYAML_OpenRouterUnchanged(t *testing.T) {
	a := exportAgent()
	a.Pricing = &domain.Pricing{PromptPerM: 1, CompletionPerM: 2}
	a.Custom = &domain.CustomProviderConfig{Protocol: "openai", AuthMode: "bearer", StructuredOutput: "strict"}
	want := fillHead("openrouter", "deepseek/deepseek-chat")
	if got := manifestText(t, a); got != want {
		t.Errorf("YAML() mismatch:\ngot:\n%s\nwant:\n%s", got, want)
	}
	m, err := domain.BuildManifest(a, nil)
	if err != nil {
		t.Fatal(err)
	}
	if m.Pricing != nil || m.CustomProvider != nil {
		t.Errorf("BuildManifest(openrouter) = %+v, want no pricing and no custom_provider", m)
	}
}

func TestBuildManifest_CustomInvalid(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(a *domain.ExportAgent)
		want   []domain.ManifestIssue
	}{
		{
			"malformed custom id", func(a *domain.ExportAgent) { a.Provider = "custom:Bad_Slug" },
			[]domain.ManifestIssue{{Path: "provider", Message: "Invalid enum value. Expected 'openai' | 'anthropic' | 'openrouter', received 'custom:Bad_Slug'"}},
		},
		{
			"no definition", func(a *domain.ExportAgent) { a.Custom = nil },
			[]domain.ManifestIssue{{Path: "custom_provider", Message: "Required"}},
		},
		{
			"bad enums", func(a *domain.ExportAgent) {
				a.Custom.Protocol, a.Custom.AuthMode, a.Custom.StructuredOutput = "grpc", "basic", "loose"
			},
			[]domain.ManifestIssue{
				{Path: "custom_provider.protocol", Message: "Invalid enum value. Expected 'openai' | 'anthropic', received 'grpc'"},
				{Path: "custom_provider.auth_mode", Message: "Invalid enum value. Expected 'bearer' | 'header', received 'basic'"},
				{Path: "custom_provider.structured_output", Message: "Invalid enum value. Expected 'strict' | 'json_object' | 'prompt_only', received 'loose'"},
			},
		},
		{
			"header mode without a header", func(a *domain.ExportAgent) { a.Custom.AuthMode = "header" },
			[]domain.ManifestIssue{{Path: "custom_provider.auth_header", Message: "String must contain at least 1 character(s)"}},
		},
		{
			"six headers and an empty one", func(a *domain.ExportAgent) {
				a.Custom.ExtraHeaders = []string{"a", "", "c", "d", "e", "f"}
			},
			[]domain.ManifestIssue{
				{Path: "custom_provider.extra_headers", Message: "Array must contain at most 5 element(s)"},
				{Path: "custom_provider.extra_headers.1", Message: "String must contain at least 1 character(s)"},
			},
		},
		{
			"json_object on anthropic", func(a *domain.ExportAgent) {
				a.Custom.Protocol, a.Custom.StructuredOutput = "anthropic", "json_object"
			},
			[]domain.ManifestIssue{{Path: "custom_provider.structured_output", Message: "json_object needs the openai protocol"}},
		},
		{
			"negative price", func(a *domain.ExportAgent) { a.Pricing = &domain.Pricing{PromptPerM: -1} },
			[]domain.ManifestIssue{{Path: "custom_provider.pricing", Message: "Number must be finite and greater than or equal to 0"}},
		},
		{
			"NaN builtin price", func(a *domain.ExportAgent) {
				a.Provider, a.Custom = "openai", nil
				a.Pricing = &domain.Pricing{PromptPerM: math.NaN()}
			},
			[]domain.ManifestIssue{{Path: "pricing", Message: "Number must be finite and greater than or equal to 0"}},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			a := customAgent()
			tc.mutate(&a)
			_, err := domain.BuildManifest(a, nil)
			var me *domain.ManifestError
			if !errors.As(err, &me) {
				t.Fatalf("BuildManifest() error = %v, want *ManifestError", err)
			}
			if diff := cmp.Diff(tc.want, me.Issues); diff != "" {
				t.Errorf("BuildManifest() issues mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// Nothing the studio keeps private reaches the manifest: the config type has no field for it.
func TestManifestYAML_NoBaseURLOrSecrets(t *testing.T) {
	text := manifestText(t, customAgent())
	for _, forbidden := range []string{"http", "base_url", "api_key", "sk-"} {
		if strings.Contains(text, forbidden) {
			t.Errorf("YAML() contains %q:\n%s", forbidden, text)
		}
	}
}

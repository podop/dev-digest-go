package domain

import (
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"
)

// The enums of the AgentManifest contract (shared contracts, knowledge.ts), in the order their
// validation message lists them.
var (
	manifestProviders  = []string{"openai", "anthropic", "openrouter"}
	manifestStrategies = []string{"auto", "single-pass", "map-reduce"}
	manifestCIFailOn   = []string{"never", "critical", "warning", "any"}
)

// Manifest is the agent contract the studio writes to .devdigest/agents/<slug>.yaml and the CI
// runner reads. Fields are listed in the order of their YAML keys (spec 7.6.4).
type Manifest struct {
	Name         string
	Provider     string
	Model        string
	SystemPrompt string
	// Skills are the skill slugs in file order; empty when the agent has none.
	Skills   []string
	Strategy string
	CIFailOn string
	Version  int
}

// ManifestError is the failure of BuildManifest: the agent cannot be serialised as a manifest.
// Issues are in the order of the manifest keys.
type ManifestError struct {
	Issues []ManifestIssue
}

func (e *ManifestError) Error() string {
	parts := make([]string, len(e.Issues))
	for i, is := range e.Issues {
		parts[i] = is.Path + ": " + is.Message
	}
	return "invalid manifest: " + strings.Join(parts, "; ")
}

// BuildManifest builds the manifest of an agent and validates it with the AgentManifest rules
// (name and model non-empty, provider, strategy and ci_fail_on in their enums), with the texts
// of the shared Zod schema. It returns a *ManifestError listing every broken field.
func BuildManifest(agent ExportAgent, skillSlugs []string) (Manifest, error) {
	var issues []ManifestIssue
	if agent.Name == "" {
		issues = append(issues, ManifestIssue{Path: "name", Message: "String must contain at least 1 character(s)"})
	}
	if !inList(manifestProviders, agent.Provider) {
		issues = append(issues, enumIssue("provider", manifestProviders, agent.Provider))
	}
	if agent.Model == "" {
		issues = append(issues, ManifestIssue{Path: "model", Message: "String must contain at least 1 character(s)"})
	}
	if !inList(manifestStrategies, agent.Strategy) {
		issues = append(issues, enumIssue("strategy", manifestStrategies, agent.Strategy))
	}
	if !inList(manifestCIFailOn, agent.CIFailOn) {
		issues = append(issues, enumIssue("ci_fail_on", manifestCIFailOn, agent.CIFailOn))
	}
	if len(issues) > 0 {
		return Manifest{}, &ManifestError{Issues: issues}
	}
	skills := make([]string, len(skillSlugs))
	copy(skills, skillSlugs)
	return Manifest{
		Name:         agent.Name,
		Provider:     agent.Provider,
		Model:        agent.Model,
		SystemPrompt: agent.SystemPrompt,
		Skills:       skills,
		Strategy:     agent.Strategy,
		CIFailOn:     agent.CIFailOn,
		Version:      agent.Version,
	}, nil
}

func inList(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

func enumIssue(path string, allowed []string, got string) ManifestIssue {
	quoted := make([]string, len(allowed))
	for i, a := range allowed {
		quoted[i] = "'" + a + "'"
	}
	return ManifestIssue{
		Path:    path,
		Message: fmt.Sprintf("Invalid enum value. Expected %s, received '%s'", strings.Join(quoted, " | "), got),
	}
}

// YAML renders the manifest as a block mapping in the key order of spec 7.6.4. Every string value
// is a double-quoted scalar written as a JSON string literal, so any content (multi-line,
// leading or trailing spaces, '#', ': ', quotes, tabs, non-ASCII) survives a YAML 1.2 parse
// unchanged. Characters YAML would fold, drop or reject are written as \uXXXX escapes: controls,
// DEL, the C1 range (U+0085 included), U+2028, U+2029, U+FEFF, U+FFFE and U+FFFF.
func (m Manifest) YAML() string {
	var b strings.Builder
	b.WriteString("name: " + yamlString(m.Name) + "\n")
	b.WriteString("provider: " + yamlString(m.Provider) + "\n")
	b.WriteString("model: " + yamlString(m.Model) + "\n")
	b.WriteString("system_prompt: " + yamlString(m.SystemPrompt) + "\n")
	if len(m.Skills) == 0 {
		b.WriteString("skills: []\n")
	} else {
		b.WriteString("skills:\n")
		for _, s := range m.Skills {
			b.WriteString("  - " + yamlString(s) + "\n")
		}
	}
	b.WriteString("strategy: " + yamlString(m.Strategy) + "\n")
	b.WriteString("ci_fail_on: " + yamlString(m.CIFailOn) + "\n")
	b.WriteString("version: " + strconv.Itoa(m.Version) + "\n")
	return b.String()
}

// yamlString writes s as a JSON string literal with the extra escapes of Manifest.YAML. Bytes
// that are not valid UTF-8 become �.
func yamlString(s string) string {
	const hex = "0123456789abcdef"
	var b strings.Builder
	b.Grow(len(s) + 2)
	b.WriteByte('"')
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size == 1 {
			b.WriteString(`�`)
			i++
			continue
		}
		i += size
		switch {
		case r == '"':
			b.WriteString(`\"`)
		case r == '\\':
			b.WriteString(`\\`)
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\r':
			b.WriteString(`\r`)
		case r == '\t':
			b.WriteString(`\t`)
		case r == '\b':
			b.WriteString(`\b`)
		case r == '\f':
			b.WriteString(`\f`)
		case r < 0x20 || r == 0x7F || (r >= 0x80 && r <= 0x9F) ||
			r == 0x2028 || r == 0x2029 || r == 0xFEFF || r == 0xFFFE || r == 0xFFFF:
			b.WriteString(`\u`)
			b.WriteByte(hex[r>>12&0xF])
			b.WriteByte(hex[r>>8&0xF])
			b.WriteByte(hex[r>>4&0xF])
			b.WriteByte(hex[r&0xF])
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

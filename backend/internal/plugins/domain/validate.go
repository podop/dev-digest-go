package domain

import (
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"strings"
	"unicode/utf8"
)

// issues collects broken rules, the first one first, at most MaxIssues.
type issues struct{ list []string }

func (c *issues) add(format string, args ...any) {
	if len(c.list) < MaxIssues {
		c.list = append(c.list, fmt.Sprintf(format, args...))
	}
}

func (c *issues) full() bool { return len(c.list) >= MaxIssues }

// text checks a string field: valid UTF-8, no U+0000, and min to max UTF-16 code units (max < 0
// means no upper bound).
func (c *issues) text(path, s string, minLen, maxLen int) {
	switch {
	case !utf8.ValidString(s):
		c.add("%s is not valid UTF-8", path)
	case hasNUL(s):
		c.add("%s must not contain U+0000", path)
	default:
		n := UTF16Len(s)
		if n < minLen || (maxLen >= 0 && n > maxLen) {
			switch {
			case minLen > 0 && maxLen >= 0:
				c.add("%s must be %d to %d characters", path, minLen, maxLen)
			case minLen > 0:
				c.add("%s must not be empty", path)
			default:
				c.add("%s must be at most %d characters", path, maxLen)
			}
		}
	}
}

// oneOf checks an enum field.
// visibleText is text for a body or prompt that an install stores without hidden text: besides
// the length rules on the raw value, something must stay once the hidden text is removed (L2).
func (c *issues) visibleText(path, s string, minLen, maxLen int) {
	before := len(c.list)
	c.text(path, s, minLen, maxLen)
	if len(c.list) != before {
		return
	}
	if cleaned, h := stripHidden(s); h.any() && strings.TrimSpace(cleaned) == "" {
		c.add("%s has no visible text after hidden text is removed (HTML comments, zero-width or bidi characters)", path)
	}
}

func (c *issues) oneOf(path, value string, allowed []string) {
	if !slices.Contains(allowed, value) {
		c.add("%s must be one of %s (found %s)", path, quoteList(allowed), quoteShort(value))
	}
}

func quoteList(values []string) string {
	out := ""
	for i, v := range values {
		if i > 0 {
			out += ", "
		}
		out += v
	}
	return out
}

// hasNUL reports a U+0000 in a Go string.
func hasNUL(s string) bool { return strings.IndexByte(s, 0) >= 0 }

// jsonHasNUL reports whether compact-or-not JSON text holds a string (or key) with the escape
// \u0000. Escapes are scanned in context: an escaped backslash followed by "u0000" is text.
func jsonHasNUL(raw []byte) bool {
	inString := false
	for i := 0; i < len(raw); i++ {
		switch c := raw[i]; {
		case !inString:
			inString = c == '"'
		case c == '"':
			inString = false
		case c == '\\' && i+1 < len(raw):
			if raw[i+1] == 'u' && i+5 < len(raw) && string(raw[i+2:i+6]) == "0000" {
				return true
			}
			i++ // skip the escaped character
		}
	}
	return false
}

// hex4 reads the four hex digits at raw[i:i+4].
func hex4(raw []byte, i int) (rune, bool) {
	if i+4 > len(raw) {
		return 0, false
	}
	var r rune
	for _, c := range raw[i : i+4] {
		switch {
		case c >= '0' && c <= '9':
			r = r<<4 | rune(c-'0')
		case c >= 'a' && c <= 'f':
			r = r<<4 | rune(c-'a'+10)
		case c >= 'A' && c <= 'F':
			r = r<<4 | rune(c-'A'+10)
		default:
			return 0, false
		}
	}
	return r, true
}

// jsonHasLoneSurrogate reports whether JSON text holds, in a string or key, a \uD800-\uDBFF escape
// that no \uDC00-\uDFFF escape follows, or a \uDC00-\uDFFF escape that no high one precedes.
// json.Valid accepts them and the decoder turns them into U+FFFD, but a jsonb column refuses
// them (SQLSTATE 22P02), which would answer an install of a previewed bundle with a 500
// (security review r1, L1). The text is valid JSON; escapes are scanned in context.
func jsonHasLoneSurrogate(raw []byte) bool {
	inString := false
	for i := 0; i < len(raw); i++ {
		switch c := raw[i]; {
		case !inString:
			inString = c == '"'
		case c == '"':
			inString = false
		case c == '\\' && i+1 < len(raw):
			if raw[i+1] != 'u' {
				i++ // skip the escaped character
				continue
			}
			r, ok := hex4(raw, i+2)
			switch {
			case !ok:
				i++
			case r >= 0xD800 && r <= 0xDBFF:
				if i+11 >= len(raw) || raw[i+6] != '\\' || raw[i+7] != 'u' {
					return true
				}
				if lo, ok := hex4(raw, i+8); !ok || lo < 0xDC00 || lo > 0xDFFF {
					return true
				}
				i += 11 // the pair
			case r >= 0xDC00 && r <= 0xDFFF:
				return true
			default:
				i += 5 // \uXXXX
			}
		}
	}
	return false
}

// jsonField checks a null | object | array JSON field (A12) that holds no U+0000 and no unpaired
// surrogate escape.
func (c *issues) jsonField(path string, raw json.RawMessage, objectOrArrayOnly bool) {
	if len(raw) == 0 {
		return
	}
	if !json.Valid(raw) {
		c.add("%s is not valid JSON", path)
		return
	}
	if jsonHasNUL(raw) {
		c.add("%s must not contain U+0000", path)
		return
	}
	if jsonHasLoneSurrogate(raw) {
		c.add("%s must not contain an unpaired surrogate escape (\\uD800 to \\uDFFF)", path)
		return
	}
	if objectOrArrayOnly {
		switch first := firstByte(raw); first {
		case '{', '[', 'n':
		default:
			c.add("%s must be null, an object or an array", path)
		}
	}
}

func firstByte(raw []byte) byte {
	for _, b := range raw {
		switch b {
		case ' ', '\t', '\r', '\n':
		default:
			return b
		}
	}
	return 0
}

// Validate checks a bundle against the rules of spec section 7 plus the enums, defaults and
// bounds of A2, A3 and SCR4. It returns nil or an *InvalidError whose Issues list the broken rules
// in order, at most MaxIssues; the first one is the message. Strings must not contain U+0000
// (Postgres refuses it) and the JSON fields no unpaired surrogate escape. Conventions, context
// files, settings and skill versions are never installed; only their counts are checked.
func Validate(b Bundle) error {
	var c issues

	m := b.Manifest
	if m.Format != BundleFormat {
		c.add("manifest.format must be %q (found %s)", BundleFormat, quoteShort(m.Format))
	}
	if m.Kind != "" && m.Kind != KindPlugin && m.Kind != KindWorkspace {
		c.add("manifest.kind must be %q or %q (found %s)", KindPlugin, KindWorkspace, quoteShort(m.Kind))
	}
	if hasNUL(m.Name) {
		c.add("manifest.name must not contain U+0000")
	} else if _, err := NormalizeName(m.Name); err != nil {
		c.add("manifest.name must be 1 to %d characters without control characters", PluginNameMax)
	}
	c.text("manifest.version", m.Version, 0, PluginVersionMax)
	if m.Description != nil {
		c.text("manifest.description", *m.Description, 0, -1)
	}

	for _, l := range []struct {
		name  string
		n, mx int
	}{
		{"agents", len(b.Agents), MaxAgents},
		{"skills", len(b.Skills), MaxSkills},
		{"eval_cases", len(b.EvalCases), MaxEvalCases},
		{"learnings", len(b.Learnings), MaxLearnings},
	} {
		if l.n > l.mx {
			c.add("%s has %d entries; the limit is %d", l.name, l.n, l.mx)
		}
	}

	skillNames := make(map[string]struct{}, len(b.Skills))
	for i, s := range b.Skills {
		if c.full() {
			break
		}
		p := fmt.Sprintf("skills[%d]", i)
		if !kebabPattern.MatchString(s.Name) || len(s.Name) > SkillNameMax {
			c.add("%s.name must be a kebab-case slug of 1 to %d characters (lowercase letters, digits and single dashes)", p, SkillNameMax)
		}
		if _, dup := skillNames[s.Name]; dup {
			c.add("%s.name %s is used by another skill of the bundle", p, quoteShort(s.Name))
		}
		skillNames[s.Name] = struct{}{}
		c.text(p+".description", s.Description, 0, SkillDescriptionMax)
		c.visibleText(p+".body", s.Body, 1, SkillBodyMax)
		c.oneOf(p+".type", s.Type, skillTypes)
		c.oneOf(p+".source", s.Source, skillSources)
		if len(s.EvidenceFiles) > MaxEvidenceFiles {
			c.add("%s.evidence_files has %d entries; the limit is %d", p, len(s.EvidenceFiles), MaxEvidenceFiles)
		}
		if len(s.Versions) > MaxSkillVersions {
			c.add("%s.versions has %d entries; the limit is %d", p, len(s.Versions), MaxSkillVersions)
		}
		for j, f := range s.EvidenceFiles {
			if j >= MaxEvidenceFiles || c.full() {
				break
			}
			c.text(fmt.Sprintf("%s.evidence_files[%d]", p, j), f, 0, -1)
		}
	}

	agentNames := make(map[string]struct{}, len(b.Agents))
	for i, a := range b.Agents {
		if c.full() {
			break
		}
		p := fmt.Sprintf("agents[%d]", i)
		c.text(p+".name", a.Name, 1, AgentNameMax)
		if _, dup := agentNames[a.Name]; dup {
			c.add("%s.name %s is used by another agent of the bundle", p, quoteShort(a.Name))
		}
		agentNames[a.Name] = struct{}{}
		c.text(p+".description", a.Description, 0, -1)
		c.oneOf(p+".provider", a.Provider, agentProviders)
		c.text(p+".model", a.Model, 1, AgentModelMax)
		c.visibleText(p+".system_prompt", a.SystemPrompt, 1, AgentPromptMax)
		c.oneOf(p+".strategy", a.Strategy, agentStrategies)
		c.oneOf(p+".ci_fail_on", a.CIFailOn, agentCIFailOns)
		c.jsonField(p+".output_schema", a.OutputSchema, false)
		if len(a.Skills) > MaxSkills {
			c.add("%s.skills has %d entries; the limit is %d", p, len(a.Skills), MaxSkills)
			continue
		}
		linked := make(map[string]struct{}, len(a.Skills))
		for j, name := range a.Skills {
			if _, ok := skillNames[name]; !ok {
				c.add("%s.skills[%d] names the skill %s, which is not in the bundle", p, j, quoteShort(name))
			}
			if _, dup := linked[name]; dup {
				c.add("%s.skills lists %s twice", p, quoteShort(name))
			}
			linked[name] = struct{}{}
		}
	}

	type owner struct{ kind, ref string }
	perOwner := make(map[owner]int)
	for i, e := range b.EvalCases {
		if c.full() {
			break
		}
		p := fmt.Sprintf("eval_cases[%d]", i)
		c.text(p+".name", e.Name, 1, CaseNameMax)
		c.oneOf(p+".owner_kind", e.OwnerKind, caseOwnerKinds)
		known := agentNames
		if e.OwnerKind == CaseOwnerSkill {
			known = skillNames
		}
		if e.OwnerKind == CaseOwnerSkill || e.OwnerKind == CaseOwnerAgent {
			if _, ok := known[e.OwnerRef]; !ok {
				c.add("%s.owner_ref names the %s %s, which is not in the bundle", p, e.OwnerKind, quoteShort(e.OwnerRef))
			}
			k := owner{e.OwnerKind, e.OwnerRef}
			perOwner[k]++
			if perOwner[k] == CasesPerOwnerMax+1 {
				c.add("%s.owner_ref: the %s %s has more than %d eval cases", p, e.OwnerKind, quoteShort(e.OwnerRef), CasesPerOwnerMax)
			}
		}
		if e.InputDiff != nil {
			c.text(p+".input_diff", *e.InputDiff, 0, CaseDiffMax)
		}
		c.jsonField(p+".input_files", e.InputFiles, true)
		c.jsonField(p+".input_meta", e.InputMeta, true)
		c.jsonField(p+".expected_output", e.ExpectedOutput, true)
		if e.Notes != nil {
			c.text(p+".notes", *e.Notes, 0, -1)
		}
	}

	for i, l := range b.Learnings {
		if c.full() {
			break
		}
		p := fmt.Sprintf("learnings[%d]", i)
		c.oneOf(p+".scope", l.Scope, learningScopes)
		c.oneOf(p+".kind", l.Kind, learningKinds)
		c.text(p+".content", l.Content, 1, LearningContentMax)
		if l.Confidence != nil && (math.IsNaN(*l.Confidence) || *l.Confidence < 0 || *l.Confidence > 1) {
			c.add("%s.confidence must be null or between 0 and 1", p)
		}
	}

	if len(c.list) == 0 {
		return nil
	}
	return &InvalidError{Issues: c.list}
}

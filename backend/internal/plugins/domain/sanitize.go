package domain

import (
	"fmt"
	"strings"
)

// MaxHiddenTextWarnings is the most per-item hidden-text warnings a preview lists; the rest are
// summed in one last warning.
const MaxHiddenTextWarnings = MaxIssues

// stripComments removes every HTML comment and counts them. It copies skills/domain's
// stripHTMLComments: a comment runs from "<!--" to the next "-->" after it, and an unclosed one
// runs to the end. It replaces the regexp `(?s)<!--.*?(?:-->|\z)`, which costs about 450 ns per
// byte inside a comment above 17 KB of input (security review r2, N1); this loop is linear.
// cmd/server's drift test (plugin_sanitize_drift_test.go) pins SanitizeHiddenText to
// skills/domain.SanitizeMarkdown on a corpus of hostile inputs.
func stripComments(s string) (string, int) {
	i := strings.Index(s, "<!--")
	if i < 0 {
		return s, 0
	}
	var out strings.Builder
	out.Grow(len(s))
	n := 0
	for i >= 0 {
		out.WriteString(s[:i])
		n++
		rest := s[i+len("<!--"):]
		end := strings.Index(rest, "-->")
		if end < 0 {
			return out.String(), n
		}
		s = rest[end+len("-->"):]
		i = strings.Index(s, "<!--")
	}
	out.WriteString(s)
	return out.String(), n
}

func isHiddenZeroWidth(r rune) bool {
	return (r >= 0x200B && r <= 0x200D) || r == 0x2060 || r == 0xFEFF
}

func isHiddenBidi(r rune) bool {
	return r == 0x061C || r == 0x200E || r == 0x200F ||
		(r >= 0x202A && r <= 0x202E) || (r >= 0x2066 && r <= 0x2069)
}

// hidden counts what stripHidden removed.
type hidden struct{ comments, zeroWidth, bidi int }

func (h hidden) any() bool { return h.comments+h.zeroWidth+h.bidi > 0 }

// kinds names what was removed, for a preview warning.
func (h hidden) kinds() string {
	var k []string
	if h.comments > 0 {
		k = append(k, "HTML comments")
	}
	if h.zeroWidth > 0 {
		k = append(k, "zero-width characters")
	}
	if h.bidi > 0 {
		k = append(k, "bidi control characters")
	}
	return strings.Join(k, ", ")
}

func removeHiddenRunes(s string, pred func(rune) bool) (string, int) {
	n := 0
	out := strings.Map(func(r rune) rune {
		if pred(r) {
			n++
			return -1
		}
		return r
	}, s)
	return out, n
}

// stripHidden removes HTML comments, zero-width and bidi control characters and turns line ends
// into LF, exactly like skills/domain.SanitizeMarkdown (a skill import).
func stripHidden(input string) (string, hidden) {
	var h hidden
	text := strings.ReplaceAll(input, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	text, h.comments = stripComments(text)
	text, h.zeroWidth = removeHiddenRunes(text, isHiddenZeroWidth)
	text, h.bidi = removeHiddenRunes(text, isHiddenBidi)
	return text, h
}

// SanitizeHiddenText removes what a reviewer cannot see but a model would read from text that is
// stored and later sent to a model (a skill body, an agent system prompt): HTML comments,
// zero-width and bidi control characters. Line ends become LF. It returns the same text and the
// same warnings as skills/domain.SanitizeMarkdown; the plugin install and the preview use it so a
// plugin cannot smuggle in text that the user's review of it does not show (D6).
func SanitizeHiddenText(input string) (string, []string) {
	text, h := stripHidden(input)
	var warnings []string
	if h.comments > 0 {
		warnings = append(warnings, fmt.Sprintf("Removed %d HTML comment(s) (hidden text)", h.comments))
	}
	if h.zeroWidth > 0 {
		warnings = append(warnings, fmt.Sprintf("Removed %d zero-width character(s)", h.zeroWidth))
	}
	if h.bidi > 0 {
		warnings = append(warnings, fmt.Sprintf("Removed %d bidi control character(s)", h.bidi))
	}
	return text, warnings
}

// cleanTexts are the texts an install stores, with hidden text removed, and one warning per field
// that had some (at most MaxHiddenTextWarnings, then a summary). Validate, PlanInstall and Preview
// all derive from it, so the preview warns about exactly what the install removes. Cleaning is
// linear but not free on a 14 MiB bundle, so a request computes it once (Prepare).
type cleanTexts struct {
	skillBodies       []string
	skillDescriptions []string
	agentPrompts      []string
	// bodyHidden and promptHidden say what the cleanup removed, for the visible-text check.
	bodyHidden     []hidden
	promptHidden   []hidden
	warnings       []string
	affectedFields int
}

func sanitizeTexts(b Bundle) cleanTexts {
	s := cleanTexts{
		skillBodies: make([]string, len(b.Skills)), skillDescriptions: make([]string, len(b.Skills)),
		agentPrompts: make([]string, len(b.Agents)),
		bodyHidden:   make([]hidden, len(b.Skills)), promptHidden: make([]hidden, len(b.Agents)),
	}
	note := func(noun, name, field string, h hidden) {
		if !h.any() {
			return
		}
		s.affectedFields++
		if s.affectedFields > MaxHiddenTextWarnings {
			return
		}
		s.warnings = append(s.warnings, fmt.Sprintf("%s %q: hidden text removed from the %s (%s)", noun, name, field, h.kinds()))
	}
	for i, sk := range b.Skills {
		var h hidden
		s.skillBodies[i], h = stripHidden(sk.Body)
		s.bodyHidden[i] = h
		note("skill", sk.Name, "body", h)
		// The description reaches the review prompt ("Applies when") like the body (N2). One that
		// is left empty stays empty, as a plugin skill may have no description.
		s.skillDescriptions[i], h = stripHidden(sk.Description)
		note("skill", sk.Name, "description", h)
	}
	for i, a := range b.Agents {
		var h hidden
		s.agentPrompts[i], h = stripHidden(a.SystemPrompt)
		s.promptHidden[i] = h
		note("agent", a.Name, "system prompt", h)
	}
	if more := s.affectedFields - MaxHiddenTextWarnings; more > 0 {
		s.warnings = append(s.warnings, fmt.Sprintf("%d more item(s) had hidden text removed", more))
	}
	return s
}

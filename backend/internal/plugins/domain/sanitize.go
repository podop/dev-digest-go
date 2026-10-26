package domain

import (
	"fmt"
	"regexp"
	"strings"
)

// MaxHiddenTextWarnings is the most per-item hidden-text warnings a preview lists; the rest are
// summed in one last warning.
const MaxHiddenTextWarnings = MaxIssues

// hiddenCommentRE copies skills/domain's htmlCommentRE: an unclosed comment runs to the end.
// cmd/server's drift test (plugin_sanitize_drift_test.go) pins SanitizeHiddenText to
// skills/domain.SanitizeMarkdown on a corpus of hostile inputs.
var hiddenCommentRE = regexp.MustCompile(`(?s)<!--.*?(?:-->|\z)`)

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
	if n := len(hiddenCommentRE.FindAllStringIndex(text, -1)); n > 0 {
		text = hiddenCommentRE.ReplaceAllString(text, "")
		h.comments = n
	}
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

// sanitized are the texts an install stores, with hidden text removed, and one warning per item
// that had some (at most MaxHiddenTextWarnings, then a summary). Both PlanInstall and Preview
// derive from it, so the preview warns about exactly what the install removes.
type sanitized struct {
	skillBodies   []string
	agentPrompts  []string
	warnings      []string
	affectedItems int
}

func sanitizeTexts(b Bundle) sanitized {
	s := sanitized{skillBodies: make([]string, len(b.Skills)), agentPrompts: make([]string, len(b.Agents))}
	note := func(noun, name, field string, h hidden) {
		if !h.any() {
			return
		}
		s.affectedItems++
		if s.affectedItems > MaxHiddenTextWarnings {
			return
		}
		s.warnings = append(s.warnings, fmt.Sprintf("%s %q: hidden text removed from the %s (%s)", noun, name, field, h.kinds()))
	}
	for i, sk := range b.Skills {
		text, h := stripHidden(sk.Body)
		s.skillBodies[i] = text
		note("skill", sk.Name, "body", h)
	}
	for i, a := range b.Agents {
		text, h := stripHidden(a.SystemPrompt)
		s.agentPrompts[i] = text
		note("agent", a.Name, "system prompt", h)
	}
	if more := s.affectedItems - MaxHiddenTextWarnings; more > 0 {
		s.warnings = append(s.warnings, fmt.Sprintf("%d more item(s) had hidden text removed", more))
	}
	return s
}

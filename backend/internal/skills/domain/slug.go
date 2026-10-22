package domain

import (
	"regexp"
	"strings"

	"golang.org/x/text/unicode/norm"
)

var (
	slugExtRE   = regexp.MustCompile(`(?i)\.(?:md|markdown|txt|zip)\z`)
	slugOtherRE = regexp.MustCompile(`[^a-z0-9]+`)
)

// Slugify turns free text into a skill name: Unicode NFKD, combining marks U+0300–U+036F removed,
// lowercased, a trailing .md/.markdown/.txt/.zip dropped, every run of other characters replaced
// by one dash, dashes trimmed, cut at SkillNameMax. Text with nothing left gives FallbackSkillName.
func Slugify(input string) string {
	s := norm.NFKD.String(input)
	s = strings.Map(func(r rune) rune {
		if r >= 0x0300 && r <= 0x036F {
			return -1
		}
		return r
	}, s)
	s = strings.ToLower(s)
	s = slugExtRE.ReplaceAllString(s, "")
	s = slugOtherRE.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if len(s) > SkillNameMax { // only [a-z0-9-] is left, so bytes are characters
		s = s[:SkillNameMax]
	}
	s = strings.TrimRight(s, "-")
	if s == "" {
		return FallbackSkillName
	}
	return s
}

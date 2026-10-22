// Package domain holds the pure import rules of the skills context: sanitizing and reading foreign
// markdown, slugs, the one-entry zip reader, URL host rules and the community catalog filter.
// It performs no I/O and has no outward dependencies (golang.org/x/text and x/net/idna only
// implement Unicode rules).
package domain

import (
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

// Limits of the skills context (wire contract: frontend/src/vendor/shared/constants/skills.ts).
const (
	// SkillBodyMax is the longest skill body, in UTF-16 code units like the Node server counts.
	SkillBodyMax = 20_000
	// SkillDescriptionMax is the longest skill description, in UTF-16 code units.
	SkillDescriptionMax = 300
	// SkillNameMax is the longest skill name.
	SkillNameMax = 64
	// ImportMaxBase64Chars is the longest base64 text of a file import.
	ImportMaxBase64Chars = 2 * 1024 * 1024
	// FallbackSkillName names a skill whose name slugs to nothing.
	FallbackSkillName = "imported-skill"
)

// SkillType classifies a skill.
type SkillType string

// The skill types.
const (
	TypeRubric     SkillType = "rubric"
	TypeConvention SkillType = "convention"
	TypeSecurity   SkillType = "security"
	TypeCustom     SkillType = "custom"
)

// ParseSkillType returns the type spelled exactly like s (case-sensitive).
func ParseSkillType(s string) (SkillType, bool) {
	switch t := SkillType(s); t {
	case TypeRubric, TypeConvention, TypeSecurity, TypeCustom:
		return t, true
	default:
		return "", false
	}
}

// ImportError is a rejected import. Its message is written for the user and is returned to the
// client as 422 invalid_import.
type ImportError struct{ Message string }

// Error implements error.
func (e *ImportError) Error() string { return e.Message }

// InvalidImport returns an ImportError with the given user-facing message.
func InvalidImport(message string) *ImportError { return &ImportError{Message: message} }

// Candidate is the skill core derived from a markdown document.
type Candidate struct {
	Name        string
	Description string
	Type        SkillType
	Body        string
	Warnings    []string
}

var (
	htmlCommentRE = regexp.MustCompile(`(?s)<!--.*?(?:-->|\z)`)
	frontmatterRE = regexp.MustCompile(`(?s)\A---[ \t]*\n(.*?)\n---[ \t]*(?:\n|\z)`)
	frontLineRE   = regexp.MustCompile(`^([A-Za-z][\w-]*)[ \t]*:[ \t]*([^\n\r\x{2028}\x{2029}]*)$`)
	paragraphRE   = regexp.MustCompile(`\n[\t\n\v\f\r \x{a0}\x{1680}\x{2000}-\x{200a}\x{2028}\x{2029}\x{202f}\x{205f}\x{3000}\x{feff}]*\n`)
)

func isZeroWidth(r rune) bool {
	return (r >= 0x200B && r <= 0x200D) || r == 0x2060 || r == 0xFEFF
}

func isBidiControl(r rune) bool {
	return r == 0x061C || r == 0x200E || r == 0x200F ||
		(r >= 0x202A && r <= 0x202E) || (r >= 0x2066 && r <= 0x2069)
}

// removeRunes drops the runes pred accepts and reports how many it dropped.
func removeRunes(s string, pred func(rune) bool) (string, int) {
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

// SanitizeMarkdown removes what a reviewer cannot see but a model would read: HTML comments,
// zero-width and bidi control characters. Line ends become LF. Every removal adds a warning.
func SanitizeMarkdown(input string) (string, []string) {
	var warnings []string
	text := strings.ReplaceAll(input, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")

	if n := len(htmlCommentRE.FindAllStringIndex(text, -1)); n > 0 {
		text = htmlCommentRE.ReplaceAllString(text, "")
		warnings = append(warnings, fmt.Sprintf("Removed %d HTML comment(s) (hidden text)", n))
	}
	var n int
	if text, n = removeRunes(text, isZeroWidth); n > 0 {
		warnings = append(warnings, fmt.Sprintf("Removed %d zero-width character(s)", n))
	}
	if text, n = removeRunes(text, isBidiControl); n > 0 {
		warnings = append(warnings, fmt.Sprintf("Removed %d bidi control character(s)", n))
	}
	return text, warnings
}

// ParseFrontmatter reads a leading `---` block of flat `key: value` lines. Keys are lowercased,
// one pair of matching quotes is removed from a value, empty values and other lines are ignored.
// There is no YAML parser on purpose: foreign input gets the smallest possible surface.
func ParseFrontmatter(text string) (map[string]string, string) {
	data := map[string]string{}
	m := frontmatterRE.FindStringSubmatchIndex(text)
	if m == nil {
		return data, text
	}
	for line := range strings.SplitSeq(text[m[2]:m[3]], "\n") {
		kv := frontLineRE.FindStringSubmatch(line)
		if kv == nil {
			continue
		}
		value := unquote(jsTrim(kv[2]))
		if value != "" {
			data[strings.ToLower(kv[1])] = value
		}
	}
	return data, text[m[1]:]
}

// unquote removes one pair of matching quotes and trims what is left.
func unquote(v string) string {
	if len(v) >= 2 && (v[0] == '"' || v[0] == '\'') && v[len(v)-1] == v[0] {
		v = v[1 : len(v)-1]
	}
	return jsTrim(v)
}

// FirstParagraph is the first block of prose: blank-line separated, ignoring heading and code
// fence lines, its lines joined with a space.
func FirstParagraph(body string) string {
	for _, p := range paragraphRE.Split(body, -1) {
		var lines []string
		for l := range strings.SplitSeq(p, "\n") {
			l = jsTrim(l)
			if l != "" && !strings.HasPrefix(l, "#") && !strings.HasPrefix(l, "```") {
				lines = append(lines, l)
			}
		}
		if len(lines) > 0 {
			return strings.Join(lines, " ")
		}
	}
	return ""
}

// SkillFromMarkdown derives the skill core shown in an import preview. fallbackName (a file or
// folder name) names the skill when the front matter has no name. It fails with an ImportError
// when no body is left or the body is longer than SkillBodyMax.
func SkillFromMarkdown(markdown, fallbackName string) (Candidate, error) {
	text, warnings := SanitizeMarkdown(markdown)
	data, rawBody := ParseFrontmatter(text)
	body := jsTrim(rawBody)
	if body == "" {
		return Candidate{}, InvalidImport("The skill has no body")
	}
	if n := utf16Len(body); n > SkillBodyMax {
		return Candidate{}, InvalidImport(fmt.Sprintf("The skill body is %d chars; the limit is %d", n, SkillBodyMax))
	}

	rawName, hasName := data["name"]
	if !hasName {
		rawName = fallbackName
	}
	name := Slugify(rawName)
	if hasName && name != rawName {
		warnings = append(warnings, fmt.Sprintf("Name normalized to %q", name))
	}

	description, ok := data["description"]
	if !ok {
		description = FirstParagraph(body)
	}
	description = collapseSpace(description)
	if utf16Len(description) > SkillDescriptionMax {
		description = strings.TrimRightFunc(utf16Prefix(description, SkillDescriptionMax-1), isJSSpace) + "…"
		warnings = append(warnings, fmt.Sprintf("Description shortened to %d chars", SkillDescriptionMax))
	}

	typ, valid := ParseSkillType(data["type"])
	if !valid {
		if given := data["type"]; given != "" {
			warnings = append(warnings, fmt.Sprintf("Unknown type \"%s\" — using \"custom\"", given))
		}
		typ = TypeCustom
	}
	return Candidate{Name: name, Description: description, Type: typ, Body: body, Warnings: warnings}, nil
}

// DecodeUTF8 returns b as text, dropping a leading byte-order mark. ok is false when b is not
// valid UTF-8.
func DecodeUTF8(b []byte) (string, bool) {
	if !utf8.Valid(b) {
		return "", false
	}
	return strings.TrimPrefix(string(b), "\xEF\xBB\xBF"), true
}

// ---- file names -------------------------------------------------------------------------------

// BaseName is the part of a slash-separated path after its last slash.
func BaseName(path string) string { return path[strings.LastIndex(path, "/")+1:] }

// FileExt is the lowercased extension of the base name with its dot, or "" when the only dot is
// the first character or there is none.
func FileExt(path string) string {
	base := BaseName(path)
	if dot := strings.LastIndex(base, "."); dot > 0 {
		return strings.ToLower(base[dot:])
	}
	return ""
}

// IsMarkdownName reports whether the file name has a text extension: .md, .markdown or .txt.
func IsMarkdownName(path string) bool {
	switch FileExt(path) {
	case ".md", ".markdown", ".txt":
		return true
	default:
		return false
	}
}

// IsZipName reports whether the file name ends in .zip.
func IsZipName(path string) bool { return FileExt(path) == ".zip" }

// StemOf is the base name without its last extension.
func StemOf(path string) string {
	base := BaseName(path)
	if dot := strings.LastIndex(base, "."); dot > 0 {
		return base[:dot]
	}
	return base
}

// ---- JavaScript string semantics (the Node server counted and trimmed this way) -----------------

// isJSSpace reports whether r is JavaScript white space or a line terminator.
func isJSSpace(r rune) bool {
	switch r {
	case '\t', '\n', '\v', '\f', '\r', ' ', 0xA0, 0x1680, 0x2028, 0x2029, 0x202F, 0x205F, 0x3000, 0xFEFF:
		return true
	}
	return r >= 0x2000 && r <= 0x200A
}

func jsTrim(s string) string { return strings.TrimFunc(s, isJSSpace) }

// collapseSpace replaces every run of white space with one space and trims the ends.
func collapseSpace(s string) string { return strings.Join(strings.FieldsFunc(s, isJSSpace), " ") }

// utf16Len is the length JavaScript reports for s.
func utf16Len(s string) int {
	n := 0
	for _, r := range s {
		n++
		if r >= 0x10000 {
			n++
		}
	}
	return n
}

// utf16Prefix is the longest prefix of s that is at most n UTF-16 code units; a surrogate pair is
// never cut in half.
func utf16Prefix(s string, n int) string {
	units := 0
	for i, r := range s {
		w := 1
		if r >= 0x10000 {
			w = 2
		}
		if units+w > n {
			return s[:i]
		}
		units += w
	}
	return s
}

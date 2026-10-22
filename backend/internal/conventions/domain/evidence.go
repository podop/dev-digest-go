// Package domain holds the pure rules of the conventions context: sample
// selection and rendering, the extraction prompt and schema, evidence
// verification and the candidate gate. It reads no files and logs nothing;
// the application layer reads the clone and calls these functions.
package domain

import (
	"regexp"
	"strings"
	"unicode/utf16"
)

// EvidenceMaxLines is the number of real file lines stored for one evidence item.
const EvidenceMaxLines = 12

// partialMinChars is the length from which a snippet line may match a longer
// file line as a substring.
const partialMinChars = 12

// DropReason says why a candidate was not kept.
type DropReason string

// Drop reasons of the wire contract.
const (
	ReasonFileNotFound    DropReason = "file_not_found"
	ReasonLineOutOfRange  DropReason = "line_out_of_range"
	ReasonSnippetMismatch DropReason = "snippet_mismatch"
	ReasonDuplicate       DropReason = "duplicate"
	ReasonInvalid         DropReason = "invalid"
)

// ClaimedEvidence is evidence as the model cited it (unverified).
type ClaimedEvidence struct {
	Path      string
	StartLine int
	EndLine   int
	Snippet   string
}

// Evidence is one verified place in the repository; Snippet is the real file text.
type Evidence struct {
	Path      string
	StartLine int
	EndLine   int
	Snippet   string
}

// jsSpaceClass is the character class of JavaScript's \s. RE2's \s lacks the
// vertical tab, the no-break space, the Unicode spaces and the byte order mark.
const jsSpaceClass = `[\s\x{0B}\x{A0}\x{1680}\x{2000}-\x{200A}\x{2028}\x{2029}\x{202F}\x{205F}\x{3000}\x{FEFF}]`

// lineNoPrefix matches the "  12| " prefix of a numbered listing line that the
// model sometimes copies.
var lineNoPrefix = regexp.MustCompile(`^` + jsSpaceClass + `*[0-9]+\|` + jsSpaceClass + `?`)

// isJSSpace reports whether r is white space in the sense of JavaScript's
// String.prototype.trim and \s.
func isJSSpace(r rune) bool {
	switch r {
	case '\t', '\n', '\v', '\f', '\r', ' ', 0xA0, 0x1680, 0x2028, 0x2029, 0x202F, 0x205F, 0x3000, 0xFEFF:
		return true
	}
	return r >= 0x2000 && r <= 0x200A
}

// utf16Len is the length of s in UTF-16 code units (JavaScript's String.length).
func utf16Len(s string) int {
	n := 0
	for _, r := range s {
		if r >= 0x10000 {
			n += 2
		} else {
			n++
		}
	}
	return n
}

// truncateUTF16 cuts s to at most n UTF-16 code units without splitting a
// surrogate pair.
func truncateUTF16(s string, n int) string {
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

// compareCodeUnits orders strings like JavaScript's default sort (UTF-16 code units).
func compareCodeUnits(a, b string) int {
	ua, ub := utf16.Encode([]rune(a)), utf16.Encode([]rune(b))
	for i := 0; i < len(ua) && i < len(ub); i++ {
		if ua[i] != ub[i] {
			if ua[i] < ub[i] {
				return -1
			}
			return 1
		}
	}
	switch {
	case len(ua) < len(ub):
		return -1
	case len(ua) > len(ub):
		return 1
	}
	return 0
}

// compareName orders names case-insensitively with byte order as tie-break (D211).
func compareName(a, b string) int {
	if c := strings.Compare(strings.ToLower(a), strings.ToLower(b)); c != 0 {
		return c
	}
	return strings.Compare(a, b)
}

// collapse trims JS white space and folds every run of it into one space.
func collapse(s string) string {
	return strings.Join(strings.FieldsFunc(s, isJSSpace), " ")
}

// normalizeNewlines turns CRLF and lone CR into LF.
func normalizeNewlines(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return strings.ReplaceAll(s, "\r", "\n")
}

// SplitLines splits file text into lines the way the evidence check expects:
// CR and CRLF become LF, and a trailing newline yields a final empty line.
func SplitLines(text string) []string {
	return strings.Split(normalizeNewlines(text), "\n")
}

// SafeRelativePath returns a repository-relative POSIX path, or false when the
// path could escape the clone: empty, NUL, absolute, a drive letter, or an
// empty, "." or ".." segment.
func SafeRelativePath(raw string) (string, bool) {
	p := strings.ReplaceAll(strings.TrimFunc(raw, isJSSpace), `\`, "/")
	for strings.HasPrefix(p, "./") {
		p = p[2:]
	}
	if p == "" || strings.ContainsRune(p, 0) || strings.HasPrefix(p, "/") {
		return "", false
	}
	if len(p) >= 2 && p[1] == ':' && isASCIILetter(p[0]) {
		return "", false
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return "", false
		}
	}
	return p, true
}

func isASCIILetter(b byte) bool { return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') }

func snippetLines(snippet string) []string {
	var out []string
	for _, l := range strings.Split(normalizeNewlines(snippet), "\n") {
		if n := collapse(lineNoPrefix.ReplaceAllString(l, "")); n != "" {
			out = append(out, n)
		}
	}
	return out
}

func lineMatches(fileLine, snip string) bool {
	return fileLine == snip || (utf16Len(snip) >= partialMinChars && strings.Contains(fileLine, snip))
}

// dedent removes the common leading space/tab indent of the non-blank lines and
// trims trailing white space.
func dedent(lines []string) string {
	cut := -1
	for _, l := range lines {
		if collapse(l) == "" {
			continue
		}
		n := len(l) - len(strings.TrimLeft(l, " \t"))
		if cut < 0 || n < cut {
			cut = n
		}
	}
	if cut < 0 {
		cut = 0
	}
	out := make([]string, len(lines))
	for i, l := range lines {
		rs := []rune(l)
		if len(rs) <= cut {
			rs = nil
		} else {
			rs = rs[cut:]
		}
		out[i] = strings.TrimRightFunc(string(rs), isJSSpace)
	}
	return strings.Join(out, "\n")
}

// VerifyEvidence checks one cited evidence item against the file's lines. The
// snippet is searched in the whole file (the match whose first line is nearest
// to the cited start line wins, the earlier one on a tie) and a found snippet is
// relocated to its real lines; the stored snippet is the real file text. On
// failure it returns the drop reason.
func VerifyEvidence(fileLines []string, claimed ClaimedEvidence) (Evidence, DropReason, bool) {
	wanted := snippetLines(claimed.Snippet)
	if len(wanted) == 0 {
		return Evidence{}, ReasonInvalid, false
	}

	normed := make([]string, len(fileLines))
	var nonBlank []int
	for i, l := range fileLines {
		normed[i] = collapse(l)
		if normed[i] != "" {
			nonBlank = append(nonBlank, i)
		}
	}

	best := -1
	cited := claimed.StartLine - 1
	for k := 0; k+len(wanted) <= len(nonBlank); k++ {
		ok := true
		for j, w := range wanted {
			if !lineMatches(normed[nonBlank[k+j]], w) {
				ok = false
				break
			}
		}
		if ok && (best < 0 || absInt(nonBlank[k]-cited) < absInt(nonBlank[best]-cited)) {
			best = k
		}
	}
	if best < 0 {
		if claimed.StartLine > len(fileLines) {
			return Evidence{}, ReasonLineOutOfRange, false
		}
		return Evidence{}, ReasonSnippetMismatch, false
	}

	first := nonBlank[best]
	last := nonBlank[best+len(wanted)-1]
	end := min(last, first+EvidenceMaxLines-1)
	return Evidence{
		Path:      claimed.Path,
		StartLine: first + 1,
		EndLine:   end + 1,
		Snippet:   dedent(fileLines[first : end+1]),
	}, "", true
}

func absInt(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

var nonKeyRE = regexp.MustCompile(`[^a-z0-9]+`)

// RuleKey is the key for de-duplicating rules: case, punctuation and spacing do not count.
func RuleKey(rule string) string {
	return strings.TrimFunc(nonKeyRE.ReplaceAllString(strings.ToLower(rule), " "), isJSSpace)
}

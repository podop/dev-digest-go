// Package notebody implements the GitLab note body rules (spec GitLab B FR13-FR17): the
// text every note DevDigest sends to GitLab must pass before the request leaves the process.
//
// GitLab runs quick actions (a line that starts with /approve, /merge, ...) when a note is
// created, edited or published, notifies @mentions, renders raw HTML and fetches Markdown
// images. [Apply] neutralises all four with invisible U+200B characters and escaped HTML:
//
//   - FR14: CR LF and a lone CR become LF; a code region is a run of N backticks up to the next
//     run of exactly N backticks (a run with no closer is not code).
//   - FR15 (everywhere, code regions included): U+200B before the "/" that follows a line's
//     leading prefix of spaces, tabs, U+FEFF, U+00A0, ">" and list markers.
//   - FR16 (outside code regions): U+200B after each "@"; "<" and ">" become &lt; and &gt;
//     except on the "<sub>DevDigest · N% confidence</sub>" line and the body's last non-empty
//     line when it is the "<!-- devdigest-finding:ID -->" marker; "![" becomes "!" + U+200B + "[".
//   - FR17: the output depends only on the body and Apply is idempotent.
//
// The package imports only the standard library: the GitLab CI runner (slice C) links it, and
// its closure test forbids server code.
package notebody

import (
	"strings"
	"unicode/utf8"
)

// MaxChars is GitLab's note length limit in characters (Unicode code points, see [Chars]).
const MaxChars = 1_000_000

// ZWSP is the neutralising character, U+200B ZERO WIDTH SPACE (spec decision D29).
const ZWSP = "\u200b"

// Byte spellings of the multi-byte prefix characters of FR15.
const (
	bom  = "\ufeff"
	nbsp = "\u00a0"
)

// Chars returns the length of s in Unicode code points, the unit of GitLab's note limit.
// A byte that is not valid UTF-8 counts as one character.
func Chars(s string) int { return utf8.RuneCountInString(s) }

// special marks the bytes FR16 may change outside code regions.
var special = [256]bool{'@': true, '<': true, '>': true, '!': true}

// Apply returns body after FR14, FR15 and FR16, in that order, applied to the whole body.
// It runs in time linear in len(body).
func Apply(body string) string {
	if strings.Contains(body, "\r") {
		body = normalizeNewlines(body)
	}
	regions := codeRegions(body)
	lastStart := lastNonEmptyLineStart(body)

	out := make([]byte, 0, len(body)+len(body)/8+16)
	flushed := 0 // body[:flushed] is already in out
	edit := func(at int, replacement string, skip int) {
		out = append(out, body[flushed:at]...)
		out = append(out, replacement...)
		flushed = at + skip
	}

	next := 0 // index of the first region that has not ended before the cursor
	for lineStart := 0; lineStart <= len(body); {
		lineEnd := len(body)
		if k := strings.IndexByte(body[lineStart:], '\n'); k >= 0 {
			lineEnd = lineStart + k
		}
		line := body[lineStart:lineEnd]
		slash := quickActionSlash(line)
		allowed := isSubLine(line) || (lineStart == lastStart && isMarkerLine(line))

		for j := 0; j < len(line); {
			i := lineStart + j
			if j == slash {
				edit(i, ZWSP, 0)
			}
			for next < len(regions) && regions[next].end <= i {
				next++
			}
			if next < len(regions) && regions[next].start <= i {
				// Inside a code region only FR15 applies: skip to its end, the line end or the slash.
				stop := min(regions[next].end, lineEnd) - lineStart
				if slash > j {
					stop = min(stop, slash)
				}
				j = stop
				continue
			}
			c := line[j]
			if !special[c] {
				j++
				continue
			}
			switch c {
			case '@':
				if !strings.HasPrefix(line[j+1:], ZWSP) {
					edit(i+1, ZWSP, 0)
				}
			case '<':
				if !allowed {
					edit(i, "&lt;", 1)
				}
			case '>':
				if !allowed {
					edit(i, "&gt;", 1)
				}
			case '!':
				if j+1 < len(line) && line[j+1] == '[' {
					edit(i+1, ZWSP, 0)
				}
			}
			j++
		}
		if lineEnd == len(body) {
			break
		}
		lineStart = lineEnd + 1
	}
	out = append(out, body[flushed:]...)
	return string(out)
}

// normalizeNewlines turns CR LF and a lone CR into LF (FR14), scanning left to right.
func normalizeNewlines(s string) string {
	b := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		if s[i] != '\r' {
			b = append(b, s[i])
			continue
		}
		b = append(b, '\n')
		if i+1 < len(s) && s[i+1] == '\n' {
			i++
		}
	}
	return string(b)
}

// region is the half-open byte range [start, end) of a code region, backtick runs included.
type region struct{ start, end int }

// codeRegions finds the code regions of s in linear time: each backtick run opens a region at
// the next run of exactly the same length; a run with no such closer is plain text and the scan
// continues with the following run.
func codeRegions(s string) []region {
	type run struct{ start, length int }
	var runs []run
	for i := 0; i < len(s); {
		k := strings.IndexByte(s[i:], '`')
		if k < 0 {
			break
		}
		start := i + k
		end := start
		for end < len(s) && s[end] == '`' {
			end++
		}
		runs = append(runs, run{start, end - start})
		i = end
	}
	if len(runs) < 2 {
		return nil
	}

	// nextSame[i] is the index of the next run with the same length as runs[i], or -1.
	nextSame := make([]int, len(runs))
	seen := make(map[int]int)
	for i := len(runs) - 1; i >= 0; i-- {
		if j, ok := seen[runs[i].length]; ok {
			nextSame[i] = j
		} else {
			nextSame[i] = -1
		}
		seen[runs[i].length] = i
	}

	var regions []region
	for i := 0; i < len(runs); {
		j := nextSame[i]
		if j < 0 {
			i++
			continue
		}
		regions = append(regions, region{runs[i].start, runs[j].start + runs[j].length})
		i = j + 1
	}
	return regions
}

// quickActionSlash returns the index in line of the "/" that FR15 puts a U+200B before, or -1.
func quickActionSlash(line string) int {
	i := 0
	for i < len(line) {
		switch c := line[i]; {
		case c == ' ' || c == '\t' || c == '>':
			i++
		case strings.HasPrefix(line[i:], bom):
			i += len(bom)
		case strings.HasPrefix(line[i:], nbsp):
			i += len(nbsp)
		case (c == '-' || c == '*' || c == '+') && i+1 < len(line) && isBlank(line[i+1]):
			i++
		case c >= '0' && c <= '9':
			end := i
			for end < len(line) && line[end] >= '0' && line[end] <= '9' && end-i <= 9 {
				end++
			}
			if end-i > 9 || end+1 >= len(line) || (line[end] != '.' && line[end] != ')') || !isBlank(line[end+1]) {
				return -1
			}
			i = end + 1
		case c == '/':
			return i
		default:
			return -1
		}
	}
	return -1
}

func isBlank(c byte) bool { return c == ' ' || c == '\t' }

// lastNonEmptyLineStart returns the offset of the body's last line that has a character other
// than a space or tab (SCR6: such a line is empty), or -1 when there is none.
func lastNonEmptyLineStart(s string) int {
	end := len(s)
	for end > 0 && (s[end-1] == ' ' || s[end-1] == '\t' || s[end-1] == '\n') {
		end--
	}
	if end == 0 {
		return -1
	}
	return strings.LastIndexByte(s[:end], '\n') + 1
}

// isSubLine reports whether line is exactly "<sub>DevDigest · N% confidence</sub>", N 1-3 digits.
func isSubLine(line string) bool {
	const prefix, suffix = "<sub>DevDigest · ", "% confidence</sub>"
	if !strings.HasPrefix(line, prefix) || !strings.HasSuffix(line, suffix) {
		return false
	}
	digits := line[len(prefix) : len(line)-len(suffix)]
	if len(digits) < 1 || len(digits) > 3 {
		return false
	}
	for i := 0; i < len(digits); i++ {
		if digits[i] < '0' || digits[i] > '9' {
			return false
		}
	}
	return true
}

// isMarkerLine reports whether line is exactly "<!-- devdigest-finding:ID -->", ID 1-64 of [A-Za-z0-9_-].
func isMarkerLine(line string) bool {
	const prefix, suffix = "<!-- devdigest-finding:", " -->"
	if !strings.HasPrefix(line, prefix) || !strings.HasSuffix(line, suffix) || len(line) < len(prefix)+len(suffix) {
		return false
	}
	id := line[len(prefix) : len(line)-len(suffix)]
	if len(id) < 1 || len(id) > 64 {
		return false
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
			return false
		}
	}
	return true
}

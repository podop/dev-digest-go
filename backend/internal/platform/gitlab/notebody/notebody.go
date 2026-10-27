// Package notebody implements the GitLab note body rules (spec GitLab B FR13-FR17): the
// text every note DevDigest sends to GitLab must pass before the request leaves the process.
//
// GitLab runs quick actions (a line that starts with /approve, /merge, ...) when a note is
// created, edited or published, notifies @mentions, renders raw HTML and fetches Markdown
// images. [Apply] neutralises all four with invisible U+200B characters and escaped HTML:
//
//   - FR14: CR LF and a lone CR become LF; a top-level fence the body leaves open is closed on its own
//     line before the trailing "<sub>" / marker lines, so those lines always render outside code (an
//     unclosed fence would swallow the marker and the duplicate check would miss the comment).
//   - FR15 (everywhere, code included): U+200B before the "/" that follows a line's leading prefix
//     of spaces, tabs, U+FEFF, U+00A0, ">" and list markers.
//   - FR16, everywhere: U+200B after each "@"; "![" becomes "!" + U+200B + "["; the "&" of a character
//     reference that decodes to "@", "<", "!" or "[" is neutralised. Outside code regions "<" and ">"
//     become &lt; and &gt; and the entity "&" becomes "&amp;"; inside code regions (see [codeRegions])
//     the changes are invisible only: U+200B after each "<" and after the "&" of such an entity
//     (decision D-new-3). Code is a best-effort guess of where GitLab renders code, and a wrong guess
//     can only change how text looks, never let a mention, tag or image through. A "<" already followed
//     by U+200B is left as it is. The "<sub>DevDigest · N% confidence</sub>" line and the body's last
//     non-empty line when it is the "<!-- devdigest-finding:ID -->" marker (FR16(b)) are kept as they
//     are wherever they are.
//   - FR17: the output depends only on the body and Apply is idempotent.
//
// The package imports only the standard library: the GitLab CI runner (slice C) links it, and
// its closure test forbids server code.
package notebody

import (
	"sort"
	"strings"
	"unicode"
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

// special marks the bytes FR16 may change.
var special = [256]bool{'@': true, '<': true, '>': true, '!': true, '&': true}

// Apply returns body after FR14, FR15 and FR16, in that order, applied to the whole body.
// It runs in time linear in len(body) (log factor for the fence pairing).
func Apply(body string) string {
	if strings.Contains(body, "\r") {
		body = normalizeNewlines(body)
	}
	body = closeOpenFence(body)
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
		// FR16(b): the two allowed lines hold no "@", "![" or entity and stay as they are, in code or not.
		if !(isSubLine(line) || (lineStart == lastStart && isMarkerLine(line))) {
			slash := quickActionSlash(line)
			for j := 0; j < len(line); j++ {
				i := lineStart + j
				if j == slash {
					edit(i, ZWSP, 0)
				}
				c := line[j]
				if !special[c] {
					continue
				}
				for next < len(regions) && regions[next].end <= i {
					next++
				}
				inCode := next < len(regions) && regions[next].start <= i
				switch c {
				case '@':
					if !strings.HasPrefix(line[j+1:], ZWSP) {
						edit(i+1, ZWSP, 0)
					}
				case '<':
					if strings.HasPrefix(line[j+1:], ZWSP) {
						break
					}
					if inCode {
						edit(i+1, ZWSP, 0)
					} else {
						edit(i, "&lt;", 1)
					}
				case '>':
					if !inCode {
						edit(i, "&gt;", 1)
					}
				case '!':
					if j+1 < len(line) && line[j+1] == '[' {
						edit(i+1, ZWSP, 0)
					}
				case '&':
					if specialEntity(line[j:]) {
						if inCode {
							edit(i+1, ZWSP, 0)
						} else {
							edit(i, "&amp;", 1)
						}
					}
				}
			}
		}
		if lineEnd == len(body) {
			break
		}
		lineStart = lineEnd + 1
	}
	out = append(out, body[flushed:]...)
	return string(out)
}

// closeOpenFence returns s with a top-level fence that is still open before the trailing
// "<sub>" / marker lines closed on its own line right before them. Those lines are allowed raw
// (FR16(b)), so inside a fence GitLab would show them as code text and the marker would not be
// found. The fence model is that of [codeRegions]; a body that leaves no fence open, or has no
// such trailing lines, is returned as it is, so a second application changes nothing.
func closeOpenFence(s string) string {
	start := trailerStart(s)
	if start <= 0 {
		return s
	}
	prefix := s[:start]
	if !strings.Contains(prefix, "```") && !strings.Contains(prefix, "~~~") {
		return s
	}
	ch, n, ok := openFence(prefix)
	if !ok {
		return s
	}
	return prefix + strings.Repeat(string(ch), n) + "\n" + s[start:]
}

// trailerStart returns the offset of the first of the body's trailing allowed lines: the marker
// line when it is the last non-empty line, together with a "<sub>" line right before it, or a
// "<sub>" last non-empty line alone. -1 when the body has none.
func trailerStart(s string) int {
	last := lastNonEmptyLineStart(s)
	if last < 0 {
		return -1
	}
	line := lineFrom(s, last)
	switch {
	case isMarkerLine(line):
		if p := lastNonEmptyLineStart(s[:last]); p >= 0 && isSubLine(lineFrom(s, p)) {
			return p
		}
		return last
	case isSubLine(line):
		return last
	}
	return -1
}

// lineFrom returns the line of s that starts at offset start, without its newline.
func lineFrom(s string, start int) string {
	if k := strings.IndexByte(s[start:], '\n'); k >= 0 {
		return s[start : start+k]
	}
	return s[start:]
}

// openFence reports the fence a top-level opener leaves unclosed in s (the character and the
// opener's run length), walking s with the same rules as [codeRegions].
func openFence(s string) (ch byte, n int, ok bool) {
	starts := lineStarts(s)
	closer := fenceClosers(s, starts)
	containerSeen := false
	for i := 0; i < len(starts); {
		line := lineAt(s, starts, i)
		if ch, n, rest, ok := fenceLine(line); ok && (ch == '~' || !strings.Contains(rest, "`")) &&
			(leadingSpaces(line) == 0 || !containerSeen) {
			if c := closer[i]; c >= 0 {
				i = c + 1
				continue
			}
			return ch, n, true
		}
		if !containerSeen && startsContainer(line) {
			containerSeen = true
		}
		i++
	}
	return 0, 0, false
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

// region is the half-open byte range [start, end) of a code region, delimiters included.
type region struct{ start, end int }

// codeRegions returns the code regions of s, sorted and disjoint, in time linear in len(s)
// (decision D-new-1, D-new-3: a best-effort model of where GitLab renders code. Code gets only
// invisible neutralisation, so a region the model gets wrong changes how text looks, never what
// GitLab parses):
//
//   - a CLOSED fenced block: an opening line of 3+ backticks (no backtick in the info string) or
//     tildes at column 0-3, closed by a line of the same character, at least as long, at column
//     0-3 and followed by blanks only. A fence indented 1-3 columns counts only while no list item
//     or block quote can be open (no earlier line started with ">" or a list marker); a fence at
//     column 0 can never be inside a container. Unclosed fences and indented code are prose.
//   - an inline span on ONE line: a backtick run and the next run of exactly the same length on
//     that line. A backslash-escaped backtick is literal, so an escaped run opens with one
//     backtick fewer; a closer is never affected by a backslash. A line that contains "|" has no
//     inline spans (a GFM table row splits its cells at a pipe even inside a code span).
//
// A line inside a fenced block is never scanned for spans.
func codeRegions(s string) []region {
	starts := lineStarts(s)
	closer := fenceClosers(s, starts)

	var fences []region
	var runs []btRun
	containerSeen := false
	for i := 0; i < len(starts); {
		line := lineAt(s, starts, i)
		if c := closer[i]; c >= 0 && (leadingSpaces(line) == 0 || !containerSeen) {
			fences = append(fences, region{starts[i], starts[c] + len(lineAt(s, starts, c))})
			i = c + 1
			continue
		}
		if !containerSeen && startsContainer(line) {
			containerSeen = true
		}
		if strings.IndexByte(line, '|') < 0 {
			runs = appendRuns(runs, s, starts[i], starts[i]+len(line))
		}
		i++
	}
	return mergeRegions(fences, inlineSpans(s, runs))
}

// lineStarts returns the byte offset of every line of s (a body of n newlines has n+1 lines).
func lineStarts(s string) []int {
	starts := make([]int, 1, strings.Count(s, "\n")+1)
	for i := 0; ; {
		k := strings.IndexByte(s[i:], '\n')
		if k < 0 {
			return starts
		}
		i += k + 1
		starts = append(starts, i)
	}
}

// lineAt returns line i of s without its newline.
func lineAt(s string, starts []int, i int) string {
	end := len(s)
	if i+1 < len(starts) {
		end = starts[i+1] - 1
	}
	return s[starts[i]:end]
}

// leadingSpaces returns the number of spaces before the first other character of line.
func leadingSpaces(line string) int {
	n := 0
	for n < len(line) && line[n] == ' ' {
		n++
	}
	return n
}

// fenceLine parses line as a fence delimiter: up to 3 spaces, then 3+ backticks or tildes. It
// reports the character, the run length and the text after the run.
func fenceLine(line string) (ch byte, n int, rest string, ok bool) {
	indent := leadingSpaces(line)
	if indent > 3 || indent >= len(line) || (line[indent] != '`' && line[indent] != '~') {
		return 0, 0, "", false
	}
	ch = line[indent]
	end := indent
	for end < len(line) && line[end] == ch {
		end++
	}
	if end-indent < 3 {
		return 0, 0, "", false
	}
	return ch, end - indent, line[end:], true
}

// fenceClosers returns, for every line, the index of the line that closes a fence opened there,
// or -1 (the line opens no fence, or none closes it). It runs right to left with one stack per
// fence character that keeps, nearest first, only closers longer than every nearer one, so
// "the nearest closer at least n long" is a binary search: linear-ish even for adversarial input.
func fenceClosers(s string, starts []int) []int {
	type closerAt struct{ line, n int }
	var stacks [2][]closerAt // backtick, tilde; the last element is the nearest and the shortest
	closer := make([]int, len(starts))
	for i := len(starts) - 1; i >= 0; i-- {
		closer[i] = -1
		ch, n, rest, ok := fenceLine(lineAt(s, starts, i))
		if !ok {
			continue
		}
		st := &stacks[0]
		if ch == '~' {
			st = &stacks[1]
		}
		if ch == '~' || !strings.Contains(rest, "`") { // opening line
			// Lengths decrease towards the end of the stack: the entries long enough are a prefix.
			k := sort.Search(len(*st), func(j int) bool { return (*st)[j].n < n })
			if k > 0 {
				closer[i] = (*st)[k-1].line
			}
		}
		if strings.Trim(rest, " \t") == "" { // closing line
			for len(*st) > 0 && (*st)[len(*st)-1].n <= n {
				*st = (*st)[:len(*st)-1]
			}
			*st = append(*st, closerAt{i, n})
		}
	}
	return closer
}

// startsContainer reports whether line opens a block quote or a list item (CommonMark: after
// leading blanks, ">" or a list marker followed by a blank or the end of the line).
func startsContainer(line string) bool {
	i := 0
	for i < len(line) && isBlank(line[i]) {
		i++
	}
	if i >= len(line) {
		return false
	}
	switch c := line[i]; {
	case c == '>' || strings.HasPrefix(line[i:], "&gt;"): // Apply writes "&gt;" for ">" outside code: same answer (FR17)
		return true
	case c == '-' || c == '*' || c == '+':
		return i+1 == len(line) || isBlank(line[i+1])
	case c >= '0' && c <= '9':
		end := i
		for end < len(line) && line[end] >= '0' && line[end] <= '9' {
			end++
		}
		return end-i <= 9 && end < len(line) && (line[end] == '.' || line[end] == ')') &&
			(end+1 == len(line) || isBlank(line[end+1]))
	}
	return false
}

// btRun is a maximal run of backticks. openStart/openLen describe it as an opener: a run
// preceded by an odd number of backslashes has its first backtick escaped (literal).
type btRun struct {
	start, end         int // the whole run
	openStart, openLen int
	lineEnd            int // offset of the end of the run's line
}

// appendRuns adds the backtick runs of s[from:to] (one line) to runs.
func appendRuns(runs []btRun, s string, from, to int) []btRun {
	for i := from; i < to; {
		k := strings.IndexByte(s[i:to], '`')
		if k < 0 {
			break
		}
		start := i + k
		end := start
		for end < to && s[end] == '`' {
			end++
		}
		slashes := 0
		for p := start - 1; p >= from && s[p] == '\\'; p-- {
			slashes++
		}
		r := btRun{start: start, end: end, openStart: start, openLen: end - start, lineEnd: to}
		if slashes%2 == 1 {
			r.openStart++
			r.openLen--
		}
		runs = append(runs, r)
		i = end
	}
	return runs
}

// inlineSpans pairs each opener with the next run of exactly its length on the same line.
func inlineSpans(s string, runs []btRun) []region {
	if len(runs) < 2 {
		return nil
	}
	// pair[i] is the nearest later run as long as runs[i] opens, or -1; one right-to-left pass
	// with the nearest run of every length.
	pair := make([]int, len(runs))
	nearest := make(map[int]int)
	for i := len(runs) - 1; i >= 0; i-- {
		pair[i] = -1
		if runs[i].openLen > 0 {
			if j, ok := nearest[runs[i].openLen]; ok {
				pair[i] = j
			}
		}
		nearest[runs[i].end-runs[i].start] = i
	}
	var spans []region
	for i := 0; i < len(runs); {
		if j := pair[i]; j >= 0 && runs[j].start < runs[i].lineEnd {
			spans = append(spans, region{runs[i].openStart, runs[j].end})
			i = j + 1
			continue
		}
		i++
	}
	return spans
}

// mergeRegions merges two sorted lists of disjoint regions.
func mergeRegions(a, b []region) []region {
	if len(b) == 0 {
		return a
	}
	out := make([]region, 0, len(a)+len(b))
	for len(a) > 0 || len(b) > 0 {
		if len(b) == 0 || (len(a) > 0 && a[0].start < b[0].start) {
			out, a = append(out, a[0]), a[1:]
		} else {
			out, b = append(out, b[0]), b[1:]
		}
	}
	return out
}

// specialEntity reports whether s, which starts with "&", begins with a character reference that
// decodes to "@", "<", "!" or "[": numeric (decimal or hex, any number of leading zeros) or one of
// &commat; &lt; &excl; &lsqb; &lbrack; in any letter case. A reference needs its ";". The exact
// lower-case "&lt;" is not reported: Apply writes it for "<", it decodes to inert text, and
// neutralising it would break FR17 and double-escape a body that was already escaped.
func specialEntity(s string) bool {
	if len(s) < 4 {
		return false
	}
	if s[1] == '#' {
		i, base := 2, 10
		if s[i] == 'x' || s[i] == 'X' {
			i, base = 3, 16
		}
		digits, v := i, 0
		for ; i < len(s); i++ {
			d := digitValue(s[i], base)
			if d < 0 {
				break
			}
			if v <= unicode.MaxRune {
				v = v*base + d
			}
		}
		if i == digits || i >= len(s) || s[i] != ';' {
			return false
		}
		return v == '@' || v == '<' || v == '!' || v == '['
	}
	i := 1
	for i < len(s) && i <= 8 && (s[i] >= 'a' && s[i] <= 'z' || s[i] >= 'A' && s[i] <= 'Z') {
		i++
	}
	if i == 1 || i >= len(s) || s[i] != ';' || s[1:i] == "lt" {
		return false
	}
	switch strings.ToLower(s[1:i]) {
	case "commat", "lt", "excl", "lsqb", "lbrack":
		return true
	}
	return false
}

// digitValue returns the value of c as a digit in base 10 or 16, or -1.
func digitValue(c byte, base int) int {
	switch {
	case c >= '0' && c <= '9':
		return int(c - '0')
	case base == 16 && c >= 'a' && c <= 'f':
		return int(c-'a') + 10
	case base == 16 && c >= 'A' && c <= 'F':
		return int(c-'A') + 10
	}
	return -1
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

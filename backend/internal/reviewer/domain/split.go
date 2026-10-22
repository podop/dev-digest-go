package domain

import (
	"strconv"
	"strings"
	"unicode/utf16"
)

// Len16 is the length of s in UTF-16 code units, which is what JavaScript's string length (and
// so every size in the review engine) counts. A byte that is not valid UTF-8 counts as one unit.
func Len16(s string) int {
	n := 0
	for _, r := range s {
		n += utf16.RuneLen(r)
	}
	return n
}

// EstimateTokens is the engine's token estimate: ceil(units / 4).
func EstimateTokens(s string) int {
	return (Len16(s) + 3) / 4
}

// prefix16 is the longest prefix of s that holds at most n UTF-16 units. A cut that would fall
// inside a surrogate pair drops the whole pair: JavaScript would keep the dangling high
// surrogate, which a Go string cannot hold.
func prefix16(s string, n int) string {
	u := 0
	for i, r := range s {
		w := utf16.RuneLen(r)
		if u+w > n {
			return s[:i]
		}
		u += w
	}
	return s
}

// JSTrim trims what JavaScript's String.prototype.trim trims (WhiteSpace and LineTerminator).
func JSTrim(s string) string {
	return strings.TrimFunc(s, isJSSpace)
}

func isJSSpace(r rune) bool {
	switch r {
	case '\t', '\n', '\v', '\f', '\r', ' ', 0xa0, 0x1680, 0x2028, 0x2029, 0x202f, 0x205f, 0x3000, 0xfeff:
		return true
	}
	return r >= 0x2000 && r <= 0x200a
}

// SplitResult is the outcome of SplitOversizeDiff.
type SplitResult struct {
	// Parts are the pieces, each at most the cap (a truncation note may add a few units to a cut piece).
	Parts []string
	// Truncated is true when at least one hunk had to be cut.
	Truncated bool
}

// diffUnit is one hunk with the file header it belongs to; the lengths are in UTF-16 units.
type diffUnit struct {
	header, hunk string
	hLen, kLen   int
}

func truncationNote(omitted, max int) string {
	return "\n[diff truncated by reviewer-core: " + strconv.Itoa(omitted) + " chars omitted (maxDiffChars=" + strconv.Itoa(max) + ")]"
}

// toUnits splits raw into (file header, hunk) units in order. Lines before the first hunk of a
// file are its header; text without a hunk yields no unit.
func toUnits(raw string) []diffUnit {
	var units []diffUnit
	var header []string
	var hunk []string
	inHunk := false
	flush := func() {
		if inHunk {
			h, k := strings.Join(header, "\n"), strings.Join(hunk, "\n")
			units = append(units, diffUnit{header: h, hunk: k, hLen: Len16(h), kLen: Len16(k)})
		}
		inHunk, hunk = false, nil
	}
	for line := range strings.SplitSeq(raw, "\n") {
		switch {
		case strings.HasPrefix(line, "diff --git "):
			flush()
			header = []string{line}
		case strings.HasPrefix(line, "@@"):
			flush()
			hunk, inHunk = []string{line}, true
		case inHunk:
			hunk = append(hunk, line)
		default:
			header = append(header, line)
		}
	}
	flush()
	return units
}

// truncateUnit cuts text to about max units, preferring a line boundary, and appends the note.
// The note's length is computed for the whole text first and for the real cut second, exactly as
// the Node splitter does, so the result can differ from max by a digit or two.
func truncateUnit(text string, max int) string {
	total := Len16(text)
	room := max - len(truncationNote(total, max))
	if room < 0 {
		room = 0
	}
	// Last "\n" that starts at a unit index <= room.
	cutAtLine, cutByte := -1, -1
	u := 0
	for i, r := range text {
		if u > room {
			break
		}
		if r == '\n' {
			cutAtLine, cutByte = u, i
		}
		u += utf16.RuneLen(r)
	}
	if cutAtLine > 0 {
		return text[:cutByte] + truncationNote(total-cutAtLine, max)
	}
	return prefix16(text, room) + truncationNote(total-room, max)
}

// SplitOversizeDiff splits raw, a chunk's diff text, into parts of at most maxUnits UTF-16 units
// at hunk boundaries (spec 7.7.3). Each part carries its file header; a hunk of the same file
// appended to a part is joined with "\n". A unit above the cap is truncated at a line boundary and
// suffixed with a note. Text without hunks is truncated whole.
func SplitOversizeDiff(raw string, maxUnits int) SplitResult {
	if Len16(raw) <= maxUnits {
		return SplitResult{Parts: []string{raw}}
	}
	units := toUnits(raw)
	if len(units) == 0 {
		return SplitResult{Parts: []string{truncateUnit(raw, maxUnits)}, Truncated: true}
	}

	var res SplitResult
	var cur strings.Builder
	curLen := 0
	curHeader := ""
	for _, u := range units {
		sameFile := curLen > 0 && curHeader == u.header
		var addition string
		var addLen int
		switch {
		case sameFile:
			addition, addLen = "\n"+u.hunk, 1+u.kLen
		case curLen > 0:
			addition, addLen = "\n"+u.header+"\n"+u.hunk, 2+u.hLen+u.kLen
		default:
			addition, addLen = u.header+"\n"+u.hunk, 1+u.hLen+u.kLen
		}
		if curLen+addLen <= maxUnits {
			cur.WriteString(addition)
			curLen += addLen
			curHeader = u.header
			continue
		}
		if curLen > 0 {
			res.Parts = append(res.Parts, cur.String())
		}
		fresh, freshLen := u.header+"\n"+u.hunk, 1+u.hLen+u.kLen
		if freshLen > maxUnits {
			fresh = truncateUnit(fresh, maxUnits)
			freshLen = Len16(fresh)
			res.Truncated = true
		}
		cur.Reset()
		cur.WriteString(fresh)
		curLen = freshLen
		curHeader = u.header
	}
	if curLen > 0 {
		res.Parts = append(res.Parts, cur.String())
	}
	return res
}

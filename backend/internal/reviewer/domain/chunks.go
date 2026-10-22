package domain

import (
	"regexp"
	"strconv"
	"strings"
	"unicode/utf16"
)

// FileChunks makes one chunk per file of the diff, labelled with the file path, whose text is the
// file's slice of the raw diff (see SliceDiff).
func FileChunks(d Diff) []Chunk {
	idx := newSectionIndex(d.Raw)
	chunks := make([]Chunk, len(d.Files))
	for i, f := range d.Files {
		chunks[i] = Chunk{Label: f.Path, DiffText: idx.slice(d, f.Path)}
	}
	return chunks
}

// WholeDiffChunk is the single chunk of a single-pass review.
func WholeDiffChunk(d Diff) Chunk {
	return Chunk{Label: "all files", DiffText: d.Raw}
}

// Oversize describes a chunk that GuardChunkSize had to split or truncate.
type Oversize struct {
	// Label is the label of the original chunk.
	Label string
	// Units is the length of the original chunk text in UTF-16 units (the "chars" of the message).
	Units int
	// Parts is the number of parts the chunk became.
	Parts int
	// Truncated is true when an oversize hunk was cut.
	Truncated bool
	// Max is the cap that was applied.
	Max int
}

// Message is the text of the info event for the split (spec 7.5 step 7).
func (o Oversize) Message() string {
	msg := "warning: " + o.Label + " diff (" + strconv.Itoa(o.Units) + " chars) exceeds maxDiffChars=" +
		strconv.Itoa(o.Max) + " → split into " + strconv.Itoa(o.Parts) + " part(s)"
	if o.Truncated {
		msg += "; oversize hunk(s) truncated"
	}
	return msg
}

// GuardChunkSize applies the size cap to every chunk. A chunk within the cap is kept as is; a
// larger one becomes its parts (labelled "<label> (part i/n)" when there are several) and is
// reported in the second result.
func GuardChunkSize(chunks []Chunk, maxUnits int) ([]Chunk, []Oversize) {
	var out []Chunk
	var notes []Oversize
	for _, c := range chunks {
		sp := SplitOversizeDiff(c.DiffText, maxUnits)
		if len(sp.Parts) == 1 && !sp.Truncated {
			out = append(out, c)
			continue
		}
		notes = append(notes, Oversize{
			Label: c.Label, Units: Len16(c.DiffText), Parts: len(sp.Parts), Truncated: sp.Truncated, Max: maxUnits,
		})
		if len(sp.Parts) == 1 {
			out = append(out, Chunk{Label: c.Label, DiffText: sp.Parts[0]})
			continue
		}
		for i, p := range sp.Parts {
			out = append(out, Chunk{
				Label:    c.Label + " (part " + strconv.Itoa(i+1) + "/" + strconv.Itoa(len(sp.Parts)) + ")",
				DiffText: p,
			})
		}
	}
	return out, notes
}

// SliceDiff returns the part of the raw diff that belongs to path: the section whose "diff --git"
// header names it (new path first, old path as a fallback for renames and deletions), never a
// substring match. When the raw text has no such section, the hunk headers of the parsed file are
// rebuilt; when the path is not a file of the diff either, the whole raw diff is returned.
func SliceDiff(d Diff, path string) string {
	return newSectionIndex(d.Raw).slice(d, path)
}

// section is one "diff --git" section of the raw diff: its header paths and its line range.
type section struct {
	a, b       string
	start, end int
}

// sectionIndex is the raw diff split into lines and sections, built once for many slices.
type sectionIndex struct {
	lines    []string
	sections []section
}

func newSectionIndex(raw string) *sectionIndex {
	idx := &sectionIndex{lines: strings.Split(raw, "\n")}
	for i, line := range idx.lines {
		if !strings.HasPrefix(line, "diff --git ") {
			continue
		}
		if n := len(idx.sections); n > 0 {
			idx.sections[n-1].end = i
		}
		a, b, _ := parseDiffGitHeader(line)
		idx.sections = append(idx.sections, section{a: a, b: b, start: i, end: len(idx.lines)})
	}
	return idx
}

func (x *sectionIndex) slice(d Diff, path string) string {
	hit := -1
	for i, s := range x.sections {
		if s.b == path {
			hit = i
			break
		}
	}
	if hit < 0 {
		for i, s := range x.sections {
			if s.a == path {
				hit = i
				break
			}
		}
	}
	if hit >= 0 {
		s := x.sections[hit]
		return strings.Join(x.lines[s.start:s.end], "\n")
	}
	for _, f := range d.Files {
		if f.Path != path {
			continue
		}
		parts := []string{"diff --git a/" + path + " b/" + path, "--- a/" + path, "+++ b/" + path}
		for _, h := range f.Hunks {
			parts = append(parts, "@@ -"+strconv.Itoa(h.OldStart)+","+strconv.Itoa(h.OldLines)+
				" +"+strconv.Itoa(h.NewStart)+","+strconv.Itoa(h.NewLines)+" @@")
		}
		return strings.Join(parts, "\n")
	}
	return d.Raw
}

// jsSpace is JavaScript's \s: the WhiteSpace and LineTerminator code points (Go's \s is ASCII only).
const jsSpace = `\t\n\v\f\r \x{a0}\x{1680}\x{2000}-\x{200a}\x{2028}\x{2029}\x{202f}\x{205f}\x{3000}\x{feff}`

// quotedHeader is the git quoted form `"a/…" "b/…"`; JavaScript's `.` excludes line terminators.
var quotedHeader = regexp.MustCompile(
	`^"((?:[^"\\]|\\[^\n\r\x{2028}\x{2029}])*)"[` + jsSpace + `]+"((?:[^"\\]|\\[^\n\r\x{2028}\x{2029}])*)"$`)

var headerEscape = regexp.MustCompile(`\\(["\\])`)

// parseDiffGitHeader reads the two paths of a "diff --git a/X b/Y" line. Quoted headers decode
// only \" and \\; for unquoted paths with spaces the split where both sides are equal wins
// (not a rename), otherwise the first " b/". ok is false when the line has no a/ and b/ paths.
func parseDiffGitHeader(line string) (a, b string, ok bool) {
	rest := strings.TrimPrefix(line, "diff --git ")
	if m := quotedHeader.FindStringSubmatch(rest); m != nil {
		a, b = headerEscape.ReplaceAllString(m[1], "$1"), headerEscape.ReplaceAllString(m[2], "$1")
	} else {
		u := utf16.Encode([]rune(rest))
		half := (len(u) - 1) / 2
		if len(u) >= 1 && (len(u)-1)%2 == 0 && u[half] == ' ' && equalUnits(u, 2, half, half+3) {
			a, b = decodeUnits(u[:half]), decodeUnits(u[half+1:])
		} else {
			sep := strings.Index(rest, " b/")
			if sep < 0 {
				return "", "", false
			}
			a, b = rest[:sep], rest[sep+1:]
		}
	}
	if !strings.HasPrefix(a, "a/") || !strings.HasPrefix(b, "b/") {
		return "", "", false
	}
	return a[2:], b[2:], true
}

// equalUnits reports whether u[from:to] equals u[from2:], as JavaScript's
// rest.slice(2, half) === rest.slice(half + 3) does (slices clamp to the length).
func equalUnits(u []uint16, from, to, from2 int) bool {
	clamp := func(i int) int { return max(0, min(i, len(u))) }
	x := u[clamp(from):max(clamp(from), clamp(to))]
	y := u[clamp(from2):]
	if len(x) != len(y) {
		return false
	}
	for i := range x {
		if x[i] != y[i] {
			return false
		}
	}
	return true
}

func decodeUnits(u []uint16) string {
	return string(utf16.Decode(u))
}

package domain

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// hunkHeader is the Node parser's "@@ -a[,b] +c[,d] @@" pattern (digits are ASCII, as JS \d).
var hunkHeader = regexp.MustCompile(`^@@ -([0-9]+)(?:,([0-9]+))? \+([0-9]+)(?:,([0-9]+))? @@`)

// maxLineNumber saturates a hunk number that does not fit a JavaScript safe integer.
const maxLineNumber = 1 << 53

// RawDiff rebuilds the raw diff of a pull request from its stored files (spec 7.7.1): files in
// path byte order; each file that has a patch contributes "diff --git a/<p> b/<p>", "--- a/<p>",
// "+++ b/<p>" and the patch; the lines are joined with "\n". No patch at all gives "".
func RawDiff(files []PatchFile) string {
	sorted := make([]PatchFile, len(files))
	copy(sorted, files)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Path < sorted[j].Path })
	var parts []string
	for _, f := range sorted {
		if f.Patch == "" {
			continue
		}
		parts = append(parts, "diff --git a/"+f.Path+" b/"+f.Path, "--- a/"+f.Path, "+++ b/"+f.Path, f.Patch)
	}
	return strings.Join(parts, "\n")
}

// ParseUnifiedDiff parses raw the way the Node server's diff parser does, quirks included: a file
// starts at a "diff --git" line and takes its path from the next "+++ " line (without "b/";
// "/dev/null" keeps the previous path); a line "+++ ..." or "--- ..." never counts as a changed
// line; inside a hunk every line that is not "+" or "-" (blank lines and "\ No newline" markers
// included) is a context line. Files without a path are dropped. Any text parses; an empty diff
// has no files.
func ParseUnifiedDiff(raw string) Diff {
	var files []DiffFile
	var cur *DiffFile
	var hunk *Hunk
	cursor := 0

	flushHunk := func() {
		if cur != nil && hunk != nil {
			cur.Hunks = append(cur.Hunks, *hunk)
		}
		hunk = nil
	}
	flushFile := func() {
		flushHunk()
		if cur != nil {
			files = append(files, *cur)
		}
		cur = nil
	}

	for line := range strings.SplitSeq(raw, "\n") {
		switch {
		case strings.HasPrefix(line, "diff --git"):
			flushFile()
			cur = &DiffFile{}
			continue
		case strings.HasPrefix(line, "+++ "):
			if cur == nil {
				cur = &DiffFile{}
			}
			p := JSTrim(strings.TrimPrefix(line[4:], "b/"))
			if p != "/dev/null" {
				cur.Path = p
			}
			continue
		case strings.HasPrefix(line, "--- "):
			continue
		}
		if strings.HasPrefix(line, "@@ ") {
			if m := hunkHeader.FindStringSubmatch(line); m != nil {
				flushHunk()
				h := Hunk{
					OldStart: parseLineNumber(m[1]), OldLines: 1,
					NewStart: parseLineNumber(m[3]), NewLines: 1,
				}
				if m[2] != "" {
					h.OldLines = parseLineNumber(m[2])
				}
				if m[4] != "" {
					h.NewLines = parseLineNumber(m[4])
				}
				if cur != nil {
					h.File = cur.Path
				}
				hunk = &h
				cursor = h.NewStart
				continue
			}
		}
		if cur == nil || hunk == nil {
			continue
		}
		switch {
		case strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++"):
			cur.Additions++
			hunk.NewLineNumbers = append(hunk.NewLineNumbers, cursor)
			cursor++
		case strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "---"):
			cur.Deletions++
		default:
			hunk.NewLineNumbers = append(hunk.NewLineNumbers, cursor)
			cursor++
		}
	}
	flushFile()

	kept := files[:0]
	for _, f := range files {
		if f.Path != "" {
			kept = append(kept, f)
		}
	}
	return Diff{Raw: raw, Files: kept}
}

func parseLineNumber(digits string) int {
	n, err := strconv.Atoi(digits)
	if err != nil || n > maxLineNumber {
		return maxLineNumber
	}
	return n
}

// ChangedLines is the number of added plus deleted lines of the diff.
func (d Diff) ChangedLines() int {
	n := 0
	for _, f := range d.Files {
		n += f.Additions + f.Deletions
	}
	return n
}

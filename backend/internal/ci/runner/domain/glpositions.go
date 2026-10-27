package domain

import (
	"strconv"
	"strings"

	reviewer "dev-digest-go/backend/internal/reviewer/domain"
)

// DiffPositions answers, for a line of the new side of a unified diff, where GitLab's diff
// position finds it on the old side (spec FR26): a draft note's position names new_path and
// new_line, plus old_path (the file's path before a rename) and, for an unchanged line, old_line.
// Build it with PositionIndex.
type DiffPositions struct {
	files map[string]*filePositions
}

type filePositions struct {
	oldPath string
	spans   []posSpan
}

// posSpan is a run of consecutive new-side lines of one hunk that are all context lines (oldFrom
// is the old-side number of the first one) or all added lines.
type posSpan struct {
	newFrom, count, oldFrom int
	context                 bool
}

// PositionIndex parses the raw unified diff the runner reviews (git diff with rename detection;
// Phase 6 diff parser rules for the new path) and records, per new-side path, the old path and the
// context and added lines of its hunks. Any text parses; hunk bodies are read by the line counts
// of their headers, so a removed line that looks like a file header is still a removed line. Of a
// repeated path the last file wins, as in the payload's line index.
func PositionIndex(raw string) DiffPositions {
	idx := DiffPositions{files: map[string]*filePositions{}}
	var (
		renameFrom, minusPath string
		newPath               string
		haveNew               bool
		cur                   *filePositions
		remOld, remNew        int
		oldLine, newLine      int
	)
	flushFile := func() {
		renameFrom, minusPath, newPath, haveNew, cur = "", "", "", false, nil
		remOld, remNew = 0, 0
	}

	for line := range strings.SplitSeq(raw, "\n") {
		if remOld > 0 || remNew > 0 {
			if cur != nil && recordHunkLine(cur, line, &oldLine, &newLine, &remOld, &remNew) {
				continue
			}
			if cur == nil && skipHunkLine(line, &remOld, &remNew) {
				continue
			}
			// Not a hunk line: the declared counts were wrong; read it as a header.
			remOld, remNew = 0, 0
		}
		switch {
		case strings.HasPrefix(line, "diff --git"):
			flushFile()
		case strings.HasPrefix(line, "rename from "):
			renameFrom = unquoteGitPath(strings.TrimPrefix(line, "rename from "))
		case strings.HasPrefix(line, "--- "):
			minusPath = oldSidePath(line[len("--- "):])
		case strings.HasPrefix(line, "+++ "):
			p := reviewer.JSTrim(strings.TrimPrefix(line[len("+++ "):], "b/"))
			haveNew = p != "/dev/null" && p != ""
			cur = nil
			if haveNew {
				newPath = p
			}
		case strings.HasPrefix(line, "@@ "):
			oldStart, oldCount, newStart, newCount, ok := parseHunkHeader(line)
			if !ok {
				continue
			}
			remOld, remNew = oldCount, newCount
			oldLine, newLine = oldStart, newStart
			if haveNew && cur == nil {
				// First hunk of this file section; an earlier section with the same path is replaced.
				cur = &filePositions{oldPath: pickOldPath(renameFrom, minusPath, newPath)}
				idx.files[newPath] = cur
				if real := strings.TrimPrefix(unquoteGitPath(newPath), "b/"); real != newPath {
					// A quoted path (special characters) is also known by the path it quotes.
					idx.files[real] = cur
				}
			}
		}
	}
	return idx
}

// recordHunkLine consumes one hunk body line for a recorded file. It returns false when the line
// cannot be one (a header or other text), which ends the hunk.
func recordHunkLine(f *filePositions, line string, oldLine, newLine, remOld, remNew *int) bool {
	switch {
	case line == "" || line[0] == ' ':
		f.add(*newLine, *oldLine, true)
		*oldLine++
		*newLine++
		*remOld--
		*remNew--
	case line[0] == '+':
		f.add(*newLine, 0, false)
		*newLine++
		*remNew--
	case line[0] == '-':
		*oldLine++
		*remOld--
	case line[0] == '\\':
	default:
		return false
	}
	return true
}

// skipHunkLine consumes a hunk body line of a file that is not recorded (deleted).
func skipHunkLine(line string, remOld, remNew *int) bool {
	switch {
	case line == "" || line[0] == ' ':
		*remOld--
		*remNew--
	case line[0] == '+':
		*remNew--
	case line[0] == '-':
		*remOld--
	case line[0] == '\\':
	default:
		return false
	}
	return true
}

// add records new-side line n, a context line whose old-side number is old, or an added line.
func (f *filePositions) add(n, old int, context bool) {
	if k := len(f.spans) - 1; k >= 0 {
		s := &f.spans[k]
		if s.context == context && s.newFrom+s.count == n && (!context || s.oldFrom+s.count == old) {
			s.count++
			return
		}
	}
	f.spans = append(f.spans, posSpan{newFrom: n, count: 1, oldFrom: old, context: context})
}

// At returns the old path and old line of the new-side line of file. oldPath is the file's path
// before a rename, else file itself (also for a file the diff does not contain). oldLine is the
// line's number on the old side when the diff shows it as an unchanged context line, else 0: an
// added line has no old line, and neither has a line the diff does not show.
func (p DiffPositions) At(file string, line int) (oldPath string, oldLine int) {
	f := p.files[file]
	if f == nil {
		return file, 0
	}
	for _, s := range f.spans {
		if line >= s.newFrom && line-s.newFrom < s.count {
			if s.context {
				return f.oldPath, s.oldFrom + (line - s.newFrom)
			}
			return f.oldPath, 0
		}
	}
	return f.oldPath, 0
}

// pickOldPath is the old path of a file section: the rename source, else the "--- " path, else the
// new path (an added file, or no usable header).
func pickOldPath(renameFrom, minusPath, newPath string) string {
	switch {
	case renameFrom != "":
		return renameFrom
	case minusPath != "":
		return minusPath
	}
	return newPath
}

// oldSidePath is the path of a "--- " line: without the "a/" prefix and the tab git appends to a
// path with spaces; "" for /dev/null (an added file).
func oldSidePath(s string) string {
	s = reviewer.JSTrim(s)
	if s == "/dev/null" {
		return ""
	}
	return strings.TrimPrefix(unquoteGitPath(s), "a/")
}

// unquoteGitPath undoes git's C-style quoting of a path with special characters; an unquoted or
// malformed path is returned as it is.
func unquoteGitPath(s string) string {
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		if u, err := strconv.Unquote(s); err == nil {
			return u
		}
	}
	return s
}

// parseHunkHeader reads "@@ -a[,b] +c[,d] @@"; a missing count is 1. Numbers saturate instead of
// overflowing.
func parseHunkHeader(line string) (oldStart, oldCount, newStart, newCount int, ok bool) {
	rest, found := strings.CutPrefix(line, "@@ -")
	if !found {
		return 0, 0, 0, 0, false
	}
	oldPart, rest, found := strings.Cut(rest, " +")
	if !found {
		return 0, 0, 0, 0, false
	}
	newPart, _, found := strings.Cut(rest, " @@")
	if !found {
		return 0, 0, 0, 0, false
	}
	oldStart, oldCount, ok = parseRange(oldPart)
	if !ok {
		return 0, 0, 0, 0, false
	}
	newStart, newCount, ok = parseRange(newPart)
	if !ok {
		return 0, 0, 0, 0, false
	}
	return oldStart, oldCount, newStart, newCount, true
}

func parseRange(s string) (start, count int, ok bool) {
	a, b, hasCount := strings.Cut(s, ",")
	start, ok = saturatingAtoi(a)
	if !ok {
		return 0, 0, false
	}
	if !hasCount {
		return start, 1, true
	}
	count, ok = saturatingAtoi(b)
	return start, count, ok
}

const maxHunkNumber = 1 << 31

func saturatingAtoi(s string) (int, bool) {
	if s == "" {
		return 0, false
	}
	n := 0
	for i := range len(s) {
		if s[i] < '0' || s[i] > '9' {
			return 0, false
		}
		if n < maxHunkNumber {
			n = n*10 + int(s[i]-'0')
		}
	}
	return min(n, maxHunkNumber), true
}

// The repo-relative path prefixes whose diff sections the GitLab runner drops (spec FR20, SCR8):
// DevDigest's own exported files, never the code under review. Exactly these two.
const (
	glIgnoredRunnerDir = ".devdigest/"
	glIgnoredJobDir    = ".gitlab/devdigest/"
)

// StripGitLabIgnored removes every "diff --git" section of the raw diff whose new-side path starts
// with ".devdigest/" or ".gitlab/devdigest/", cut and read like StripIgnoredFiles (the path is
// what follows the first " b/" of the header, trimmed as JavaScript trims). A path that git quotes
// (special characters) shows as ` "b/<path>"` and is read the same way. The text of the kept
// sections is returned byte for byte.
func StripGitLabIgnored(raw string) string {
	lines := strings.Split(raw, "\n")
	kept := lines[:0]
	skipping := false
	for _, line := range lines {
		if strings.HasPrefix(line, "diff --git") {
			skipping = isGitLabIgnoredPath(headerNewPath(line)) || isGitLabIgnoredPath(quotedHeaderNewPath(line))
		}
		if !skipping {
			kept = append(kept, line)
		}
	}
	return strings.Join(kept, "\n")
}

func isGitLabIgnoredPath(path string) bool {
	return strings.HasPrefix(path, glIgnoredRunnerDir) || strings.HasPrefix(path, glIgnoredJobDir)
}

// quotedHeaderNewPath is the unquoted new path of a header that ends in git's quoted form
// ` "b/<path>"`; "" when the header does not end in one.
func quotedHeaderNewPath(line string) string {
	i := strings.LastIndex(line, ` "b/`)
	if i < 0 || !strings.HasSuffix(line, `"`) {
		return ""
	}
	return strings.TrimPrefix(unquoteGitPath(line[i+1:]), "b/")
}

package domain

import (
	"strings"

	reviewer "dev-digest-go/backend/internal/reviewer/domain"
)

// MaxDiffBytes caps the PR diff the runner downloads (spec 7.8.5, D6018); a larger body is a hard
// failure. The Node runner read it unbounded.
const MaxDiffBytes = 64 << 20

// The repo-relative path prefixes whose diff sections are dropped before the review: DevDigest's
// own exported files, not the target repository's code. The runner file would make GitHub reject
// an inline comment with 422 and so the whole review, and reviewing the generated workflow is
// noise.
const (
	ignoredRunnerDir   = ".devdigest/"
	ignoredWorkflowDir = ".github/workflows/"
)

func isIgnoredDiffPath(path string) bool {
	return strings.HasPrefix(path, ignoredRunnerDir) || strings.HasPrefix(path, ignoredWorkflowDir)
}

// StripIgnoredFiles removes every "diff --git" section of the raw diff whose new-side path starts
// with ".devdigest/" or ".github/workflows/". GitHub's diff media type emits one header per file,
// so the sections are cut at the headers; the path is what follows the first " b/" of the header
// line, trimmed as JavaScript trims. The text of the kept sections is returned byte for byte.
func StripIgnoredFiles(raw string) string {
	lines := strings.Split(raw, "\n")
	kept := lines[:0]
	skipping := false
	for _, line := range lines {
		if strings.HasPrefix(line, "diff --git") {
			skipping = isIgnoredDiffPath(headerNewPath(line))
		}
		if !skipping {
			kept = append(kept, line)
		}
	}
	return strings.Join(kept, "\n")
}

// headerNewPath is the capture of the Node pattern / b\/(.*)$/ on a "diff --git" line, trimmed;
// "" when the pattern does not match. In JavaScript "." stops at a line terminator, so a " b/"
// followed (anywhere after it) by CR, U+2028 or U+2029 does not match and the next " b/" is tried
// (the line holds no LF: it comes from a split on LF).
func headerNewPath(line string) string {
	from := 0
	for {
		i := strings.Index(line[from:], " b/")
		if i < 0 {
			return ""
		}
		rest := line[from+i+len(" b/"):]
		if !strings.ContainsAny(rest, "\r  ") {
			return reviewer.JSTrim(rest)
		}
		from += i + 1
	}
}

// ParseDiff parses the raw diff the way the Node runner does: the Phase 4 unified-diff rules,
// except that the final line break does not start an empty last line, which would count as a
// context line and widen the last hunk by one line (that would let the grounding gate accept a
// finding one line past the real hunk). Raw keeps the text as given.
func ParseDiff(raw string) reviewer.Diff {
	d := reviewer.ParseUnifiedDiff(strings.TrimSuffix(raw, "\n"))
	d.Raw = raw
	return d
}

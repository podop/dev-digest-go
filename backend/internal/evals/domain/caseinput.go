package domain

import (
	"slices"
	"strconv"
	"strings"

	reviewer "dev-digest-go/backend/internal/reviewer/domain"
)

// LineRange is a contiguous run of new-side line numbers of one file.
type LineRange struct{ Start, End int }

// hasFileHeader is JavaScript's /^(diff --git|\+\+\+ )/m on text. Its ^ matches at the start and
// after every JavaScript line terminator (\n, \r, U+2028, U+2029), which RE2's (?m)^ does not
// (it knows \n only), so the scan is done by hand.
func hasFileHeader(text string) bool {
	atLineStart := true
	for i, r := range text {
		if atLineStart {
			rest := text[i:]
			if strings.HasPrefix(rest, "diff --git") || strings.HasPrefix(rest, "+++ ") {
				return true
			}
		}
		atLineStart = r == '\n' || r == '\r' || r == 0x2028 || r == 0x2029
	}
	return false
}

// NormalizeCaseDiff turns CRLF into LF and wraps a paste that has no file header ("diff --git" or
// "+++ " at a line start) as the single-file diff of file; a diff with headers is kept as given
// (spec 7.4.2 step 2).
func NormalizeCaseDiff(raw, file string) string {
	text := strings.ReplaceAll(raw, "\r\n", "\n")
	if hasFileHeader(text) {
		return text
	}
	return BuildSingleFileDiff(file, text)
}

// newSideRanges returns the new-side lines of file as sorted, merged runs: adjacent line numbers
// join one run.
func newSideRanges(d reviewer.Diff, file string) []LineRange {
	var lines []int
	for _, f := range d.Files {
		if f.Path != file {
			continue
		}
		for _, h := range f.Hunks {
			lines = append(lines, h.NewLineNumbers...)
		}
	}
	slices.Sort(lines)
	lines = slices.Compact(lines)
	ranges := []LineRange{}
	for _, n := range lines {
		if k := len(ranges); k > 0 && n == ranges[k-1].End+1 {
			ranges[k-1].End = n
		} else {
			ranges = append(ranges, LineRange{Start: n, End: n})
		}
	}
	return ranges
}

// CheckCaseContent runs the deterministic checks that keep a case passable (spec 7.4.2 step 3), in
// order: the stored length; a file with a hunk; the expectation file among the diff's files (exact
// path match); the expectation lines intersecting a new-side line of that file. The diff is parsed
// by the review engine's parser. The failure is a *RuleError (422) with the code the client
// branches on; its details carry the paths or ranges the author can pick from (at most
// DiffFilesDetailMax and RangesDetailMax).
func CheckCaseContent(diff string, e Expectation) error {
	if reviewer.Len16(diff) > DiffMaxChars {
		return unprocessable(CodePatchTooLarge, "The diff exceeds "+strconv.Itoa(DiffMaxChars)+" characters", nil)
	}
	parsed := reviewer.ParseUnifiedDiff(diff)
	hasHunk := false
	for _, f := range parsed.Files {
		if len(f.Hunks) > 0 {
			hasHunk = true
			break
		}
	}
	if !hasHunk {
		return unprocessable(CodeDiffInvalid, "The diff has no file with a hunk (@@ header)", nil)
	}
	paths := []string{}
	for _, f := range parsed.Files {
		if !slices.Contains(paths, f.Path) {
			paths = append(paths, f.Path)
		}
	}
	if !slices.Contains(paths, e.File) {
		return unprocessable(CodeFileNotInDiff, "The expectation file is not among the diff files",
			map[string]any{"files": paths[:min(len(paths), DiffFilesDetailMax)]})
	}
	ranges := newSideRanges(parsed, e.File)
	for _, r := range ranges {
		if r.Start <= e.EndLine && e.StartLine <= r.End {
			return nil
		}
	}
	return unprocessable(CodeLinesNotInDiff, "The expectation lines do not intersect a changed or context line of the file",
		map[string]any{"ranges": ranges[:min(len(ranges), RangesDetailMax)]})
}

// titleOrName: a blank or missing PR title is stored as the case name (spec 7.4.2 step 4).
func titleOrName(prTitle *string, name string) string {
	if prTitle == nil || reviewer.JSTrim(*prTitle) == "" {
		return name
	}
	return *prTitle
}

// descriptionOrNil: a blank or missing description is stored as NULL.
func descriptionOrNil(text *string) *string {
	if text == nil || reviewer.JSTrim(*text) == "" {
		return nil
	}
	return strPtr(*text)
}

// BuildManualCase is the snapshot of a from-scratch case. diff is already normalised and checked;
// repo, PR number and head commit are nil and there is no source finding, review or PR.
func BuildManualCase(workspaceID string, kind OwnerKind, ownerID string, in CreateInput, diff string) NewCase {
	e := in.Expectation
	return NewCase{
		WorkspaceID: workspaceID,
		OwnerKind:   kind,
		OwnerID:     ownerID,
		Name:        in.Name,
		Notes:       copyString(in.Notes),
		Expectation: Expectation{
			Kind:      e.Kind,
			File:      e.File,
			StartLine: e.StartLine,
			EndLine:   e.EndLine,
			Severity:  copyString(e.Severity),
			Category:  copyString(e.Category),
			Title:     copyString(e.Title),
		},
		InputDiff: diff,
		InputMeta: InputMeta{
			PRTitle:       titleOrName(in.PRTitle, in.Name),
			PRDescription: descriptionOrNil(in.PRDescription),
		},
	}
}

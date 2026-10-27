package domain

import (
	"slices"
	"strconv"
	"strings"

	reviewer "dev-digest-go/backend/internal/reviewer/domain"
)

// MaxInlineThreads is the most positioned drafts one GitLab run creates (spec FR48).
const MaxInlineThreads = 50

// postedSuffix ends every summary body (Phase 6 7.8.7).
const postedSuffix = "_Posted via DevDigest._"

// InlineCandidate is one inline comment of the review payload together with the finding it comes
// from: the cap orders and lists by the finding.
type InlineCandidate struct {
	Finding reviewer.Finding
	Comment InlineComment
}

// SelectInline splits the inline comments that have an anchor line into the first limit in the
// FR48 order and the rest. The order is severity descending (CRITICAL, WARNING, SUGGESTION, any
// other severity last), then file path in byte order, then start line ascending, then the
// position in cs (the engine's finding order). Both results are in that order; cs is not
// modified. A limit below 1 selects nothing.
func SelectInline(cs []InlineCandidate, limit int) (selected, rest []InlineCandidate) {
	ordered := slices.Clone(cs)
	slices.SortStableFunc(ordered, func(a, b InlineCandidate) int {
		if ra, rb := inlineSeverityRank(a.Finding.Severity), inlineSeverityRank(b.Finding.Severity); ra != rb {
			return rb - ra
		}
		if c := strings.Compare(a.Finding.File, b.Finding.File); c != 0 {
			return c
		}
		switch {
		case a.Finding.StartLine < b.Finding.StartLine:
			return -1
		case a.Finding.StartLine > b.Finding.StartLine:
			return 1
		}
		return 0
	})
	limit = max(limit, 0)
	if len(ordered) <= limit {
		return ordered, nil
	}
	return ordered[:limit:limit], ordered[limit:]
}

func inlineSeverityRank(s reviewer.Severity) int {
	switch s {
	case reviewer.SeverityCritical:
		return 3
	case reviewer.SeverityWarning:
		return 2
	case reviewer.SeveritySuggestion:
		return 1
	}
	return 0
}

// CapSection is the section the summary body gains when comments are left out (spec 7.7): a line
// naming how many findings were not posted inline, then one bullet per finding of rest, in the
// order given (the FR48 order). It is "" for an empty rest. The text of a finding is untrusted;
// the caller passes the whole assembled body through the note body rules.
func CapSection(rest []InlineCandidate) string {
	if len(rest) == 0 {
		return ""
	}
	var b strings.Builder
	noun := " more findings"
	if len(rest) == 1 {
		noun = " more finding"
	}
	b.WriteString("_" + strconv.Itoa(len(rest)) + noun + " not posted inline (limit: " + strconv.Itoa(MaxInlineThreads) + " inline threads per run):_")
	for _, c := range rest {
		f := c.Finding
		loc := f.File + ":" + strconv.Itoa(f.StartLine)
		if f.EndLine != f.StartLine {
			loc += "-" + strconv.Itoa(f.EndLine)
		}
		b.WriteString("\n- " + severityEmoji(f.Severity) + " **" + f.Title + "** (" + strings.ToLower(string(f.Severity)) + ") — `" + loc + "`")
	}
	return b.String()
}

// InsertCapSection puts section between the last finding bullet and "_Posted via DevDigest._" of
// a summary body, one blank line on each side. A body that does not end with that line gets the
// section appended after a blank line. An empty section leaves the body unchanged.
func InsertCapSection(body, section string) string {
	if section == "" {
		return body
	}
	if head, ok := strings.CutSuffix(body, "\n\n"+postedSuffix); ok {
		return head + "\n\n" + section + "\n\n" + postedSuffix
	}
	return body + "\n\n" + section
}

// CapLogLine is the content-free job-log line printed when comments were left out (spec FR48).
func CapLogLine(posted, notPosted int) string {
	return "[agent-runner] inline comments capped: posted=" + strconv.Itoa(posted) + " not_posted=" + strconv.Itoa(notPosted)
}

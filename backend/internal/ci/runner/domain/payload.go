package domain

import (
	"strconv"
	"strings"

	reviewer "dev-digest-go/backend/internal/reviewer/domain"
)

// The review events of the GitHub pull request review API the payload can carry.
const (
	EventApprove        = "APPROVE"
	EventRequestChanges = "REQUEST_CHANGES"
	EventComment        = "COMMENT"
)

// InlineComment is a review comment anchored to a new-side line of the diff.
type InlineComment struct {
	Path string
	Line int
	Body string
}

// ReviewPayload is the review the runner posts, before the app maps APPROVE to COMMENT.
// Comments is nil when no finding has a line GitHub can anchor to.
type ReviewPayload struct {
	Body     string
	Event    string
	Comments []InlineComment
}

// ToReviewPayload turns the grounded findings into the review to post (spec 7.8.7). The event is
// computed from the findings and the gate, never from the model's verdict: APPROVE with no
// findings, REQUEST_CHANGES when the gate triggers, else COMMENT. title is the manifest name.
// Each finding gets one inline comment on a line of the diff that GitHub can resolve; a finding
// without such a line stays in the body only.
func ToReviewPayload(findings []reviewer.Finding, failOn reviewer.FailOn, diff reviewer.Diff, title string) ReviewPayload {
	event := EventComment
	switch {
	case len(findings) == 0:
		event = EventApprove
	case GateTriggered(findings, failOn):
		event = EventRequestChanges
	}
	return ReviewPayload{
		Body:     composeBody(findings, event, title),
		Event:    event,
		Comments: inlineComments(findings, diff),
	}
}

func severityEmoji(s reviewer.Severity) string {
	switch s {
	case reviewer.SeverityCritical:
		return "🔴"
	case reviewer.SeverityWarning:
		return "🟡"
	case reviewer.SeveritySuggestion:
		return "🔵"
	}
	return "•"
}

func severityCounts(findings []reviewer.Finding) string {
	var critical, warning, suggestion int
	for _, f := range findings {
		switch f.Severity {
		case reviewer.SeverityCritical:
			critical++
		case reviewer.SeverityWarning:
			warning++
		case reviewer.SeveritySuggestion:
			suggestion++
		}
	}
	return strconv.Itoa(critical) + " critical · " + strconv.Itoa(warning) + " warning · " + strconv.Itoa(suggestion) + " suggestion"
}

func composeBody(findings []reviewer.Finding, event, title string) string {
	header := "## " + title
	switch event {
	case EventApprove:
		header += " — Approved ✅"
	case EventRequestChanges:
		header += " — Changes requested"
	}
	if len(findings) == 0 {
		return header + "\n\n_No findings. Looks good._"
	}

	lines := make([]string, len(findings))
	for i, f := range findings {
		loc := "`" + f.File + ":" + strconv.Itoa(f.StartLine)
		if f.EndLine != f.StartLine {
			loc += "-" + strconv.Itoa(f.EndLine)
		}
		loc += "`"
		lines[i] = "- " + severityEmoji(f.Severity) + " **" + f.Title + "** (" + strings.ToLower(string(f.Severity)) + ", " +
			string(f.Category) + ") — " + loc + "\n  - " + f.Rationale + suggestionLine(f, "\n  - _Suggestion:_ ")
	}
	noun := " findings"
	if len(findings) == 1 {
		noun = " finding"
	}
	summary := "**" + strconv.Itoa(len(findings)) + noun + "** · " + severityCounts(findings)
	return header + "\n\n" + summary + "\n\n" + strings.Join(lines, "\n") + "\n\n_Posted via DevDigest._"
}

// suggestionLine is prefix + the suggestion, or "" when the finding has none (nil or empty).
func suggestionLine(f reviewer.Finding, prefix string) string {
	if f.Suggestion == nil || *f.Suggestion == "" {
		return ""
	}
	return prefix + *f.Suggestion
}

func inlineComments(findings []reviewer.Finding, diff reviewer.Diff) []InlineComment {
	index := newLineIndex(diff)
	var out []InlineComment
	for _, f := range findings {
		line, ok := index[f.File].resolve(f.StartLine, f.EndLine)
		if !ok {
			continue
		}
		out = append(out, InlineComment{
			Path: f.File,
			Line: line,
			Body: "**" + f.Title + "** (" + strings.ToLower(string(f.Severity)) + ")\n\n" + f.Rationale + suggestionLine(f, "\n\n_Suggestion:_ "),
		})
	}
	return out
}

// lineRange is a span of new-side lines, both ends included.
type lineRange struct{ from, to int }

// fileLines are the new-side lines of one file's hunks, in the order the hunks list them. A hunk
// without listed lines (only deletions) stands for its declared range, which is kept as a span so
// that a huge declared count costs nothing.
type fileLines []lineRange

// newLineIndex maps each path to its lines; like the Node index, the last file of a repeated
// path wins.
func newLineIndex(d reviewer.Diff) map[string]fileLines {
	idx := make(map[string]fileLines, len(d.Files))
	for _, f := range d.Files {
		var lines fileLines
		for _, h := range f.Hunks {
			if len(h.NewLineNumbers) == 0 {
				lines = append(lines, lineRange{h.NewStart, h.NewStart + max(h.NewLines, 1) - 1})
				continue
			}
			for _, n := range h.NewLineNumbers {
				lines = append(lines, lineRange{n, n})
			}
		}
		idx[f.Path] = lines
	}
	return idx
}

func (l fileLines) has(n int) bool {
	for _, r := range l {
		if r.from <= n && n <= r.to {
			return true
		}
	}
	return false
}

// resolve picks the line an inline comment can anchor to: end itself when the diff has it, else
// the diff line within [start, end] (either order) nearest to end; false when the range holds
// none. Grounding only proves that the range meets a hunk, and a
// comment on an unchanged line makes GitHub reject the whole review ("Line could not be
// resolved").
func (l fileLines) resolve(start, end int) (int, bool) {
	if l.has(end) {
		return end, true
	}
	lo, hi := min(start, end), max(start, end)
	best, found := 0, false
	var bestDist uint64
	for _, r := range l {
		from, to := max(r.from, lo), min(r.to, hi)
		if from > to {
			continue
		}
		// end is in no span, so the nearest line of this one is the end of it facing end.
		n := from
		if to < end {
			n = to
		}
		if dist := distance(n, end); !found || dist < bestDist {
			best, bestDist, found = n, dist, true
		}
	}
	return best, found
}

// distance is |a-b| without overflow, whatever the signs.
func distance(a, b int) uint64 {
	if a < b {
		return uint64(b) - uint64(a)
	}
	return uint64(a) - uint64(b)
}

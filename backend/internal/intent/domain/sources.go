package domain

// The sources of a derivation (spec 7.4.3, 7.4.4; Node intent-service.ts collectCoreSources and
// fallbackSources, intent/domain/intent.ts). The reads themselves (GitHub, the clone) belong to
// the application layer; this file builds the Source values from what it read, applies the
// limits, decides derived_from and confidence, and renders the prompt block. All lengths are
// UTF-16 units.

import (
	"regexp"
	"strings"

	reviewer "dev-digest-go/backend/internal/reviewer/domain"
)

var (
	htmlComment = regexp.MustCompile(`(?s)<!--.*?-->`)
	// ^\s{0,3}#{1,6}\s+\S.*$ with JavaScript's \s and ".": a "." does not match \r, U+2028 or
	// U+2029 (a heading line that ends in \r is kept).
	headingLine = regexp.MustCompile(`^[` + jsSpace + `]{0,3}#{1,6}[` + jsSpace + `]+[^` + jsSpace + `][^\n\r\x{2028}\x{2029}]*$`)
	labelRuns   = regexp.MustCompile(`[^a-zA-Z0-9-]+`)
)

func strp(s string) *string { return &s }

// StripBodyBoilerplate removes HTML comments (PR template instructions) and heading lines from a
// PR body and trims the rest.
func StripBodyBoilerplate(body string) string {
	lines := strings.Split(htmlComment.ReplaceAllString(body, ""), "\n")
	kept := lines[:0]
	for _, line := range lines {
		if !headingLine.MatchString(line) {
			kept = append(kept, line)
		}
	}
	return reviewer.JSTrim(strings.Join(kept, "\n"))
}

// IsSubstantiveBody reports whether the body carries real content on its own: SubstantiveBodyMinUnits
// or more units are left after StripBodyBoilerplate.
func IsSubstantiveBody(body string) bool {
	if body == "" {
		return false
	}
	return reviewer.Len16(StripBodyBoilerplate(body)) >= SubstantiveBodyMinUnits
}

// TitleSource is the title source: the title cut to TitleMaxUnits.
func TitleSource(title string) Source {
	return Source{Kind: KindTitle, Ref: KindTitle, Status: StatusUsed, Content: reviewer.Cut(title, TitleMaxUnits)}
}

// BodySource is the body source, cut to BodyMaxUnits (status truncated when that shortened it);
// ok is false for an empty body, which has no source.
func BodySource(body string) (s Source, ok bool) {
	if body == "" {
		return Source{}, false
	}
	text, cut := reviewer.CutTruncated(body, BodyMaxUnits)
	return Source{Kind: KindBody, Ref: KindBody, Status: statusOf(cut), Content: text}, true
}

func statusOf(truncated bool) string {
	if truncated {
		return StatusTruncated
	}
	return StatusUsed
}

// TicketSource is a ticket that was read: "<title>\n\n<body>" cut to TicketMaxUnits. ref is the
// number's string form (TicketRef.Ref).
func TicketSource(ref, issueTitle, issueBody string) Source {
	text, cut := reviewer.CutTruncated(issueTitle+"\n\n"+issueBody, TicketMaxUnits)
	return Source{Kind: KindTicket, Ref: ref, Status: statusOf(cut), Content: text}
}

// FailedSource is a ticket or doc whose read failed; reason is the error text shown on the source.
func FailedSource(kind, ref, reason string) Source {
	return Source{Kind: kind, Ref: ref, Status: StatusFailed, Detail: strp(reason)}
}

// SkippedSources are the skipped references of a kind (KindTicket or KindDoc), in order, each with
// its reason.
func SkippedSources(kind string, skipped []Skipped) []Source {
	out := make([]Source, len(skipped))
	for i, s := range skipped {
		out[i] = Source{Kind: kind, Ref: s.Ref, Status: StatusSkipped, Detail: strp(s.Reason)}
	}
	return out
}

// DocBudget is the unit budget shared by all docs of a derivation (DocTotalMaxUnits). The zero
// value has no budget left; use NewDocBudget.
type DocBudget struct{ left int }

// NewDocBudget returns a full budget.
func NewDocBudget() DocBudget { return DocBudget{left: DocTotalMaxUnits} }

// Exhausted reports whether no budget is left: the next doc is not read but skipped
// (BudgetSkipped).
func (b DocBudget) Exhausted() bool { return b.left <= 0 }

// Left is the remaining budget in units.
func (b DocBudget) Left() int { return b.left }

// BudgetSkipped is the source of a doc that is not read because the budget is gone.
func BudgetSkipped(path string) Source {
	return Source{Kind: KindDoc, Ref: path, Status: StatusSkipped, Detail: strp("doc total budget reached")}
}

// Used turns a doc that was read into its source: the text cut to the smaller of DocMaxUnits and
// the budget left, which then shrinks by the length of the cut text. The status is truncated when
// the cut shortened it or when the read itself was cut (readTruncated).
func (b *DocBudget) Used(path, text string, readTruncated bool) Source {
	per := DocMaxUnits
	if b.left < per {
		per = b.left
	}
	cutText, cut := reviewer.CutTruncated(text, per)
	b.left -= reviewer.Len16(cutText)
	return Source{Kind: KindDoc, Ref: path, Status: statusOf(cut || readTruncated), Content: cutText}
}

// FallbackSources are the commits, branch and changed-files sources added only when the intent is
// inferred: the first CommitsMax commit messages each cut to CommitMaxUnits, joined by "\n" (only
// with at least one commit); the branch cut to BranchMaxUnits (always); and the changed files
// (the first ChangedFilesMax paths under "Changed files:") plus the diff excerpt cut to
// DiffExcerptMaxUnits, joined by a blank line (only when something is left).
func FallbackSources(commits []Commit, branch string, changedFiles []string, diffExcerpt string) []Source {
	var out []Source
	n := len(commits)
	if n > CommitsMax {
		n = CommitsMax
	}
	if n > 0 {
		lines := make([]string, n)
		for i := 0; i < n; i++ {
			lines[i] = reviewer.Cut(commits[i].Message, CommitMaxUnits)
		}
		out = append(out, Source{Kind: KindCommits, Ref: KindCommits, Status: StatusUsed, Content: strings.Join(lines, "\n")})
	}
	out = append(out, Source{Kind: KindBranch, Ref: KindBranch, Status: StatusUsed, Content: reviewer.Cut(branch, BranchMaxUnits)})

	var parts []string
	paths := changedFiles
	if len(paths) > ChangedFilesMax {
		paths = paths[:ChangedFilesMax]
	}
	if len(paths) > 0 {
		parts = append(parts, "Changed files:\n"+strings.Join(paths, "\n"))
	}
	if diffExcerpt != "" {
		if excerpt := reviewer.Cut(diffExcerpt, DiffExcerptMaxUnits); excerpt != "" {
			parts = append(parts, excerpt)
		}
	}
	if len(parts) > 0 {
		out = append(out, Source{Kind: KindDiff, Ref: "changed-files", Status: StatusUsed, Content: strings.Join(parts, "\n\n")})
	}
	return out
}

func isTicketOrDoc(s Source) bool { return s.Kind == KindTicket || s.Kind == KindDoc }

// DerivedFrom is "explicit" when the body is substantive or a ticket or doc was actually loaded
// (status used or truncated), else "inferred". Only the title, body, ticket and doc sources count
// (the core sources, before any fallback is added).
func DerivedFrom(body string, core []Source) string {
	if IsSubstantiveBody(body) {
		return DerivedExplicit
	}
	for _, s := range core {
		if isTicketOrDoc(s) && (s.Status == StatusUsed || s.Status == StatusTruncated) {
			return DerivedExplicit
		}
	}
	return DerivedInferred
}

// ConfidenceFor is set by code, never by the model: low for an inferred intent; for an explicit
// one medium when any ticket or doc source was skipped or failed, else high.
func ConfidenceFor(derivedFrom string, sources []Source) string {
	if derivedFrom == DerivedInferred {
		return ConfidenceLow
	}
	for _, s := range sources {
		if isTicketOrDoc(s) && (s.Status == StatusSkipped || s.Status == StatusFailed) {
			return ConfidenceMedium
		}
	}
	return ConfidenceHigh
}

// StatusCounts counts the sources that were used (used or truncated), skipped and failed: the
// numbers of the "PR intent sources" event.
func StatusCounts(sources []Source) (used, skipped, failed int) {
	for _, s := range sources {
		switch s.Status {
		case StatusUsed, StatusTruncated:
			used++
		case StatusSkipped:
			skipped++
		case StatusFailed:
			failed++
		}
	}
	return used, skipped, failed
}

// IntentSources is the record part of every source, in order (never nil).
func IntentSources(sources []Source) []reviewer.IntentSource {
	out := make([]reviewer.IntentSource, len(sources))
	for i, s := range sources {
		out[i] = s.IntentSource()
	}
	return out
}

func sourceHeading(s Source) string {
	switch s.Kind {
	case KindTitle:
		return "### PR title"
	case KindBody:
		return "### PR description"
	case KindTicket:
		return "### Linked ticket #" + s.Ref
	case KindDoc:
		return "### Linked doc: " + s.Ref
	case KindCommits:
		return "### Commit messages"
	case KindBranch:
		return "### Branch name"
	case KindDiff:
		return "### Changed files"
	}
	return "### " + s.Kind
}

// sourceLabel is the delimiter label of a source: "intent-<kind>-<ref>" with every run of
// characters outside [a-zA-Z0-9-] replaced by "-", lower-cased.
func sourceLabel(s Source) string {
	return strings.ToLower(labelRuns.ReplaceAllString("intent-"+s.Kind+"-"+s.Ref, "-"))
}

// RenderSourcesBlock is the classification prompt's material: for each source whose content has
// non-blank text, its heading and the content in an untrusted block (tag neutralisation as the
// review prompt), joined by a blank line. Skipped and failed sources contribute nothing.
func RenderSourcesBlock(sources []Source) string {
	var parts []string
	for _, s := range sources {
		if reviewer.JSTrim(s.Content) == "" {
			continue
		}
		parts = append(parts, sourceHeading(s)+"\n"+reviewer.WrapUntrusted(sourceLabel(s), s.Content))
	}
	return strings.Join(parts, "\n\n")
}

package domain

// Pure builders of the generation input (spec section 7.9.1 and 7.9.2; Node
// brief/domain/input.ts): changed line ranges from hunk headers, the capped file list, the capped
// findings, the prompt's blast, the linked issue number and the missing-input entries. Nothing
// here reads a file, a database or the network: the use case passes the facts in. No hunk body is
// ever kept.

import (
	"regexp"
	"slices"
	"strconv"
	"strings"

	reviewer "dev-digest-go/backend/internal/reviewer/domain"
)

// maxSafeInteger is JavaScript's Number.MAX_SAFE_INTEGER.
const maxSafeInteger = 1<<53 - 1

// LineRange is an inclusive 1-based line range in head numbering.
type LineRange struct{ Start, End int }

// ChangedFile is a changed file as stored: counts plus all its ranges, taken from its patch.
type ChangedFile struct {
	Path                 string
	Additions, Deletions int
	Ranges               []LineRange
}

// PromptFile is a changed file as the prompt lists it: at most MaxRanges ranges, with its
// smart-diff role.
type PromptFile struct {
	ChangedFile
	Role string
}

// RoleFunc tells the smart-diff role of a path ("core", "tests", "wiring", "docs",
// "boilerplate"); the use case binds it to the smart-diff context (app.Roles).
type RoleFunc func(path string) string

// hunkHeader is the shape of a unified-diff hunk header; only the head side is read.
var hunkHeader = regexp.MustCompile(`^@@ -\d+(?:,\d+)? \+(\d+)(?:,(\d+))? @@`)

// ChangedRanges returns the changed line ranges in head numbering from the "@@" headers of a
// patch only. A pure-deletion hunk ("+c,0") has no head line: it becomes [c, c], clamped to at
// least 1. A header whose numbers do not fit an int saturates (outside the parity range: line
// numbers beyond 2^53 are not real).
func ChangedRanges(patch string) []LineRange {
	if patch == "" {
		return nil
	}
	var out []LineRange
	for _, line := range strings.Split(patch, "\n") {
		m := hunkHeader.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		start := max(1, atoiSat(m[1]))
		count := 1
		if m[2] != "" {
			count = atoiSat(m[2])
		}
		end := start
		if count > 1 {
			end = start + count - 1
		}
		out = append(out, LineRange{Start: start, End: end})
	}
	return out
}

// atoiSat parses a digit string, saturating at the largest int instead of failing.
func atoiSat(s string) int {
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return int(^uint(0) >> 1)
	}
	return int(n)
}

// ChangedFiles turns the stored files into ChangedFile values, in the stored order.
func ChangedFiles(stored []StoredFile) []ChangedFile {
	out := make([]ChangedFile, len(stored))
	for i, f := range stored {
		out[i] = ChangedFile{Path: f.Path, Additions: f.Additions, Deletions: f.Deletions, Ranges: ChangedRanges(f.Patch)}
	}
	return out
}

// SelectedFiles is the file list of the prompt.
type SelectedFiles struct {
	Files []PromptFile
	// More counts the files not listed (rendered as one "<n> more files not listed" line).
	More int
}

// SelectFiles returns the at most MaxFiles files with the most changed lines (additions plus
// deletions, stable on ties), each with at most MaxRanges ranges and its role.
func SelectFiles(files []ChangedFile, role RoleFunc) SelectedFiles {
	order := make([]int, len(files))
	for i := range order {
		order[i] = i
	}
	slices.SortStableFunc(order, func(a, b int) int {
		return (files[b].Additions + files[b].Deletions) - (files[a].Additions + files[a].Deletions)
	})
	if len(order) > MaxFiles {
		order = order[:MaxFiles]
	}
	listed := make([]PromptFile, len(order))
	for i, idx := range order {
		f := files[idx]
		ranges := f.Ranges
		if len(ranges) > MaxRanges {
			ranges = ranges[:MaxRanges]
		}
		f.Ranges = slices.Clone(ranges)
		listed[i] = PromptFile{ChangedFile: f, Role: role(f.Path)}
	}
	return SelectedFiles{Files: listed, More: max(0, len(files)-len(listed))}
}

// FlattenFindings lists the findings of the reviews in review order, each review's findings in
// their order.
func FlattenFindings(reviews []ReviewFacts) []FindingFact {
	var out []FindingFact
	for _, r := range reviews {
		out = append(out, r.Findings...)
	}
	return out
}

func severityRank(s reviewer.Severity) int {
	switch s {
	case reviewer.SeverityCritical:
		return 0
	case reviewer.SeverityWarning:
		return 1
	case reviewer.SeveritySuggestion:
		return 2
	}
	return 3
}

// SelectFindings returns the at most MaxFindings findings, CRITICAL first and stable within a
// severity, with titles cut to MaxFindingTitle.
func SelectFindings(findings []FindingFact) []FindingFact {
	out := slices.Clone(findings)
	slices.SortStableFunc(out, func(a, b FindingFact) int { return severityRank(a.Severity) - severityRank(b.Severity) })
	if len(out) > MaxFindings {
		out = out[:MaxFindings]
	}
	for i := range out {
		out[i].Title = reviewer.Cut(out[i].Title, MaxFindingTitle)
	}
	return out
}

// UsableBlast returns the blast unless it is missing or degraded: a degraded blast is "unknown",
// not data.
func UsableBlast(b *Blast) *Blast {
	if b == nil || b.Degraded {
		return nil
	}
	return b
}

// PromptBlast is what the prompt carries of the blast radius: the summary, every changed symbol
// and at most MaxCallers distinct callers.
type PromptBlast struct {
	Summary        string
	ChangedSymbols []BlastSymbol
	Callers        []BlastCaller
}

// NewPromptBlast builds the prompt's blast: callers distinct by "<file>:<line>:<name>" across the
// downstream groups in order, the first MaxCallers of them.
func NewPromptBlast(b Blast) PromptBlast {
	seen := map[string]bool{}
	var callers []BlastCaller
	for _, g := range b.Downstream {
		for _, c := range g.Callers {
			key := c.File + ":" + strconv.Itoa(c.Line) + ":" + c.Name
			if seen[key] {
				continue
			}
			seen[key] = true
			callers = append(callers, c)
		}
	}
	if len(callers) > MaxCallers {
		callers = callers[:MaxCallers]
	}
	return PromptBlast{Summary: b.Summary, ChangedSymbols: b.ChangedSymbols, Callers: callers}
}

// issueRef is the rule of the GitHub adapter for "linked_issue": the first "#<digits>", no
// boundary.
var issueRef = regexp.MustCompile(`#(\d+)`)

// LinkedIssueNumber returns the first "#N" of the pull request description when N is a safe
// integer above zero.
func LinkedIssueNumber(body *string) (int, bool) {
	if body == nil || *body == "" {
		return 0, false
	}
	m := issueRef.FindStringSubmatch(*body)
	if m == nil {
		return 0, false
	}
	n, err := strconv.ParseUint(m[1], 10, 64)
	if err != nil || n == 0 || n > maxSafeInteger {
		return 0, false
	}
	return int(n), true
}

// PromptDescription returns the description of the prompt: the body cut to MaxDescription when
// it is not empty, else "".
func PromptDescription(body *string) string {
	if body == nil || *body == "" {
		return ""
	}
	return reviewer.Cut(*body, MaxDescription)
}

// IssueFact is the linked issue as the prompt carries it.
type IssueFact struct {
	Number int
	Title  string
	// Body is cut to MaxIssueBody; "" when the issue has none.
	Body string
}

// NewIssueFact builds the prompt's issue: the body cut to MaxIssueBody when it is not empty.
func NewIssueFact(i Issue) IssueFact {
	f := IssueFact{Number: i.Number, Title: i.Title}
	if i.Body != nil && *i.Body != "" {
		f.Body = reviewer.Cut(*i.Body, MaxIssueBody)
	}
	return f
}

// SpecText is a readable spec document of the prompt.
type SpecText struct{ Path, Text string }

// MergeDocuments merges the documents of several reviews in review order: the first occurrence
// of a path wins.
func MergeDocuments(perReview ...[]Document) []Document {
	seen := map[string]bool{}
	var out []Document
	for _, docs := range perReview {
		for _, d := range docs {
			if seen[d.Path] {
				continue
			}
			seen[d.Path] = true
			out = append(out, d)
		}
	}
	return out
}

// IncludedSpecs returns the included documents that have text, in the given order, as the
// prompt's specs.
func IncludedSpecs(docs []Document) []SpecText {
	var out []SpecText
	for _, d := range docs {
		if d.Status == DocIncluded {
			out = append(out, SpecText{Path: d.Path, Text: d.Text})
		}
	}
	return out
}

// InputFacts are the facts of one generation, as the use case gathered them.
type InputFacts struct {
	Pull Pull
	// Files are all the changed files of the pull request (ChangedFiles of the stored files).
	Files []ChangedFile
	// Findings are all the findings of the current reviews (FlattenFindings).
	Findings []FindingFact
	// Intent is the stored intent; nil when there is none.
	Intent *reviewer.Intent
	// Blast is the blast as read (degraded or not); nil when the read failed.
	Blast *Blast
	// Issue is the fetched linked issue; nil when not linked or not fetched.
	Issue *Issue
	// Docs are the merged documents (MergeDocuments).
	Docs []Document
}

// BuildInput assembles the prompt input from the facts: description and issue body cut to their
// caps, files and findings selected, the blast reduced to the usable one, only the included
// documents as specs.
func BuildInput(f InputFacts, role RoleFunc) BriefInput {
	sel := SelectFiles(f.Files, role)
	in := BriefInput{
		Title:       f.Pull.Title,
		Description: PromptDescription(f.Pull.Body),
		HeadSHA:     f.Pull.HeadSHA,
		Files:       sel.Files,
		MoreFiles:   sel.More,
		Intent:      f.Intent,
		Findings:    SelectFindings(f.Findings),
		Specs:       IncludedSpecs(f.Docs),
	}
	if b := UsableBlast(f.Blast); b != nil {
		pb := NewPromptBlast(*b)
		in.Blast = &pb
	}
	if f.Issue != nil {
		is := NewIssueFact(*f.Issue)
		in.Issue = &is
	}
	return in
}

// IssueOutcome says how the linked issue turned out.
type IssueOutcome string

// The issue outcomes.
const (
	IssueNotLinked   IssueOutcome = "not_linked"
	IssueFetched     IssueOutcome = "fetched"
	IssueFetchFailed IssueOutcome = "fetch_failed"
)

// IssueOutcomeOf is the outcome for a description that links an issue or not and a fetch that
// delivered an issue or not.
func IssueOutcomeOf(linked, fetched bool) IssueOutcome {
	switch {
	case !linked:
		return IssueNotLinked
	case fetched:
		return IssueFetched
	}
	return IssueFetchFailed
}

// SpecsOutcome is the outcome of the spec-document step, counted after the budget trim.
type SpecsOutcome struct {
	// ReviewedAgents counts the current reviews of the pull request (agent-less ones included).
	ReviewedAgents int
	// AttachedDocs counts the distinct documents attached to them.
	AttachedDocs int
	// Unreadable counts documents with status missing, too_large or unreadable.
	Unreadable int
	// OverBudget counts documents with status over_budget plus the ones the prompt trim dropped.
	OverBudget int
}

// NewSpecsOutcome counts the merged documents of reviews current reviews; dropped is the number
// of documents the prompt trim removed (TrimResult.DroppedSpecs).
func NewSpecsOutcome(reviews int, docs []Document, dropped int) SpecsOutcome {
	o := SpecsOutcome{ReviewedAgents: reviews, AttachedDocs: len(docs), OverBudget: dropped}
	for _, d := range docs {
		switch d.Status {
		case DocMissing, DocTooLarge, DocUnreadable:
			o.Unreadable++
		case DocOverBudget:
			o.OverBudget++
		}
	}
	return o
}

// MissingInputFacts are what BuildMissingInputs decides on.
type MissingInputFacts struct {
	// Description is the pull request body; nil, empty or blank counts as no description.
	Description *string
	// HasIntent is true when a stored intent exists.
	HasIntent bool
	// Blast is the blast as read, degraded or not; nil when the read failed.
	Blast *Blast
	Specs SpecsOutcome
	Issue IssueOutcome
}

// BuildMissingInputs lists every absent or dropped input with its reason, in order: description,
// intent, blast, specs, linked issue. It is empty when the brief had everything.
func BuildMissingInputs(f MissingInputFacts) []MissingInput {
	var out []MissingInput
	if f.Description == nil || reviewer.JSTrim(*f.Description) == "" {
		out = append(out, MissingInput{Input: InputDescription, Reason: ReasonEmpty})
	}
	if !f.HasIntent {
		out = append(out, MissingInput{Input: InputIntent, Reason: ReasonNotDerived})
	}
	switch {
	case f.Blast == nil:
		out = append(out, MissingInput{Input: InputBlast, Reason: ReasonDegraded})
	case f.Blast.Degraded:
		out = append(out, MissingInput{Input: InputBlast, Reason: ReasonDegraded, Detail: f.Blast.Reason})
	}
	s := f.Specs
	switch {
	case s.ReviewedAgents == 0:
		out = append(out, MissingInput{Input: InputSpecs, Reason: ReasonNoReviewRun})
	case s.AttachedDocs == 0:
		out = append(out, MissingInput{Input: InputSpecs, Reason: ReasonNoneAttached})
	default:
		if s.OverBudget > 0 {
			out = append(out, MissingInput{Input: InputSpecs, Reason: ReasonOverBudget, Detail: strconv.Itoa(s.OverBudget) + " of " + strconv.Itoa(s.AttachedDocs)})
		}
		if s.Unreadable > 0 {
			out = append(out, MissingInput{Input: InputSpecs, Reason: ReasonUnreadable, Detail: strconv.Itoa(s.Unreadable) + " of " + strconv.Itoa(s.AttachedDocs)})
		}
	}
	switch f.Issue {
	case IssueNotLinked:
		out = append(out, MissingInput{Input: InputLinkedIssue, Reason: ReasonNotLinked})
	case IssueFetchFailed:
		out = append(out, MissingInput{Input: InputLinkedIssue, Reason: ReasonFetchFailed})
	}
	return out
}

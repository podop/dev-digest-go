package domain

import (
	"strconv"

	reviewer "dev-digest-go/backend/internal/reviewer/domain"
)

// RuleKind says how a RuleError is reported: the use cases map it to an application error kind.
type RuleKind int

// The kinds of RuleError.
const (
	// RuleUnprocessable is Node's ValidationError: an HTTP 422 with a code.
	RuleUnprocessable RuleKind = iota + 1
	// RuleBadRequest is Node's InvalidInputError: an HTTP 400 with a code.
	RuleBadRequest
)

// The codes of the case rules (spec section 7.2).
const (
	CodeFindingUndecided = "finding_undecided"
	CodeFindingNoAgent   = "finding_no_agent"
	CodePatchUnavailable = "patch_unavailable"
	CodePatchTooLarge    = "patch_too_large"
	CodeDiffInvalid      = "diff_invalid"
	CodeFileNotInDiff    = "expectation_file_not_in_diff"
	CodeLinesNotInDiff   = "expectation_lines_not_in_diff"
	CodeValidationError  = "validation_error"
	CodeNotComparable    = "runs_not_comparable"
)

// RuleError is a broken pure rule of the context. Message is the user-visible text of the spec and
// Details the structured detail the response carries (nil when none): "files" ([]string) for
// CodeFileNotInDiff and "ranges" ([]LineRange) for CodeLinesNotInDiff.
type RuleError struct {
	Kind    RuleKind
	Code    string
	Message string
	Details map[string]any
}

// Error implements error.
func (e *RuleError) Error() string { return e.Message }

func unprocessable(code, message string, details map[string]any) *RuleError {
	return &RuleError{Kind: RuleUnprocessable, Code: code, Message: message, Details: details}
}

// BuildSingleFileDiff is the stored patch of one file as a valid single-file unified diff: the same
// header the pull-request store writes, so the diff parser sees a/<path> and b/<path>.
func BuildSingleFileDiff(path, patch string) string {
	return "diff --git a/" + path + " b/" + path + "\n--- a/" + path + "\n+++ b/" + path + "\n" + patch
}

// CaseName is the default case name: the finding title, <file>:<start_line> when the title is
// blank, cut to CaseNameMax UTF-16 units.
func CaseName(title, file string, startLine int) string {
	name := reviewer.JSTrim(title)
	if name == "" {
		name = file + ":" + strconv.Itoa(startLine)
	}
	return reviewer.PrefixUnits(name, CaseNameMax)
}

// BuildFromFinding validates the finding (decided, has an agent, has a usable patch) and builds the
// case to insert (spec 7.4.1 steps 3 and 4). The first broken rule is a *RuleError (422).
func BuildFromFinding(workspaceID string, src FindingSource) (NewCase, error) {
	var kind ExpectationKind
	switch {
	case src.AcceptedAt != nil:
		kind = MustFind
	case src.DismissedAt != nil:
		kind = MustNotFlag
	default:
		return NewCase{}, unprocessable(CodeFindingUndecided, "Accept or dismiss the finding first", nil)
	}
	if src.AgentID == nil {
		return NewCase{}, unprocessable(CodeFindingNoAgent, "The review of this finding has no agent", nil)
	}
	if src.Patch == nil || reviewer.JSTrim(*src.Patch) == "" {
		return NewCase{}, unprocessable(CodePatchUnavailable, "The PR has no stored patch for this file", nil)
	}
	if reviewer.Len16(*src.Patch) > DiffMaxChars {
		return NewCase{}, unprocessable(CodePatchTooLarge, "The patch exceeds "+strconv.Itoa(DiffMaxChars)+" characters", nil)
	}
	severity, category, title := src.Severity, src.Category, src.Title
	repo, headSHA, prNumber := src.RepoFullName, src.HeadSHA, src.PRNumber
	return NewCase{
		WorkspaceID: workspaceID,
		OwnerKind:   OwnerAgent,
		OwnerID:     *src.AgentID,
		Name:        CaseName(src.Title, src.File, src.StartLine),
		Expectation: Expectation{
			Kind:      kind,
			File:      src.File,
			StartLine: src.StartLine,
			EndLine:   max(src.EndLine, src.StartLine),
			Severity:  &severity,
			Category:  &category,
			Title:     &title,
		},
		InputDiff: BuildSingleFileDiff(src.File, *src.Patch),
		InputMeta: InputMeta{
			PRTitle:       src.PRTitle,
			PRDescription: copyString(src.PRBody),
			Repo:          &repo,
			PRNumber:      &prNumber,
			HeadSHA:       &headSHA,
		},
		SourceFindingID: strPtr(src.FindingID),
		SourceReviewID:  strPtr(src.ReviewID),
		SourcePRID:      strPtr(src.PRID),
	}, nil
}

func strPtr(s string) *string { return &s }

// copyString returns a pointer to a copy of *p, nil for nil.
func copyString(p *string) *string {
	if p == nil {
		return nil
	}
	return strPtr(*p)
}

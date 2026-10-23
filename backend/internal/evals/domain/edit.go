package domain

import "time"

// EditMerge is the result of merging a PATCH into a stored case.
type EditMerge struct {
	// Case is the stored case with the edit applied; ContentEditedAt is stamped when the edit
	// really changed some content value and it was still unset.
	Case Case
	// Recheck says the edit carries diff, file or lines: the merged content must pass
	// CheckCaseContent.
	Recheck bool
	// ContentChanged says some content value (not name or notes) really differs from the stored one.
	ContentChanged bool
}

// MergeEdit merges a PATCH into the stored case (spec 7.4.3). An absent field keeps the stored
// value and a null clears severity, category, title and the description; a merged end_line below
// start_line is a *RuleError (400 validation_error), so changing only one bound is validated
// too; a blank or null pr_title becomes the merged name; a new diff is normalised against the
// merged file. now stamps ContentEditedAt. The stored case is not modified.
func MergeEdit(stored Case, patch UpdateInput, now time.Time) (EditMerge, error) {
	linesEdited := patch.StartLine != nil || patch.EndLine != nil

	start, end := stored.Expectation.StartLine, stored.Expectation.EndLine
	if patch.StartLine != nil {
		start = *patch.StartLine
	}
	if patch.EndLine != nil {
		end = *patch.EndLine
	}
	if linesEdited && end < start {
		return EditMerge{}, &RuleError{Kind: RuleBadRequest, Code: CodeValidationError, Message: "end_line must be >= start_line"}
	}

	s := stored.Expectation
	exp := Expectation{
		Kind:      s.Kind,
		File:      s.File,
		StartLine: start,
		EndLine:   end,
		Severity:  mergeField(patch.Severity, s.Severity),
		Category:  mergeField(patch.Category, s.Category),
		Title:     mergeField(patch.Title, s.Title),
	}
	if patch.Kind != nil {
		exp.Kind = *patch.Kind
	}
	if patch.File != nil {
		exp.File = *patch.File
	}

	name := stored.Name
	if patch.Name != nil {
		name = *patch.Name
	}
	inputDiff := stored.InputDiff
	if patch.InputDiff != nil {
		inputDiff = NormalizeCaseDiff(*patch.InputDiff, exp.File)
	}
	meta := stored.InputMeta
	meta.PRDescription = copyString(meta.PRDescription)
	if patch.PRTitle.Set {
		meta.PRTitle = titleOrName(patch.PRTitle.Value, name)
	}
	if patch.PRDescription.Set {
		meta.PRDescription = descriptionOrNil(patch.PRDescription.Value)
	}

	contentChanged := exp.Kind != s.Kind ||
		exp.File != s.File ||
		exp.StartLine != s.StartLine ||
		exp.EndLine != s.EndLine ||
		!equalPtr(exp.Severity, s.Severity) ||
		!equalPtr(exp.Category, s.Category) ||
		!equalPtr(exp.Title, s.Title) ||
		inputDiff != stored.InputDiff ||
		meta.PRTitle != stored.InputMeta.PRTitle ||
		!equalPtr(meta.PRDescription, stored.InputMeta.PRDescription)

	merged := stored
	merged.Name = name
	if patch.Notes.Set {
		merged.Notes = copyString(patch.Notes.Value)
	} else {
		merged.Notes = copyString(stored.Notes)
	}
	merged.Expectation = exp
	merged.InputDiff = inputDiff
	merged.InputMeta = meta
	if contentChanged && merged.ContentEditedAt == nil {
		t := now
		merged.ContentEditedAt = &t
	}
	return EditMerge{
		Case:           merged,
		Recheck:        patch.InputDiff != nil || patch.File != nil || linesEdited,
		ContentChanged: contentChanged,
	}, nil
}

// mergeField applies a nullable PATCH field to a stored value: absent keeps it, null clears it.
func mergeField(f Field[string], stored *string) *string {
	if !f.Set {
		return copyString(stored)
	}
	return copyString(f.Value)
}

// equalPtr compares two optional strings by value.
func equalPtr(a, b *string) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

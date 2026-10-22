package domain

import "slices"

// TrimResult is a prompt input cut to the budget.
type TrimResult struct {
	Input BriefInput
	// DroppedSpecs counts the whole spec documents dropped to fit; they become specs/over_budget.
	DroppedSpecs int
	// DroppedFiles counts the files taken off the list to fit; they join the "more files" line.
	DroppedFiles int
	// EstTokens is the estimated input of the trimmed input; it may still exceed the budget (the
	// changed symbols are uncapped).
	EstTokens int
}

// TrimToBudget keeps the input within maxTokens (spec 7.9.3; the use case passes
// InputBudgetTokens): while the estimate is above the budget and spec documents remain, the last
// document goes; then, while above the budget and files remain, the last listed file goes and
// MoreFiles grows by one. The caps of the facts (callers, issue, description) are already
// applied by BuildInput. The input given is not modified.
func TrimToBudget(in BriefInput, maxTokens int) TrimResult {
	cur := in
	tokens := EstimateInputTokens(cur)
	res := TrimResult{}
	for tokens > maxTokens && len(cur.Specs) > 0 {
		cur.Specs = cur.Specs[:len(cur.Specs)-1]
		res.DroppedSpecs++
		tokens = EstimateInputTokens(cur)
	}
	for tokens > maxTokens && len(cur.Files) > 0 {
		cur.Files = cur.Files[:len(cur.Files)-1]
		cur.MoreFiles++
		res.DroppedFiles++
		tokens = EstimateInputTokens(cur)
	}
	// The caller may keep and mutate its own slices; the result owns its (shorter) views.
	cur.Specs = slices.Clip(cur.Specs)
	cur.Files = slices.Clip(cur.Files)
	res.Input = cur
	res.EstTokens = tokens
	return res
}

// SpecPaths returns the paths of the input's spec documents (the brief's specs_used).
func SpecPaths(in BriefInput) []string {
	out := make([]string, len(in.Specs))
	for i, s := range in.Specs {
		out[i] = s.Path
	}
	return out
}

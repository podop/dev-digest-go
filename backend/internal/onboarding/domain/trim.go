package domain

import rdomain "dev-digest-go/backend/internal/reviewer/domain"

const (
	charsPerToken = 4
	// trimSlackUnits is slack for the delimiters and heading a shrunk source still carries.
	trimSlackUnits = 64
)

// shrinkText keeps the first len-(over*4+64) units of text; "" when nothing is left (the source
// is then absent).
func shrinkText(text string, overTokens int) string {
	if text == "" {
		return ""
	}
	keep := rdomain.Len16(text) - (overTokens*charsPerToken + trimSlackUnits)
	if keep <= 0 {
		return ""
	}
	return rdomain.PrefixUnits(text, keep)
}

// shrinkLines is shrinkText for the repo map, additionally cut back to before the last line
// break (when that index is above 0).
func shrinkLines(text string, overTokens int) string {
	keep := rdomain.Len16(text) - (overTokens*charsPerToken + trimSlackUnits)
	if keep <= 0 {
		return ""
	}
	cut := rdomain.PrefixUnits(text, keep)
	for i := len(cut) - 1; i > 0; i-- {
		if cut[i] == '\n' {
			return cut[:i]
		}
	}
	return cut
}

// TrimToBudget keeps the whole input within maxTokens estimated tokens. Trim order, least
// valuable first: the README, then the compose file (each shrunk by the excess, dropped when
// nothing is left), then the TODO lines from the end, then, only if still over, the repo map.
// The input is not modified; the unchanged parts of the result share its backing storage.
func TrimToBudget(input TourInput, maxTokens int) TourInput {
	cur := input
	over := func() int { return EstimateInputTokens(cur) - maxTokens }
	if o := over(); o > 0 {
		cur.Readme = shrinkText(cur.Readme, o)
	}
	if o := over(); o > 0 {
		cur.Compose = shrinkText(cur.Compose, o)
	}
	if over() > 0 {
		kept := append([]TodoLine(nil), cur.TodoLines...)
		for len(kept) > 0 && over() > 0 {
			kept = kept[:len(kept)-1]
			cur.TodoLines = kept
		}
		cur.TodoLines = kept
	}
	for cur.RepoMap != "" {
		o := over()
		if o <= 0 {
			break
		}
		cur.RepoMap = shrinkLines(cur.RepoMap, o)
	}
	return cur
}

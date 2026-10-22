package domain

// penalty is the score a finding of the severity costs; an unknown severity costs nothing.
func penalty(s Severity) int {
	switch s {
	case SeverityCritical:
		return 35
	case SeverityWarning:
		return 12
	case SeveritySuggestion:
		return 3
	}
	return 0
}

// severityRank is the rank a severity has for the CI gate; an unknown severity ranks 0.
func severityRank(s Severity) int {
	switch s {
	case SeverityCritical:
		return 3
	case SeverityWarning:
		return 2
	case SeveritySuggestion:
		return 1
	}
	return 0
}

// ScoreFromFindings is the review score (spec FR4029): 100 minus the severity penalties (CRITICAL
// 35, WARNING 12, SUGGESTION 3) of the kept findings, clamped to 0-100. It replaces the model's
// own score, which drifts between models.
func ScoreFromFindings(findings []Finding) int {
	total := 0
	for _, f := range findings {
		total += penalty(f.Severity)
	}
	return max(0, min(100, 100-total))
}

// CountBlockers is the number of findings whose severity rank reaches the CI gate's minimum
// (any 1, warning 2, critical 3); never, and any unknown gate, count none.
func CountBlockers(findings []Finding, failOn FailOn) int {
	var minRank int
	switch failOn {
	case FailOnAny:
		minRank = 1
	case FailOnWarning:
		minRank = 2
	case FailOnCritical:
		minRank = 3
	default:
		return 0
	}
	n := 0
	for _, f := range findings {
		if severityRank(f.Severity) >= minRank {
			n++
		}
	}
	return n
}

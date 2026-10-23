package domain

import reviewer "dev-digest-go/backend/internal/reviewer/domain"

// CountBlockers is the number of grounded findings whose severity rank (SUGGESTION 1, WARNING 2,
// CRITICAL 3) reaches the minimum of the gate: any 1, warning 2, critical 3. never, and any value
// that is not a gate, count none. It is the model-independent signal the exit code and the review
// event follow; the model's own verdict is never used.
func CountBlockers(findings []reviewer.Finding, failOn reviewer.FailOn) int {
	return reviewer.CountBlockers(findings, failOn)
}

// GateTriggered reports whether at least one finding is a blocker under failOn: the review then
// requests changes and the runner exits 1.
func GateTriggered(findings []reviewer.Finding, failOn reviewer.FailOn) bool {
	return CountBlockers(findings, failOn) > 0
}

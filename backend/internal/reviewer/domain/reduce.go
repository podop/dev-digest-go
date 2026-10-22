package domain

import (
	"math"
	"strings"
)

// verdictRank orders verdicts for the reduce step; an unknown verdict ranks like approve.
func verdictRank(v Verdict) int {
	switch v {
	case VerdictRequestChanges:
		return 2
	case VerdictComment:
		return 1
	}
	return 0
}

// ReduceReviews merges the partial reviews of a map-reduce run into one (spec FR4026): findings
// concatenated in partial order, the worst verdict, the rounded mean of the model scores, and the
// non-empty summaries joined with one space. A single partial is returned unchanged; no partial
// gives an empty approve with score 0.
func ReduceReviews(partials []Review) Review {
	if len(partials) == 1 {
		return partials[0]
	}
	var findings []Finding
	verdict := VerdictApprove
	sum := 0
	var summaries []string
	for _, p := range partials {
		findings = append(findings, p.Findings...)
		if verdictRank(p.Verdict) > verdictRank(verdict) {
			verdict = p.Verdict
		}
		sum += p.Score
		if p.Summary != "" {
			summaries = append(summaries, p.Summary)
		}
	}
	score := 0
	if len(partials) > 0 {
		score = jsRound(float64(sum) / float64(len(partials)))
	}
	return Review{Verdict: verdict, Summary: strings.Join(summaries, " "), Score: score, Findings: findings}
}

// jsRound is Math.round: halves round toward positive infinity.
func jsRound(x float64) int {
	f := math.Floor(x)
	if x-f >= 0.5 {
		f++
	}
	return int(f)
}

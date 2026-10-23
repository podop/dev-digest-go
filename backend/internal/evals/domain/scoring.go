package domain

import "math"

// MetricDecimals and CostDecimals are the decimals metrics and deltas, and money, are rounded to
// (the cost matches numeric(12, 6)).
const (
	MetricDecimals = 4
	CostDecimals   = 6
)

// jsRound is Math.round: the nearest integer, ties toward +Infinity.
func jsRound(x float64) float64 {
	f := math.Floor(x)
	if x-f >= 0.5 {
		f++
	}
	return f
}

// RoundTo is Node's Math.round(value * 10^decimals) / 10^decimals. The product is a runtime
// float64 operation, as in JavaScript. Negative zero is returned as zero: JSON.stringify writes
// "0" for it and Go's encoder "-0".
func RoundTo(value float64, decimals int) float64 {
	factor := math.Pow10(decimals)
	scaled := float64(value * factor)
	r := jsRound(scaled) / factor
	if r == 0 {
		return 0
	}
	return r
}

// RoundMetric rounds a metric to MetricDecimals.
func RoundMetric(value float64) float64 { return RoundTo(value, MetricDecimals) }

// MetricChange is next - prev rounded to decimals; nil when either side is nil.
func MetricChange(prev, next *float64, decimals int) *float64 {
	if prev == nil || next == nil {
		return nil
	}
	d := RoundTo(*next-*prev, decimals)
	return &d
}

// RangesIntersect reports whether two inclusive line ranges intersect, either bound order (like
// the grounding gate).
func RangesIntersect(aStart, aEnd, bStart, bEnd int) bool {
	aLo, aHi := min(aStart, aEnd), max(aStart, aEnd)
	bLo, bHi := min(bStart, bEnd), max(bStart, bEnd)
	return aLo <= bHi && bLo <= aHi
}

// KeptFinding is a finding the engine kept (grounded), reduced to what scoring and display need.
type KeptFinding struct {
	File               string
	StartLine, EndLine int
	Severity, Category *string
	Title              *string
}

// MatchesExpectation reports whether a kept finding matches the expectation: the same file and
// intersecting line ranges.
func MatchesExpectation(e Expectation, f KeptFinding) bool {
	return f.File == e.File && RangesIntersect(f.StartLine, f.EndLine, e.StartLine, e.EndLine)
}

// CaseScore is the scoring of one case from its kept findings (the case is never an error here).
// Status is CasePassed or CaseFailed.
type CaseScore struct {
	Status                                   CaseStatus
	Findings                                 []ResultFinding
	HitCount, ViolationCount, UnlabeledCount int
}

// ScoreCase scores one case: a matching finding is a hit (must_find) or a violation
// (must_not_flag), every other kept finding is unlabeled. must_find passes with at least one
// hit, must_not_flag with no violation. Findings is never nil.
func ScoreCase(e Expectation, kept []KeptFinding) CaseScore {
	label := MatchViolation
	if e.Kind == MustFind {
		label = MatchHit
	}
	s := CaseScore{Findings: make([]ResultFinding, 0, len(kept))}
	for _, f := range kept {
		match := MatchUnlabeled
		switch {
		case !MatchesExpectation(e, f):
			s.UnlabeledCount++
		case label == MatchHit:
			match = MatchHit
			s.HitCount++
		default:
			match = MatchViolation
			s.ViolationCount++
		}
		s.Findings = append(s.Findings, ResultFinding{
			File: f.File, StartLine: f.StartLine, EndLine: f.EndLine,
			Severity: f.Severity, Category: f.Category, Title: f.Title, Match: match,
		})
	}
	passed := s.ViolationCount == 0
	if e.Kind == MustFind {
		passed = s.HitCount >= 1
	}
	s.Status = CaseFailed
	if passed {
		s.Status = CasePassed
	}
	return s
}

// CaseOutcome is what the run aggregate needs from one finished case.
type CaseOutcome struct {
	Kind                                     ExpectationKind
	Status                                   CaseStatus
	HitCount, ViolationCount, UnlabeledCount int
	DroppedCount                             int
	CostUSD                                  *float64
	Error                                    *string
}

// RunTotals are the aggregate scores of a run over its non-error cases and the counters that go
// with them. A metric with a zero denominator is nil.
type RunTotals struct {
	Metrics                   Metrics
	CasesPassed, CasesErrored int
}

func ratio(numerator, denominator int) *float64 {
	if denominator == 0 {
		return nil
	}
	r := RoundMetric(float64(numerator) / float64(denominator))
	return &r
}

// AggregateRun aggregates a run over its non-error cases: recall = must_find cases passed /
// must_find cases; precision = hits / (hits + violations); citation accuracy = kept / (kept +
// dropped). The cost sums every case that reported one, an errored case's spend included, and is
// nil when none did.
func AggregateRun(outcomes []CaseOutcome) RunTotals {
	var mustFind, mustFindPassed, passed, scored int
	var hits, violations, unlabeled, dropped int
	var costs []float64
	for _, o := range outcomes {
		if o.CostUSD != nil {
			costs = append(costs, *o.CostUSD)
		}
		if o.Status == CaseError {
			continue
		}
		scored++
		if o.Status == CasePassed {
			passed++
		}
		if o.Kind == MustFind {
			mustFind++
			if o.Status == CasePassed {
				mustFindPassed++
			}
		}
		hits += o.HitCount
		violations += o.ViolationCount
		unlabeled += o.UnlabeledCount
		dropped += o.DroppedCount
	}
	kept := hits + violations + unlabeled
	t := RunTotals{
		Metrics: Metrics{
			Recall:            ratio(mustFindPassed, mustFind),
			Precision:         ratio(hits, hits+violations),
			CitationAccuracy:  ratio(kept, kept+dropped),
			UnlabeledFindings: unlabeled,
		},
		CasesPassed:  passed,
		CasesErrored: len(outcomes) - scored,
	}
	if len(costs) > 0 {
		sum := 0.0
		for _, c := range costs {
			sum += c
		}
		cost := RoundTo(sum, CostDecimals)
		t.Metrics.CostUSD = &cost
	}
	return t
}

// RunVerdict is the final status of a run and its error text (nil for none).
type RunVerdict struct {
	Status RunStatus
	Error  *string
}

// Verdict is RunFailed with the first case error (EveryCaseErrored when no case has a message)
// when every outcome errored, otherwise RunCompleted. No outcome at all (every case deleted) is
// completed.
func Verdict(outcomes []CaseOutcome) RunVerdict {
	if len(outcomes) == 0 {
		return RunVerdict{Status: RunCompleted}
	}
	for _, o := range outcomes {
		if o.Status != CaseError {
			return RunVerdict{Status: RunCompleted}
		}
	}
	msg := EveryCaseErrored
	for _, o := range outcomes {
		if o.Error != nil && *o.Error != "" {
			msg = *o.Error
			break
		}
	}
	return RunVerdict{Status: RunFailed, Error: &msg}
}

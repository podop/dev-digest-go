package domain

import (
	"math"
	"slices"
	"time"
)

// Kpis folds the KPIs of one window (spec FR11). span is [From, To) of the window the rows belong
// to; rows are that window's rows; f.Pulls and f.Runs cover both windows, so the merge and cost
// figures are filtered by span here.
//
//   - PRsReviewed is the row count.
//   - Merged counts every pull with a merge time in span, reviewed or not (EC2). A merged pull
//     without a merge time is counted in no window (EC3).
//   - AvgHealth is the rounded mean of the rows' health (nil when no row has one).
//   - TimeToFirstReviewS is the median over the rows whose first review lies in span.
//   - TimeToMergeS is the median over the pulls merged in span that have an opened time.
//   - CriticalFound and OpenCritical sum the rows' snapshot figures.
//   - ReviewCostUSD sums the cost of the runs started in span (unknown cost = 0), rounded to
//     MoneyDecimals.
//
// Medians of an even count are the truncated mean of the two middle values (whole seconds).
func Kpis(span Span, rows []Row, f Facts) KpiSet {
	k := KpiSet{PRsReviewed: len(rows)}

	var healthSum, healthN int
	var firstReview []int64
	for _, r := range rows {
		if r.Health != nil {
			healthSum += *r.Health
			healthN++
		}
		if r.FirstReviewAt != nil && span.Contains(*r.FirstReviewAt) && r.TimeToFirstReviewS != nil {
			firstReview = append(firstReview, *r.TimeToFirstReviewS)
		}
		k.CriticalFound += r.Findings.Critical
		k.OpenCritical += r.OpenCritical
	}
	if healthN > 0 {
		avg := int(math.Round(float64(healthSum) / float64(healthN)))
		k.AvgHealth = &avg
	}
	k.TimeToFirstReviewS = aggMedian(firstReview)

	var toMerge []int64
	for _, p := range f.Pulls {
		if p.MergedAt == nil || !span.Contains(*p.MergedAt) {
			continue
		}
		k.Merged++
		if p.OpenedAt != nil {
			toMerge = append(toMerge, int64(p.MergedAt.Sub(*p.OpenedAt)/time.Second))
		}
	}
	k.TimeToMergeS = aggMedian(toMerge)

	var cost float64
	for _, run := range f.Runs {
		if run.CostUSD != nil && span.Contains(run.RanAt) {
			cost += *run.CostUSD
		}
	}
	k.ReviewCostUSD = aggRound(cost, MoneyDecimals)
	return k
}

// aggMedian returns the median of vs (whole seconds), nil for an empty slice. vs is not modified.
func aggMedian(vs []int64) *int64 {
	if len(vs) == 0 {
		return nil
	}
	s := slices.Clone(vs)
	slices.Sort(s)
	var m int64
	if n := len(s); n%2 == 1 {
		m = s[n/2]
	} else {
		m = (s[n/2-1] + s[n/2]) / 2
	}
	return &m
}

// aggRound rounds v half away from zero to the given number of decimals.
func aggRound(v float64, decimals int) float64 {
	p := math.Pow10(decimals)
	return math.Round(v*p) / p
}

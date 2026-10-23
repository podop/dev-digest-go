package domain

// metricOf reads one metric of a run.
func metricOf(r Run, m Metric) *float64 {
	switch m {
	case MetricRecall:
		return r.Metrics.Recall
	case MetricPrecision:
		return r.Metrics.Precision
	default:
		return r.Metrics.CitationAccuracy
	}
}

// RegressionAlert names every metric of the latest finished run that fell by at least
// -RegressionThreshold against the previous finished run, in the order recall, precision, citation
// accuracy. Deltas are rounded first (0.92 to 0.90 counts); a metric that is nil on either side
// cannot regress. It is nil when a run is missing or nothing dropped enough.
func RegressionAlert(latest, previous *Run) []MetricDelta {
	if latest == nil || previous == nil {
		return nil
	}
	var alert []MetricDelta
	for _, m := range []Metric{MetricRecall, MetricPrecision, MetricCitationAccuracy} {
		d := MetricChange(metricOf(*previous, m), metricOf(*latest, m), MetricDecimals)
		if d != nil && *d <= RegressionThreshold {
			alert = append(alert, MetricDelta{Metric: m, Delta: *d})
		}
	}
	return alert
}

// window returns the first limit runs of a newest-first list, oldest first (chart order).
func window(newestFirst []Run, limit int) []Run {
	n := max(0, min(len(newestFirst), limit))
	out := make([]Run, 0, n)
	for i := n - 1; i >= 0; i-- {
		out = append(out, newestFirst[i])
	}
	return out
}

// Trend is the last limit finished runs of a newest-first list, oldest first. Never nil.
func Trend(finishedNewestFirst []Run, limit int) []Run { return window(finishedNewestFirst, limit) }

// Sparkline is the last SparklineMax finished runs of a newest-first list as chart points, oldest
// first. Never nil.
func Sparkline(finishedNewestFirst []Run) []SparkPoint {
	runs := window(finishedNewestFirst, SparklineMax)
	out := make([]SparkPoint, 0, len(runs))
	for _, r := range runs {
		out = append(out, SparkPoint{
			RunID:  r.ID,
			Recall: r.Metrics.Recall, Precision: r.Metrics.Precision, CitationAccuracy: r.Metrics.CitationAccuracy,
		})
	}
	return out
}

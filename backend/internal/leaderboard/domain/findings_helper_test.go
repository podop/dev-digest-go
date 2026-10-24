package domain_test

import "dev-digest-go/backend/internal/leaderboard/domain"

// setFindings fills f.Counts and f.TopFindings from raw findings, the way the store hands them
// over: one ReviewCounts per review that has findings (all states; the not-dismissed ones; the
// open criticals) and every finding as a top-finding candidate (the store sends at most three per
// PR and snapshot; the domain re-applies the cap, so more candidates must give the same rows).
// The expected values of the tests are computed by hand from the findings, not by this helper.
func setFindings(f *domain.Facts, findings []domain.FindingFact) {
	byReview := make(map[string]*domain.ReviewCounts)
	var order []string
	for _, fi := range findings {
		c, ok := byReview[fi.ReviewID]
		if !ok {
			c = &domain.ReviewCounts{ReviewID: fi.ReviewID}
			byReview[fi.ReviewID] = c
			order = append(order, fi.ReviewID)
		}
		bump(&c.All, fi.Severity)
		if fi.DismissedAt == nil {
			bump(&c.Live, fi.Severity)
			if fi.Severity == domain.SeverityCritical && fi.AcceptedAt == nil {
				c.OpenCritical++
			}
		}
	}
	f.Counts = f.Counts[:0:0]
	for _, id := range order {
		f.Counts = append(f.Counts, *byReview[id])
	}
	f.TopFindings = append(f.TopFindings[:0:0], findings...)
}

func bump(c *domain.FindingCounts, severity string) {
	switch severity {
	case domain.SeverityCritical:
		c.Critical++
	case domain.SeverityWarning:
		c.Warning++
	case domain.SeveritySuggestion:
		c.Suggestion++
	}
}

package domain

import (
	"cmp"
	"math"
	"slices"
	"time"
)

// BuildRows folds one Facts read into the rows of the window (cur) and of the previous window
// (prev), spec FR8-FR10. A PR is a row of a window when at least one of its reviews (kind
// review) was created in the window's half-open range. The snapshot of a row is the newest
// review per agent created in that range; reviews of deleted agents are one agent. The result
// is not ranked: it is ordered by repo, number and PR id only so that equal input gives equal
// output; call Rank to order it for the page.
//
// Facts must cover [win.PrevFrom, win.To); reviews, runs and findings outside a window's range
// are ignored by that window. now is the only clock (open age, derived status).
func BuildRows(win Window, f Facts, now time.Time) (cur, prev []Row) {
	in := newRowInput(f)
	return in.rows(win.Cur(), now), in.rows(win.Prev(), now)
}

// rowInput holds Facts indexed by the keys the folds join on.
type rowInput struct {
	pulls    []PullFact
	reviews  map[string][]ReviewFact  // by PR id
	findings map[string][]FindingFact // by review id
	runs     map[string][]RunFact     // by PR id
}

func newRowInput(f Facts) rowInput {
	in := rowInput{
		pulls:    f.Pulls,
		reviews:  make(map[string][]ReviewFact),
		findings: make(map[string][]FindingFact),
		runs:     make(map[string][]RunFact),
	}
	for _, r := range f.Reviews {
		in.reviews[r.PRID] = append(in.reviews[r.PRID], r)
	}
	for _, fi := range f.Findings {
		in.findings[fi.ReviewID] = append(in.findings[fi.ReviewID], fi)
	}
	for _, r := range f.Runs {
		in.runs[r.PRID] = append(in.runs[r.PRID], r)
	}
	return in
}

// rows builds the rows of one window range.
func (in rowInput) rows(span Span, now time.Time) []Row {
	rows := make([]Row, 0)
	seen := make(map[string]bool, len(in.pulls))
	for _, p := range in.pulls {
		if seen[p.PRID] {
			continue
		}
		reviews := reviewsInSpan(in.reviews[p.PRID], span)
		if len(reviews) == 0 {
			continue
		}
		seen[p.PRID] = true
		rows = append(rows, in.row(p, reviews, span, now))
	}
	slices.SortFunc(rows, func(a, b Row) int {
		return cmp.Or(cmp.Compare(a.Repo, b.Repo), cmp.Compare(a.Number, b.Number), cmp.Compare(a.PRID, b.PRID))
	})
	return rows
}

// reviewsInSpan keeps the reviews created in span.
func reviewsInSpan(all []ReviewFact, span Span) []ReviewFact {
	var out []ReviewFact
	for _, r := range all {
		if span.Contains(r.CreatedAt) {
			out = append(out, r)
		}
	}
	return out
}

// row builds one row from a PR and its (non-empty) reviews of the window.
func (in rowInput) row(p PullFact, reviews []ReviewFact, span Span, now time.Time) Row {
	snapshot, rounds := windowSnapshot(reviews)

	row := Row{
		PRID:                  p.PRID,
		RepoID:                p.RepoID,
		Repo:                  p.Repo,
		Number:                p.Number,
		Title:                 p.Title,
		Author:                p.Author,
		Status:                rowStatus(p, now),
		Draft:                 p.Draft,
		Additions:             p.Additions,
		Deletions:             p.Deletions,
		FilesCount:            p.FilesCount,
		Size:                  sizeOfPull(p),
		Rounds:                rounds,
		CostUSD:               runCostIn(in.runs[p.PRID], span),
		NewCommitsSinceReview: p.LastReviewedSHA != nil && *p.LastReviewedSHA != p.HeadSHA,
		OpenedAt:              p.OpenedAt,
		MergedAt:              p.MergedAt,
		FirstReviewAt:         p.FirstReviewAt,
		Reviews:               make([]RowReview, 0, len(snapshot)),
	}

	var scoreSum, scoreN, riskPoints int
	for _, r := range snapshot {
		rr := RowReview{
			ReviewID:  r.ReviewID,
			AgentName: r.AgentName,
			Score:     r.Score,
			CreatedAt: r.CreatedAt,
		}
		if r.AgentID == "" {
			rr.AgentName = DeletedAgentName
		} else {
			id := r.AgentID
			rr.AgentID = &id
		}
		for _, fi := range in.findings[r.ReviewID] {
			rr.Items = append(rr.Items, FindingItem{
				File:        fi.File,
				Severity:    fi.Severity,
				AcceptedAt:  fi.AcceptedAt,
				DismissedAt: fi.DismissedAt,
			})
			addSeverity(&rr.Findings, fi.Severity)
			if fi.DismissedAt == nil {
				riskPoints += severityWeight(fi.Severity)
				if fi.Severity == SeverityCritical && fi.AcceptedAt == nil {
					row.OpenCritical++
				}
			}
		}
		row.Findings.Critical += rr.Findings.Critical
		row.Findings.Warning += rr.Findings.Warning
		row.Findings.Suggestion += rr.Findings.Suggestion
		if r.Score != nil {
			scoreSum += *r.Score
			scoreN++
		}
		row.Reviews = append(row.Reviews, rr)
	}
	slices.SortFunc(row.Reviews, func(a, b RowReview) int {
		return cmp.Or(cmp.Compare(a.AgentName, b.AgentName), cmp.Compare(a.ReviewID, b.ReviewID))
	})

	if scoreN > 0 {
		h := int(math.Round(float64(scoreSum) / float64(scoreN)))
		row.Health = &h
	}
	row.Risk = riskOfPoints(riskPoints)
	row.TimeToFirstReviewS = rowSecondsBetween(p.OpenedAt, p.FirstReviewAt)
	if row.Status == StatusMerged {
		row.TimeToMergeS = rowSecondsBetween(p.OpenedAt, p.MergedAt)
	}
	if row.Status != StatusMerged && row.Status != StatusClosed && p.OpenedAt != nil {
		age := int64(now.Sub(*p.OpenedAt) / time.Second)
		row.OpenAgeS = &age
	}
	return row
}

// windowSnapshot returns the newest review per agent (a later CreatedAt wins, then the larger
// review id) and the highest number of reviews any one agent wrote. Reviews of deleted agents
// (AgentID "") are one agent.
func windowSnapshot(reviews []ReviewFact) (snapshot []ReviewFact, rounds int) {
	newest := make(map[string]ReviewFact)
	counts := make(map[string]int)
	for _, r := range reviews {
		counts[r.AgentID]++
		rounds = max(rounds, counts[r.AgentID])
		cur, ok := newest[r.AgentID]
		if !ok || r.CreatedAt.After(cur.CreatedAt) || (r.CreatedAt.Equal(cur.CreatedAt) && r.ReviewID > cur.ReviewID) {
			newest[r.AgentID] = r
		}
	}
	for _, r := range newest {
		snapshot = append(snapshot, r)
	}
	return snapshot, rounds
}

// rowStatus is merged or closed as stored, else the status the PR list derives (plan P1): no
// review of the current head is needs_review, a reviewed head not updated for more than
// StaleAfter is stale, else reviewed.
func rowStatus(p PullFact, now time.Time) string {
	if p.Status == StatusMerged || p.Status == StatusClosed {
		return p.Status
	}
	if p.LastReviewedSHA == nil || *p.LastReviewedSHA != p.HeadSHA {
		return StatusNeedsReview
	}
	if p.UpdatedAt != nil && now.Sub(*p.UpdatedAt) > StaleAfter {
		return StatusStale
	}
	return StatusReviewed
}

// sizeOfPull buckets additions + deletions; SizeNone when all three stats are 0 (EC9).
func sizeOfPull(p PullFact) Size {
	if p.Additions == 0 && p.Deletions == 0 && p.FilesCount == 0 {
		return SizeNone
	}
	switch lines := p.Additions + p.Deletions; {
	case lines <= SizeMaxS:
		return SizeS
	case lines <= SizeMaxM:
		return SizeM
	case lines <= SizeMaxL:
		return SizeL
	default:
		return SizeXL
	}
}

// riskOfPoints maps the weighted points of the non-dismissed findings to a level.
func riskOfPoints(points int) Risk {
	switch {
	case points >= RiskHighFrom:
		return RiskHigh
	case points >= RiskMediumFrom:
		return RiskMedium
	default:
		return RiskLow
	}
}

func severityWeight(severity string) int {
	switch severity {
	case SeverityCritical:
		return WeightCritical
	case SeverityWarning:
		return WeightWarning
	case SeveritySuggestion:
		return WeightSuggestion
	}
	return 0
}

func addSeverity(c *FindingCounts, severity string) {
	switch severity {
	case SeverityCritical:
		c.Critical++
	case SeverityWarning:
		c.Warning++
	case SeveritySuggestion:
		c.Suggestion++
	}
}

// runCostIn sums the cost of the runs started in span; an unknown cost is 0 (plan Q3).
func runCostIn(runs []RunFact, span Span) float64 {
	var sum float64
	for _, r := range runs {
		if r.CostUSD != nil && span.Contains(r.RanAt) {
			sum += *r.CostUSD
		}
	}
	scale := math.Pow10(MoneyDecimals)
	return math.Round(sum*scale) / scale
}

// rowSecondsBetween is to - from in whole seconds, nil when either instant is unknown.
func rowSecondsBetween(from, to *time.Time) *int64 {
	if from == nil || to == nil {
		return nil
	}
	s := int64(to.Sub(*from) / time.Second)
	return &s
}

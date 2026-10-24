package domain

import (
	"cmp"
	"math"
	"slices"
	"time"
)

// BuildRows folds one Facts read into the rows of the window (cur) and of the previous window
// (prev; nil when the window has none, 30d), spec FR8-FR10. A PR is a row of a window when at least one of its reviews (kind
// review) was created in the window's half-open range. The snapshot of a row is the newest
// review per agent created in that range; reviews of deleted agents are one agent. The result
// is not ranked: it is ordered by repo, number and PR id only so that equal input gives equal
// output; call Rank to order it for the page.
//
// Facts must cover [win.ReadFrom(), win.To); reviews, runs and findings outside a window's range
// are ignored by that window. now is the only clock (open age, derived status).
func BuildRows(win Window, f Facts, now time.Time) (cur, prev []Row) {
	in := newRowInput(f)
	cur = in.rows(win.Cur(), now)
	if win.HasPrev {
		prev = in.rows(win.Prev(), now)
	}
	return cur, prev
}

// rowInput holds Facts indexed by the keys the folds join on.
type rowInput struct {
	pulls   []PullFact
	reviews map[string][]ReviewFact  // by PR id
	counts  map[string]ReviewCounts  // by review id
	top     map[string][]FindingFact // by review id
	runs    map[string][]RunFact     // by PR id
}

func newRowInput(f Facts) rowInput {
	in := rowInput{
		pulls:   f.Pulls,
		reviews: make(map[string][]ReviewFact),
		counts:  make(map[string]ReviewCounts, len(f.Counts)),
		top:     make(map[string][]FindingFact, len(f.TopFindings)),
		runs:    make(map[string][]RunFact),
	}
	for _, r := range f.Reviews {
		in.reviews[r.PRID] = append(in.reviews[r.PRID], r)
	}
	for _, c := range f.Counts {
		in.counts[c.ReviewID] = c
	}
	for _, fi := range f.TopFindings {
		in.top[fi.ReviewID] = append(in.top[fi.ReviewID], fi)
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

	var healthLost, riskSum int
	var top []TopFinding
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
		c := in.counts[r.ReviewID]
		rr.Findings = c.All
		healthLost += healthPoints(c.Live)
		riskSum += riskPoints(c.Live)
		row.OpenCritical += c.OpenCritical
		for _, fi := range in.top[r.ReviewID] {
			top = append(top, TopFinding{
				FindingID: fi.FindingID,
				Severity:  fi.Severity,
				Title:     fi.Title,
				File:      fi.File,
				StartLine: fi.StartLine,
				AgentName: rr.AgentName,
				State:     findingState(fi),
			})
		}
		row.Findings.Critical += rr.Findings.Critical
		row.Findings.Warning += rr.Findings.Warning
		row.Findings.Suggestion += rr.Findings.Suggestion
		row.Reviews = append(row.Reviews, rr)
	}
	slices.SortFunc(row.Reviews, func(a, b RowReview) int {
		return cmp.Or(cmp.Compare(a.AgentName, b.AgentName), cmp.Compare(a.ReviewID, b.ReviewID))
	})

	if rounds > 0 {
		h := max(0, HealthBase-healthLost-HealthPerExtraRound*(rounds-1))
		row.Health = &h
	}
	row.TopFindings = topFindings(top)
	row.Risk = riskOfPoints(riskSum)
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

// findingState is the triage state of a finding; dismissed wins over accepted (EC7).
func findingState(f FindingFact) string {
	switch {
	case f.DismissedAt != nil:
		return FindingDismissed
	case f.AcceptedAt != nil:
		return FindingAccepted
	}
	return FindingOpen
}

// topFindings keeps the TopFindingsLimit most notable findings: severity (CRITICAL first), then
// state (open, accepted, dismissed), then file, start line and finding id. It never returns nil.
// The store selects its candidates with the same order in SQL (ORDER BY of the top-findings
// query in adapters/postgres, string columns in byte order = Go's cmp.Compare on strings).
func topFindings(all []TopFinding) []TopFinding {
	slices.SortFunc(all, func(a, b TopFinding) int {
		return cmp.Or(
			cmp.Compare(severityOrder(a.Severity), severityOrder(b.Severity)),
			cmp.Compare(stateOrder(a.State), stateOrder(b.State)),
			cmp.Compare(a.File, b.File),
			cmp.Compare(a.StartLine, b.StartLine),
			cmp.Compare(a.FindingID, b.FindingID),
		)
	})
	n := min(len(all), TopFindingsLimit)
	return append(make([]TopFinding, 0, n), all[:n]...)
}

func severityOrder(severity string) int {
	switch severity {
	case SeverityCritical:
		return 0
	case SeverityWarning:
		return 1
	case SeveritySuggestion:
		return 2
	}
	return 3
}

func stateOrder(state string) int {
	switch state {
	case FindingOpen:
		return 0
	case FindingAccepted:
		return 1
	}
	return 2
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

// sizeOfPull buckets additions + deletions (XS < 50, S < 200, M < 500, L < 1000, XL above);
// SizeNone when all three stats are 0 (EC9).
func sizeOfPull(p PullFact) Size {
	if p.Additions == 0 && p.Deletions == 0 && p.FilesCount == 0 {
		return SizeNone
	}
	switch lines := p.Additions + p.Deletions; {
	case lines < SizeBelowS:
		return SizeXS
	case lines < SizeBelowM:
		return SizeS
	case lines < SizeBelowL:
		return SizeM
	case lines < SizeBelowXL:
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

// healthPoints is what the counted (non-dismissed) findings take off the health (spec FR10).
func healthPoints(c FindingCounts) int {
	return c.Critical*HealthCritical + c.Warning*HealthWarning + c.Suggestion*HealthSuggestion
}

// riskPoints is the weighted sum of the counted (non-dismissed) findings (spec FR10).
func riskPoints(c FindingCounts) int {
	return c.Critical*WeightCritical + c.Warning*WeightWarning + c.Suggestion*WeightSuggestion
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

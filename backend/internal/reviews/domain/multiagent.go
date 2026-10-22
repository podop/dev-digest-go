package domain

import (
	"fmt"
	"math"
	"sort"
	"time"

	"golang.org/x/text/collate"
	"golang.org/x/text/language"

	reviewer "dev-digest-go/backend/internal/reviewer/domain"
)

// VerdictIgnored is the verdict of a take whose agent flagged nothing in a location group.
const VerdictIgnored = "ignored"

// ErrInvalidLimit is returned by ListLimit for a limit outside 1..MultiAgentListLimit. The use
// case maps it to 400 with the code invalid_limit and the message of the error.
var ErrInvalidLimit = fmt.Errorf("limit must be between 1 and %d", MultiAgentListLimit)

// ListLimit resolves the limit of the multi-agent run list: nil is the default
// (MultiAgentListLimit), anything outside 1..MultiAgentListLimit is ErrInvalidLimit.
func ListLimit(limit *int) (int, error) {
	if limit == nil {
		return MultiAgentListLimit, nil
	}
	if *limit < 1 || *limit > MultiAgentListLimit {
		return 0, ErrInvalidLimit
	}
	return *limit, nil
}

// OrderColumns returns the columns ordered by agent name ascending in English collation order
// (what JavaScript's localeCompare(…, 'en') gives: case-insensitive first, punctuation not
// ignored), ties by run id in UTF-16 order. The input is not modified.
func OrderColumns(columns []AgentColumn) []AgentColumn {
	out := append([]AgentColumn{}, columns...)
	// A Collator is not safe for concurrent use, so each call makes its own.
	coll := collate.New(language.English)
	sort.SliceStable(out, func(i, j int) bool {
		if c := coll.CompareString(out[i].AgentName, out[j].AgentName); c != 0 {
			return c < 0
		}
		return CompareUTF16(out[i].RunID, out[j].RunID) < 0
	})
	return out
}

// RunTotals returns the wall clock of the parallel fan-out (the longest column, a null duration
// counted as 0) and the summed cost (nil when no column has one).
func RunTotals(columns []AgentColumn) (durationMS int, costUSD *float64) {
	sum, priced := 0.0, false
	for _, c := range columns {
		if c.DurationMS != nil && *c.DurationMS > durationMS {
			durationMS = *c.DurationMS
		}
		if c.CostUSD != nil {
			sum += *c.CostUSD
			priced = true
		}
	}
	if !priced {
		return durationMS, nil
	}
	return durationMS, &sum
}

// SortFindings orders findings by file (UTF-16 order), start line, then id (UTF-16 order): the
// order of a multi-agent column's findings. It sorts in place.
func SortFindings(findings []FindingRecord) {
	sort.SliceStable(findings, func(i, j int) bool {
		a, b := findings[i], findings[j]
		if c := CompareUTF16(a.File, b.File); c != 0 {
			return c < 0
		}
		if a.StartLine != b.StartLine {
			return a.StartLine < b.StartLine
		}
		return CompareUTF16(a.ID, b.ID) < 0
	})
}

// BuildMultiAgentRun assembles the comparison read model from the parent row and its columns:
// columns ordered (OrderColumns), the agent count, the totals and the location groups. Columns
// and Conflicts are never nil.
func BuildMultiAgentRun(header MultiAgentHeader, columns []AgentColumn) MultiAgentRun {
	ordered := OrderColumns(columns)
	duration, cost := RunTotals(ordered)
	return MultiAgentRun{
		MultiAgentHeader: header,
		AgentCount:       len(ordered),
		TotalDurationMS:  duration,
		TotalCostUSD:     cost,
		Columns:          ordered,
		Conflicts:        GroupFindings(ordered),
	}
}

func severityRank(s reviewer.Severity) int {
	switch s {
	case reviewer.SeverityCritical:
		return 3
	case reviewer.SeverityWarning:
		return 2
	case reviewer.SeveritySuggestion:
		return 1
	}
	return 0
}

// top returns the highest-severity finding; ties keep the earlier one. Nil for none.
func top(findings []FindingRecord) *FindingRecord {
	var best *FindingRecord
	for i := range findings {
		if best == nil || severityRank(findings[i].Severity) > severityRank(best.Severity) {
			best = &findings[i]
		}
	}
	return best
}

type located struct {
	finding     FindingRecord
	columnIndex int
}

// GroupFindings groups the findings of the done columns by location (spec 7.3). All their
// findings are sorted by file (UTF-16 order), start line, column index; a finding joins the
// current group when its file equals the group's and its start line is at most the group's
// running maximum end line. A group yields the smallest start line, the largest end line, the
// title of the highest-severity per-column best (ties to the earlier column), and one take per
// done column in column order. The group is a conflict unless every done column flagged it with
// the same best severity. The result is never nil.
func GroupFindings(columns []AgentColumn) []Conflict {
	var done []AgentColumn
	for _, c := range columns {
		if c.Status == StatusDone {
			done = append(done, c)
		}
	}
	var all []located
	for i, c := range done {
		for _, f := range c.Findings {
			all = append(all, located{finding: f, columnIndex: i})
		}
	}
	sort.SliceStable(all, func(i, j int) bool {
		a, b := all[i], all[j]
		if c := CompareUTF16(a.finding.File, b.finding.File); c != 0 {
			return c < 0
		}
		if a.finding.StartLine != b.finding.StartLine {
			return a.finding.StartLine < b.finding.StartLine
		}
		return a.columnIndex < b.columnIndex
	})

	var groups [][]located
	var current []located
	currentEnd := 0
	for _, item := range all {
		f := item.finding
		if len(current) > 0 && f.File == current[0].finding.File && f.StartLine <= currentEnd {
			current = append(current, item)
			currentEnd = max(currentEnd, f.EndLine)
			continue
		}
		if len(current) > 0 {
			groups = append(groups, current)
		}
		current = []located{item}
		currentEnd = f.EndLine
	}
	if len(current) > 0 {
		groups = append(groups, current)
	}

	conflicts := make([]Conflict, 0, len(groups))
	for _, g := range groups {
		conflicts = append(conflicts, conflictOf(g, done))
	}
	return conflicts
}

func conflictOf(group []located, done []AgentColumn) Conflict {
	takes := make([]ConflictTake, len(done))
	var bests []FindingRecord
	for i, col := range done {
		mine := []FindingRecord{}
		for _, g := range group {
			if g.columnIndex == i {
				mine = append(mine, g.finding)
			}
		}
		take := ConflictTake{
			AgentID: col.AgentID, Persona: col.AgentName, RunID: col.RunID,
			Verdict: VerdictIgnored, FindingIDs: make([]string, len(mine)),
		}
		if best := top(mine); best != nil {
			take.Verdict, take.Note = string(best.Severity), best.Title
			bests = append(bests, *best)
		}
		for k, f := range mine {
			take.FindingIDs[k] = f.ID
		}
		takes[i] = take
	}

	c := Conflict{
		File:    group[0].finding.File,
		Line:    group[0].finding.StartLine,
		EndLine: group[0].finding.EndLine,
		Takes:   takes,
	}
	for _, g := range group[1:] {
		c.Line = min(c.Line, g.finding.StartLine)
		c.EndLine = max(c.EndLine, g.finding.EndLine)
	}
	// Highest severity across the columns; ties go to the first column.
	if best := top(bests); best != nil {
		c.Title = best.Title
	}
	for _, t := range takes {
		if t.Verdict == VerdictIgnored || t.Verdict != takes[0].Verdict {
			c.IsConflict = true
			break
		}
	}
	return c
}

// EstimateRun is one done run as the estimate reads it.
type EstimateRun struct {
	ID      string
	AgentID string
	RanAt   time.Time
	// DurationMS and CostUSD are nil when the run did not record them.
	DurationMS *int
	CostUSD    *float64
}

// Estimates computes the per-agent averages of GET /runs/estimates from done runs (any pull
// request, any source) that have an agent: per agent, the EstimateSampleSize newest runs (ran_at
// descending, then id) that have a duration or a cost; sample_size counts them and each average
// skips the runs without that value (nil when none has it). Rows are ordered by agent id, bytes
// first as the database would order a "C" collation. Runs without a duration and a cost, and
// runs without an agent id, are not sampled.
//
// The cost average is computed in integer micro-dollars (the column keeps 6 decimals), so it
// equals the database's exact decimal average rounded once to a float.
func Estimates(runs []EstimateRun) []AgentRunEstimate {
	byAgent := map[string][]EstimateRun{}
	for _, r := range runs {
		if r.AgentID == "" || (r.DurationMS == nil && r.CostUSD == nil) {
			continue
		}
		byAgent[r.AgentID] = append(byAgent[r.AgentID], r)
	}
	ids := make([]string, 0, len(byAgent))
	for id := range byAgent {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	out := make([]AgentRunEstimate, 0, len(ids))
	for _, id := range ids {
		sample := byAgent[id]
		sort.SliceStable(sample, func(i, j int) bool {
			if !sample[i].RanAt.Equal(sample[j].RanAt) {
				return sample[i].RanAt.After(sample[j].RanAt)
			}
			return sample[i].ID < sample[j].ID
		})
		if len(sample) > EstimateSampleSize {
			sample = sample[:EstimateSampleSize]
		}
		est := AgentRunEstimate{AgentID: id, SampleSize: len(sample)}
		var durSum, durN, microSum, costN int64
		for _, r := range sample {
			if r.DurationMS != nil {
				durSum += int64(*r.DurationMS)
				durN++
			}
			if r.CostUSD != nil {
				microSum += int64(math.Round(*r.CostUSD * 1e6))
				costN++
			}
		}
		if durN > 0 {
			avg := float64(durSum) / float64(durN)
			est.AvgDurationMS = &avg
		}
		if costN > 0 {
			avg := float64(microSum) / (float64(costN) * 1e6)
			est.AvgCostUSD = &avg
		}
		out = append(out, est)
	}
	return out
}

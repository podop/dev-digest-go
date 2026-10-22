package domain

import (
	"math"
	"sort"
	"time"
)

// moneyFactor is a variable so that n*moneyFactor is a runtime float64 operation, as in
// JavaScript (a constant expression would be evaluated exactly).
var moneyFactor = math.Pow(10, MoneyDecimals)

// jsRound is Math.round: the nearest integer, ties toward +Infinity.
func jsRound(x float64) float64 {
	f := math.Floor(x)
	if x-f >= 0.5 {
		return f + 1
	}
	return f
}

// RoundMoney rounds a money amount to MoneyDecimals decimals the way Node did:
// Math.round(n * 10^6) / 10^6.
func RoundMoney(n float64) float64 {
	scaled := float64(n * moneyFactor)
	return jsRound(scaled) / moneyFactor
}

// AgentFacts are one agent's counted runs of one window with their finding counts (nil = no
// findings).
type AgentFacts struct {
	Runs     []RunFact
	Findings *FindingCounts
}

// AgentMetrics folds the counted runs of one agent and its finding counts (spec 7.11.3). The
// total cost is rounded after every addition, as Node did, so the order of the runs matters.
func AgentMetrics(runs []RunFact, findings *FindingCounts) Metrics {
	var m Metrics
	var cost float64
	var doneWithDuration, durationSum int
	var last *time.Time
	for i := range runs {
		r := &runs[i]
		switch r.Status {
		case RunDone:
			m.RunsDone++
		case RunFailed:
			m.RunsFailed++
		default:
			m.RunsCancelled++
		}
		if r.Source == SourceLocal {
			m.RunsLocal++
		} else {
			m.RunsCI++
		}
		if r.CostUSD != nil {
			m.PricedRuns++
			cost = RoundMoney(cost + *r.CostUSD)
		}
		if r.Status == RunDone && r.DurationMs != nil {
			doneWithDuration++
			durationSum += *r.DurationMs
		}
		if last == nil || r.RanAt.UnixMilli() > last.UnixMilli() {
			at := r.RanAt
			last = &at
		}
	}
	m.Runs = len(runs)
	m.UnpricedRuns = len(runs) - m.PricedRuns
	if m.PricedRuns > 0 {
		total := cost
		avg := RoundMoney(cost / float64(m.PricedRuns))
		m.TotalCostUSD, m.AvgCostUSD = &total, &avg
	}
	if doneWithDuration > 0 {
		avg := int(jsRound(float64(durationSum) / float64(doneWithDuration)))
		m.AvgDurationMs = &avg
	}
	if last != nil {
		at := msTime(last.UnixMilli())
		m.LastRunAt = &at
	}
	if findings != nil {
		m.FindingsTotal, m.Accepted, m.Dismissed = findings.Total, findings.Accepted, findings.Dismissed
	}
	m.Decisions = m.Accepted + m.Dismissed
	m.Pending = m.FindingsTotal - m.Decisions
	if m.Decisions > 0 {
		rate := float64(m.Accepted) / float64(m.Decisions)
		m.AcceptRate = &rate
	}
	m.SmallSample = m.Decisions > 0 && m.Decisions < MinDecisions
	return m
}

// BuildRow is the one row builder behind both routes: a nil agent is the deleted-agents row.
// previous holds the same agent's facts in the window right before win.
func BuildRow(agent *AgentRef, current, previous AgentFacts, win PeriodWindow) Row {
	row := Row{
		AgentName: DeletedAgentsName,
		Metrics:   AgentMetrics(current.Runs, current.Findings),
		Previous:  AgentMetrics(previous.Runs, previous.Findings),
		RunsTrend: RunsTrend(current.Runs, win),
	}
	if agent != nil {
		id := agent.ID
		row.AgentID, row.AgentName = &id, agent.Name
	}
	return row
}

// Grouped holds the facts of one window per agent; the empty id is the deleted-agents key, which
// gathers runs without an agent and runs whose agent is not an agent of the workspace.
type Grouped struct {
	runs   map[string][]RunFact
	counts map[string]FindingCounts
}

// GroupFacts groups runs and finding counts by agent; known is the set of the workspace's agents.
func GroupFacts(runs []RunFact, counts []FindingCounts, known map[string]bool) Grouped {
	key := func(id *string) string {
		if id != nil && known[*id] {
			return *id
		}
		return ""
	}
	g := Grouped{runs: map[string][]RunFact{}, counts: map[string]FindingCounts{}}
	for _, r := range runs {
		k := key(r.AgentID)
		g.runs[k] = append(g.runs[k], r)
	}
	for _, c := range counts {
		k := key(c.AgentID)
		sum := g.counts[k]
		sum.Total += c.Total
		sum.Accepted += c.Accepted
		sum.Dismissed += c.Dismissed
		g.counts[k] = sum
	}
	return g
}

// Facts returns one agent's facts; a nil agent id selects the deleted-agents key.
func (g Grouped) Facts(agentID *string) AgentFacts {
	k := ""
	if agentID != nil {
		k = *agentID
	}
	f := AgentFacts{Runs: g.runs[k]}
	if c, ok := g.counts[k]; ok {
		f.Findings = &c
	}
	return f
}

// HasDeleted reports whether the window has runs of deleted agents.
func (g Grouped) HasDeleted() bool { return len(g.runs[""]) > 0 }

// FindMostActive picks the agent with the most runs among the rows of agents; ties go to the later
// last run, then to the name in code-unit order. Deleted agents never qualify.
func FindMostActive(rows []Row) *MostActive {
	var best *Row
	for i := range rows {
		row := &rows[i]
		if row.AgentID == nil || row.Metrics.Runs == 0 {
			continue
		}
		if best == nil || beats(row, best) {
			best = row
		}
	}
	if best == nil {
		return nil
	}
	return &MostActive{AgentID: *best.AgentID, AgentName: best.AgentName, Runs: best.Metrics.Runs}
}

func beats(a, b *Row) bool {
	if a.Metrics.Runs != b.Metrics.Runs {
		return a.Metrics.Runs > b.Metrics.Runs
	}
	if at, bt := lastRunMs(a), lastRunMs(b); at != bt {
		return at > bt
	}
	return compareNames(a.AgentName, b.AgentName) < 0
}

func lastRunMs(r *Row) int64 {
	if r.Metrics.LastRunAt == nil {
		return 0
	}
	return r.Metrics.LastRunAt.UnixMilli()
}

// Summarize sums the rows; the accept rate is pooled (accepted over all decisions) and the total
// cost is the rounded running sum of the rows' totals.
func Summarize(rows []Row) Summary {
	var s Summary
	var cost float64
	for i := range rows {
		m := &rows[i].Metrics
		s.Runs += m.Runs
		s.PricedRuns += m.PricedRuns
		s.UnpricedRuns += m.UnpricedRuns
		s.Accepted += m.Accepted
		s.Dismissed += m.Dismissed
		s.Pending += m.Pending
		if m.TotalCostUSD != nil {
			cost = RoundMoney(cost + *m.TotalCostUSD)
		}
	}
	s.Decisions = s.Accepted + s.Dismissed
	if s.PricedRuns > 0 {
		s.TotalCostUSD = &cost
	}
	if s.Decisions > 0 {
		rate := float64(s.Accepted) / float64(s.Decisions)
		s.AvgAcceptRate = &rate
	}
	s.MostActive = FindMostActive(rows)
	return s
}

// segments drops zero values and orders by value descending, then key ascending with nil last.
func segments(items []CostSegment) []CostSegment {
	out := make([]CostSegment, 0, len(items))
	for _, s := range items {
		if s.Value > 0 {
			out = append(out, s)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Value != out[j].Value {
			return out[i].Value > out[j].Value
		}
		return compareKeys(out[i].Key, out[j].Key) < 0
	})
	return out
}

// CostByAgent is one segment per row with a positive priced cost; the deleted-agents row has a
// nil key and label.
func CostByAgent(rows []Row) []CostSegment {
	items := make([]CostSegment, 0, len(rows))
	for _, r := range rows {
		seg := CostSegment{Runs: r.Metrics.PricedRuns}
		if r.Metrics.TotalCostUSD != nil {
			seg.Value = *r.Metrics.TotalCostUSD
		}
		if r.AgentID != nil {
			id, name := *r.AgentID, r.AgentName
			seg.Key, seg.Label = &id, &name
		}
		items = append(items, seg)
	}
	return segments(items)
}

// CostByModel groups the priced runs by the run's own stored model (nil = unknown model).
func CostByModel(runs []RunFact) []CostSegment {
	type bucket struct {
		model *string
		seg   CostSegment
	}
	var order []string
	byModel := map[string]*bucket{}
	nilKey := "\x00nil" // a model name cannot hold U+0000 (the column is text)
	for _, r := range runs {
		if r.CostUSD == nil {
			continue
		}
		k := nilKey
		if r.Model != nil {
			k = "m:" + *r.Model
		}
		b, ok := byModel[k]
		if !ok {
			b = &bucket{model: r.Model}
			byModel[k] = b
			order = append(order, k)
		}
		b.seg.Value = RoundMoney(b.seg.Value + *r.CostUSD)
		b.seg.Runs++
	}
	items := make([]CostSegment, 0, len(order))
	for _, k := range order {
		b := byModel[k]
		seg := b.seg
		if b.model != nil {
			m := *b.model
			seg.Key, seg.Label = &m, &m
		}
		items = append(items, seg)
	}
	return segments(items)
}

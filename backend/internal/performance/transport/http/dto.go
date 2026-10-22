package performancehttp

import (
	"time"

	"dev-digest-go/backend/internal/performance/domain"
)

// The wire shapes of spec 7.3 (contract files observability.ts and productionize.ts). Struct
// field order is the key order of the JSON; nullable fields are pointers without omitempty and
// slices are never nil, so an empty list is [] and not null. Timestamps are strings in the form
// of JavaScript's toISOString (domain.ISOMillis).

type periodInfoDTO struct {
	Key  string `json:"key"`
	From string `json:"from"`
	To   string `json:"to"`
}

type metricsDTO struct {
	Runs          int      `json:"runs"`
	RunsDone      int      `json:"runs_done"`
	RunsFailed    int      `json:"runs_failed"`
	RunsCancelled int      `json:"runs_cancelled"`
	RunsLocal     int      `json:"runs_local"`
	RunsCI        int      `json:"runs_ci"`
	PricedRuns    int      `json:"priced_runs"`
	UnpricedRuns  int      `json:"unpriced_runs"`
	TotalCostUSD  *float64 `json:"total_cost_usd"`
	AvgCostUSD    *float64 `json:"avg_cost_usd"`
	AvgDurationMs *int     `json:"avg_duration_ms"`
	LastRunAt     *string  `json:"last_run_at"`
	FindingsTotal int      `json:"findings_total"`
	Accepted      int      `json:"accepted"`
	Dismissed     int      `json:"dismissed"`
	Pending       int      `json:"pending"`
	Decisions     int      `json:"decisions"`
	AcceptRate    *float64 `json:"accept_rate"`
	SmallSample   bool     `json:"small_sample"`
}

type statPointDTO struct {
	Label string `json:"label"`
	Value int    `json:"value"`
}

type costSegmentDTO struct {
	Key   *string `json:"key"`
	Label *string `json:"label"`
	Value float64 `json:"value"`
	Runs  int     `json:"runs"`
}

type mostActiveDTO struct {
	AgentID   string `json:"agent_id"`
	AgentName string `json:"agent_name"`
	Runs      int    `json:"runs"`
}

type summaryDTO struct {
	Runs          int            `json:"runs"`
	PricedRuns    int            `json:"priced_runs"`
	UnpricedRuns  int            `json:"unpriced_runs"`
	Accepted      int            `json:"accepted"`
	Dismissed     int            `json:"dismissed"`
	Pending       int            `json:"pending"`
	Decisions     int            `json:"decisions"`
	TotalCostUSD  *float64       `json:"total_cost_usd"`
	AvgAcceptRate *float64       `json:"avg_accept_rate"`
	MostActive    *mostActiveDTO `json:"most_active"`
}

type rowDTO struct {
	AgentID   *string        `json:"agent_id"`
	AgentName string         `json:"agent_name"`
	Metrics   metricsDTO     `json:"metrics"`
	Previous  metricsDTO     `json:"previous"`
	RunsTrend []statPointDTO `json:"runs_trend"`
}

type agentPerfDTO struct {
	Period          periodInfoDTO    `json:"period"`
	MinDecisions    int              `json:"min_decisions"`
	CostBasis       string           `json:"cost_basis"`
	Summary         summaryDTO       `json:"summary"`
	PreviousSummary summaryDTO       `json:"previous_summary"`
	RunsTrend       []statPointDTO   `json:"runs_trend"`
	Agents          []rowDTO         `json:"agents"`
	CostByAgent     []costSegmentDTO `json:"cost_by_agent"`
	CostByModel     []costSegmentDTO `json:"cost_by_model"`
}

type usageCountDTO struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	Runs  int    `json:"runs"`
}

type severityBucketDTO struct {
	From       string `json:"from"`
	Critical   int    `json:"critical"`
	Warning    int    `json:"warning"`
	Suggestion int    `json:"suggestion"`
}

type categoryCountDTO struct {
	Category string `json:"category"`
	Count    int    `json:"count"`
}

type runPRDTO struct {
	ID     string `json:"id"`
	RepoID string `json:"repo_id"`
	Number int    `json:"number"`
}

type runHistoryDTO struct {
	RunID         string    `json:"run_id"`
	RanAt         string    `json:"ran_at"`
	Status        string    `json:"status"`
	Source        string    `json:"source"`
	PR            *runPRDTO `json:"pr"`
	Tokens        *int      `json:"tokens"`
	CostUSD       *float64  `json:"cost_usd"`
	FindingsCount *int      `json:"findings_count"`
	HasTrace      bool      `json:"has_trace"`
}

type agentStatsDTO struct {
	AgentID            string              `json:"agent_id"`
	AgentName          string              `json:"agent_name"`
	Period             periodInfoDTO       `json:"period"`
	MinDecisions       int                 `json:"min_decisions"`
	CostBasis          string              `json:"cost_basis"`
	Metrics            metricsDTO          `json:"metrics"`
	Previous           metricsDTO          `json:"previous"`
	RunsTrend          []statPointDTO      `json:"runs_trend"`
	CostByModel        []costSegmentDTO    `json:"cost_by_model"`
	SkillsUsed         []usageCountDTO     `json:"skills_used"`
	MemoryPulled       []usageCountDTO     `json:"memory_pulled"`
	FindingsBySeverity []severityBucketDTO `json:"findings_by_severity"`
	FindingsByCategory []categoryCountDTO  `json:"findings_by_category"`
	RunHistory         []runHistoryDTO     `json:"run_history"`
}

func isoPtr(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := domain.ISOMillis(*t)
	return &s
}

func toPeriod(p domain.PeriodInfo) periodInfoDTO {
	return periodInfoDTO{Key: string(p.Key), From: domain.ISOMillis(p.From), To: domain.ISOMillis(p.To)}
}

func toMetrics(m domain.Metrics) metricsDTO {
	return metricsDTO{
		Runs: m.Runs, RunsDone: m.RunsDone, RunsFailed: m.RunsFailed, RunsCancelled: m.RunsCancelled,
		RunsLocal: m.RunsLocal, RunsCI: m.RunsCI, PricedRuns: m.PricedRuns, UnpricedRuns: m.UnpricedRuns,
		TotalCostUSD: m.TotalCostUSD, AvgCostUSD: m.AvgCostUSD, AvgDurationMs: m.AvgDurationMs,
		LastRunAt: isoPtr(m.LastRunAt), FindingsTotal: m.FindingsTotal, Accepted: m.Accepted,
		Dismissed: m.Dismissed, Pending: m.Pending, Decisions: m.Decisions, AcceptRate: m.AcceptRate,
		SmallSample: m.SmallSample,
	}
}

func toPoints(ps []domain.StatPoint) []statPointDTO {
	out := make([]statPointDTO, len(ps))
	for i, p := range ps {
		out[i] = statPointDTO{Label: p.Label, Value: p.Value}
	}
	return out
}

func toSegments(ss []domain.CostSegment) []costSegmentDTO {
	out := make([]costSegmentDTO, len(ss))
	for i, s := range ss {
		out[i] = costSegmentDTO{Key: s.Key, Label: s.Label, Value: s.Value, Runs: s.Runs}
	}
	return out
}

func toSummary(s domain.Summary) summaryDTO {
	out := summaryDTO{
		Runs: s.Runs, PricedRuns: s.PricedRuns, UnpricedRuns: s.UnpricedRuns, Accepted: s.Accepted,
		Dismissed: s.Dismissed, Pending: s.Pending, Decisions: s.Decisions,
		TotalCostUSD: s.TotalCostUSD, AvgAcceptRate: s.AvgAcceptRate,
	}
	if s.MostActive != nil {
		out.MostActive = &mostActiveDTO{AgentID: s.MostActive.AgentID, AgentName: s.MostActive.AgentName, Runs: s.MostActive.Runs}
	}
	return out
}

func toAgentPerf(p domain.AgentPerf) agentPerfDTO {
	rows := make([]rowDTO, len(p.Agents))
	for i, r := range p.Agents {
		rows[i] = rowDTO{
			AgentID: r.AgentID, AgentName: r.AgentName, Metrics: toMetrics(r.Metrics),
			Previous: toMetrics(r.Previous), RunsTrend: toPoints(r.RunsTrend),
		}
	}
	return agentPerfDTO{
		Period: toPeriod(p.Period), MinDecisions: domain.MinDecisions, CostBasis: domain.CostBasis,
		Summary: toSummary(p.Summary), PreviousSummary: toSummary(p.PreviousSummary),
		RunsTrend: toPoints(p.RunsTrend), Agents: rows,
		CostByAgent: toSegments(p.CostByAgent), CostByModel: toSegments(p.CostByModel),
	}
}

func toUsage(us []domain.UsageCount) []usageCountDTO {
	out := make([]usageCountDTO, len(us))
	for i, u := range us {
		out[i] = usageCountDTO{Key: u.Key, Label: u.Label, Runs: u.Runs}
	}
	return out
}

func toHistory(items []domain.RunHistoryItem) []runHistoryDTO {
	out := make([]runHistoryDTO, len(items))
	for i, it := range items {
		d := runHistoryDTO{
			RunID: it.RunID, RanAt: domain.ISOMillis(it.RanAt), Status: string(it.Status), Source: string(it.Source),
			Tokens: it.Tokens, CostUSD: it.CostUSD, FindingsCount: it.FindingsCount, HasTrace: it.HasTrace,
		}
		if it.PR != nil {
			d.PR = &runPRDTO{ID: it.PR.ID, RepoID: it.PR.RepoID, Number: it.PR.Number}
		}
		out[i] = d
	}
	return out
}

func toAgentStats(s domain.AgentStats) agentStatsDTO {
	sev := make([]severityBucketDTO, len(s.FindingsBySeverity))
	for i, b := range s.FindingsBySeverity {
		sev[i] = severityBucketDTO{From: b.From, Critical: b.Critical, Warning: b.Warning, Suggestion: b.Suggestion}
	}
	cats := make([]categoryCountDTO, len(s.FindingsByCategory))
	for i, c := range s.FindingsByCategory {
		cats[i] = categoryCountDTO{Category: c.Category, Count: c.Count}
	}
	return agentStatsDTO{
		AgentID: s.AgentID, AgentName: s.AgentName, Period: toPeriod(s.Period),
		MinDecisions: domain.MinDecisions, CostBasis: domain.CostBasis,
		Metrics: toMetrics(s.Metrics), Previous: toMetrics(s.Previous), RunsTrend: toPoints(s.RunsTrend),
		CostByModel: toSegments(s.CostByModel), SkillsUsed: toUsage(s.SkillsUsed), MemoryPulled: toUsage(s.MemoryPulled),
		FindingsBySeverity: sev, FindingsByCategory: cats, RunHistory: toHistory(s.RunHistory),
	}
}

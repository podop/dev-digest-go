// Package domain is the vocabulary of the performance context (Phase 5, spec section 7.11): the
// period query and its window, the per-run facts the store reads, and the dashboard and stats
// read models built from them. The pure folds (windows, metrics, rows, summaries, cost segments,
// trends, ordering, period validation) are added next to this file; it fixes only the shared
// types and constants. It imports only the standard library and carries no JSON or database
// tags: the wire shapes live in transport/http.
package domain

import (
	"errors"
	"time"
)

// ErrNotFound is returned by the store for an agent that is not in the workspace.
var ErrNotFound = errors.New("performance: not found")

// Constants of the context (spec section 7.11).
const (
	// MinDecisions is the smallest number of accept/dismiss decisions for a reliable accept rate.
	MinDecisions = 5
	// MaxCustomSpanDays is the longest custom period.
	MaxCustomSpanDays = 366
	// DeletedAgentsName is the name of the row that gathers the runs of deleted agents.
	DeletedAgentsName = "Deleted agents"
	// CostBasis is the response's cost basis.
	CostBasis = "estimate"
	// SeveritySlices is the number of slices of the findings-by-severity chart.
	SeveritySlices = 6
	// TopUsage is the number of rows of the most-used skills and most-pulled memories lists.
	TopUsage = 5
	// HourlyTrendMaxHours: periods up to this long trend per hour, longer ones per day.
	HourlyTrendMaxHours = 48
	// RunHistoryLimit is the number of rows of a stats run history.
	RunHistoryLimit = 20
	// MoneyDecimals is the number of decimals money is rounded to.
	MoneyDecimals = 6
)

// Preset windows: [now - hours, now].
const (
	Preset1dHours  = 24
	Preset30dHours = 720
)

// PeriodKey is the reporting period of a request.
type PeriodKey string

// The period keys. The default is Period30d.
const (
	Period1d     PeriodKey = "1d"
	Period30d    PeriodKey = "30d"
	PeriodCustom PeriodKey = "custom"
)

// PeriodQuery is the validated period query of a request. From and To are set exactly for
// PeriodCustom (the transport validates presence, order and the 366-day span before any query
// runs) and are truncated to milliseconds.
type PeriodQuery struct {
	Key      PeriodKey
	From, To *time.Time
}

// PeriodWindow is the resolved window: presets are [From, To] with To = now (inclusive), custom
// ranges [From, To), and the previous window ends at From (exclusive).
type PeriodWindow struct {
	Key         PeriodKey
	From, To    time.Time
	ToInclusive bool
}

// RunStatus is the status of a counted run.
type RunStatus string

// The counted statuses.
const (
	RunDone      RunStatus = "done"
	RunFailed    RunStatus = "failed"
	RunCancelled RunStatus = "cancelled"
)

// RunSource is the source of a counted run.
type RunSource string

// The counted sources.
const (
	SourceLocal RunSource = "local"
	SourceCI    RunSource = "ci"
)

// AgentRef is an agent of the workspace, enabled or not.
type AgentRef struct{ ID, Name string }

// RunFact is one counted run (status done, failed or cancelled; source local or ci) with ran_at
// inside the window.
type RunFact struct {
	// AgentID is nil for a run without an agent; a run whose agent is not an agent of the
	// workspace belongs to the deleted-agents key (the domain groups them).
	AgentID    *string
	Status     RunStatus
	Source     RunSource
	Model      *string
	DurationMs *int
	CostUSD    *float64
	RanAt      time.Time
}

// FindingCounts are the findings of the counted runs of one agent: accepted = accepted_at set,
// dismissed = dismissed_at set and accepted_at not.
type FindingCounts struct {
	AgentID                    *string
	Total, Accepted, Dismissed int
}

// FindingFact is the number of findings of one counted run with one severity and category.
type FindingFact struct {
	RanAt    time.Time
	Severity string
	Category string
	Count    int
}

// SkillFact is a skill with the number of counted runs that had it in the prompt.
type SkillFact struct {
	SkillID, Name string
	Runs          int
}

// RunPR is the pull request a run reviewed.
type RunPR struct {
	ID, RepoID string
	Number     int
}

// RunHistoryItem is one counted run of an agent, as the stats run history lists it.
type RunHistoryItem struct {
	RunID  string
	RanAt  time.Time
	Status RunStatus
	Source RunSource
	// PR is nil when the pull request was deleted or the run had none.
	PR *RunPR
	// Tokens is tokens in + out (a null side counts 0); nil when both are null.
	Tokens        *int
	CostUSD       *float64
	FindingsCount *int
	HasTrace      bool
}

// PeriodInfo is the resolved window echoed by both responses.
type PeriodInfo struct {
	Key      PeriodKey
	From, To time.Time
}

// Metrics are the per-agent metrics over the counted runs of one window (spec section 7.11.3).
type Metrics struct {
	Runs, RunsDone, RunsFailed, RunsCancelled int
	RunsLocal, RunsCI                         int
	PricedRuns, UnpricedRuns                  int
	// TotalCostUSD and AvgCostUSD are nil when no run is priced.
	TotalCostUSD *float64
	AvgCostUSD   *float64
	// AvgDurationMs is nil when no done run has a duration.
	AvgDurationMs *int
	LastRunAt     *time.Time
	FindingsTotal int
	Accepted      int
	Dismissed     int
	Pending       int
	Decisions     int
	// AcceptRate is nil when there are no decisions.
	AcceptRate  *float64
	SmallSample bool
}

// StatPoint is one slice of a trend: Label is the slice start as an ISO-8601 UTC string with
// milliseconds.
type StatPoint struct {
	Label string
	Value int
}

// Row is a dashboard row; a nil AgentID is the deleted-agents row.
type Row struct {
	AgentID   *string
	AgentName string
	Metrics   Metrics
	Previous  Metrics
	RunsTrend []StatPoint
}

// MostActive is the agent with the most runs.
type MostActive struct {
	AgentID, AgentName string
	Runs               int
}

// Summary is the cross-agent totals of one window.
type Summary struct {
	Runs, PricedRuns, UnpricedRuns int
	Accepted, Dismissed, Pending   int
	Decisions                      int
	// TotalCostUSD is nil when no run is priced; AvgAcceptRate when there are no decisions.
	TotalCostUSD  *float64
	AvgAcceptRate *float64
	// MostActive is nil when no agent qualifies.
	MostActive *MostActive
}

// CostSegment is a cost-breakdown segment: Key and Label are the agent id and name or the model
// (nil = deleted agents or unknown model); Runs counts priced runs. Value is positive.
type CostSegment struct {
	Key, Label *string
	Value      float64
	Runs       int
}

// UsageCount is a skill or memory with the number of counted runs it was in.
type UsageCount struct {
	Key, Label string
	Runs       int
}

// SeverityBucket counts the findings of the counted runs that started in one slice; From is the
// slice start as an ISO-8601 UTC string with milliseconds.
type SeverityBucket struct {
	From                          string
	Critical, Warning, Suggestion int
}

// CategoryCount is the number of findings of one category.
type CategoryCount struct {
	Category string
	Count    int
}

// AgentPerf is the dashboard (GET /agents/performance): one row per agent of the workspace plus
// the deleted-agents row, ordered by name in code-unit order.
type AgentPerf struct {
	Period          PeriodInfo
	Summary         Summary
	PreviousSummary Summary
	RunsTrend       []StatPoint
	Agents          []Row
	CostByAgent     []CostSegment
	CostByModel     []CostSegment
}

// AgentStats is one agent's stats (GET /agents/:id/stats); Metrics, Previous and RunsTrend equal
// the agent's dashboard row for the same period.
type AgentStats struct {
	AgentID, AgentName string
	Period             PeriodInfo
	Metrics            Metrics
	Previous           Metrics
	RunsTrend          []StatPoint
	CostByModel        []CostSegment
	SkillsUsed         []UsageCount
	MemoryPulled       []UsageCount
	FindingsBySeverity []SeverityBucket
	FindingsByCategory []CategoryCount
	RunHistory         []RunHistoryItem
}

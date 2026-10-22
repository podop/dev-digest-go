package postgres

import "time"

// The row types of the queries. They are declared per adapter and read only the columns the
// context needs of tables that other contexts own.

type agentRow struct {
	ID   string `gorm:"column:id"`
	Name string `gorm:"column:name"`
}

type runRow struct {
	AgentID    *string   `gorm:"column:agent_id"`
	Status     string    `gorm:"column:status"`
	Source     string    `gorm:"column:source"`
	Model      *string   `gorm:"column:model"`
	DurationMs *int      `gorm:"column:duration_ms"`
	CostUSD    *float64  `gorm:"column:cost_usd"`
	RanAt      time.Time `gorm:"column:ran_at"`
}

type findingCountsRow struct {
	AgentID   *string `gorm:"column:agent_id"`
	Total     int     `gorm:"column:total"`
	Accepted  int     `gorm:"column:accepted"`
	Dismissed int     `gorm:"column:dismissed"`
}

type findingFactRow struct {
	RanAt    time.Time `gorm:"column:ran_at"`
	Severity string    `gorm:"column:severity"`
	Category string    `gorm:"column:category"`
	N        int       `gorm:"column:n"`
}

type skillUsageRow struct {
	SkillID string `gorm:"column:skill_id"`
	Name    string `gorm:"column:name"`
	Runs    int    `gorm:"column:runs"`
}

// memoryRow holds trace -> 'memory_pulled' as raw JSON bytes; NULL (the key is absent) is nil.
type memoryRow struct {
	Pulled []byte `gorm:"column:pulled"`
}

type historyRow struct {
	RunID         string    `gorm:"column:run_id"`
	RanAt         time.Time `gorm:"column:ran_at"`
	Status        string    `gorm:"column:status"`
	Source        string    `gorm:"column:source"`
	PRID          *string   `gorm:"column:pr_id"`
	PRRepoID      *string   `gorm:"column:pr_repo_id"`
	PRNumber      *int      `gorm:"column:pr_number"`
	TokensIn      *int      `gorm:"column:tokens_in"`
	TokensOut     *int      `gorm:"column:tokens_out"`
	CostUSD       *float64  `gorm:"column:cost_usd"`
	FindingsCount *int      `gorm:"column:findings_count"`
	TraceRunID    *string   `gorm:"column:trace_run_id"`
}

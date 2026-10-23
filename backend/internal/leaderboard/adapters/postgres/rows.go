package postgres

import "time"

// The row types of the queries. They are declared per adapter and read only the columns the
// context needs of tables that other contexts own.

type repoRow struct {
	ID           string     `gorm:"column:id"`
	Owner        string     `gorm:"column:owner"`
	Name         string     `gorm:"column:name"`
	FullName     string     `gorm:"column:full_name"`
	LastPolledAt *time.Time `gorm:"column:last_polled_at"`
}

type pullRow struct {
	ID              string     `gorm:"column:id"`
	RepoID          string     `gorm:"column:repo_id"`
	Repo            string     `gorm:"column:repo"`
	Number          int        `gorm:"column:number"`
	Title           string     `gorm:"column:title"`
	Author          string     `gorm:"column:author"`
	HeadSHA         string     `gorm:"column:head_sha"`
	LastReviewedSHA *string    `gorm:"column:last_reviewed_sha"`
	Additions       int        `gorm:"column:additions"`
	Deletions       int        `gorm:"column:deletions"`
	FilesCount      int        `gorm:"column:files_count"`
	Status          string     `gorm:"column:status"`
	Draft           bool       `gorm:"column:draft"`
	OpenedAt        *time.Time `gorm:"column:opened_at"`
	UpdatedAt       *time.Time `gorm:"column:updated_at"`
	MergedAt        *time.Time `gorm:"column:merged_at"`
	ClosedAt        *time.Time `gorm:"column:closed_at"`
	FirstReviewAt   *time.Time `gorm:"column:first_review_at"`
	Reviewed        bool       `gorm:"column:reviewed"`
}

type reviewRow struct {
	ID        string    `gorm:"column:id"`
	PRID      string    `gorm:"column:pr_id"`
	AgentID   *string   `gorm:"column:agent_id"`
	AgentName *string   `gorm:"column:agent_name"`
	Score     *int      `gorm:"column:score"`
	CreatedAt time.Time `gorm:"column:created_at"`
}

type findingRow struct {
	ReviewID    string     `gorm:"column:review_id"`
	File        string     `gorm:"column:file"`
	Severity    string     `gorm:"column:severity"`
	AcceptedAt  *time.Time `gorm:"column:accepted_at"`
	DismissedAt *time.Time `gorm:"column:dismissed_at"`
}

type runRow struct {
	PRID    string    `gorm:"column:pr_id"`
	RanAt   time.Time `gorm:"column:ran_at"`
	CostUSD *float64  `gorm:"column:cost_usd"`
}

type openCriticalRow struct {
	PRID string `gorm:"column:pr_id"`
	N    int    `gorm:"column:n"`
}

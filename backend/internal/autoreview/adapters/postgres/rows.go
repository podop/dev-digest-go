package postgres

import (
	"time"

	"dev-digest-go/backend/internal/autoreview/domain"
)

// recordRow is a row of auto_review_records (migration 0006). decided_at has no default: the
// trigger's clock is always passed.
type recordRow struct {
	ID          string    `gorm:"column:id;primaryKey;default:gen_random_uuid()"`
	WorkspaceID string    `gorm:"column:workspace_id"`
	PRID        string    `gorm:"column:pr_id"`
	HeadSHA     string    `gorm:"column:head_sha"`
	Event       string    `gorm:"column:event"`
	Outcome     string    `gorm:"column:outcome"`
	RunsStarted int       `gorm:"column:runs_started"`
	DecidedAt   time.Time `gorm:"column:decided_at"`
}

func (recordRow) TableName() string { return "auto_review_records" }

func fromRecord(rec domain.Record) recordRow {
	return recordRow{
		WorkspaceID: rec.WorkspaceID, PRID: rec.PRID, HeadSHA: rec.HeadSHA,
		Event: string(rec.Event), Outcome: string(rec.Outcome),
		RunsStarted: rec.RunsStarted, DecidedAt: rec.DecidedAt.UTC(),
	}
}

// candidateRow is one row of the candidates statement.
type candidateRow struct {
	PRID            string     `gorm:"column:pr_id"`
	Repo            string     `gorm:"column:repo"`
	Number          int        `gorm:"column:number"`
	HeadSHA         string     `gorm:"column:head_sha"`
	LastReviewedSHA string     `gorm:"column:last_reviewed_sha"`
	HasAnyRecord    bool       `gorm:"column:has_any_record"`
	Running         bool       `gorm:"column:running"`
	UpdatedAt       *time.Time `gorm:"column:updated_at"`
}

func (r candidateRow) toDomain() domain.Candidate {
	return domain.Candidate{
		PRID: r.PRID, Repo: r.Repo, Number: r.Number, HeadSHA: r.HeadSHA,
		LastReviewedSHA: r.LastReviewedSHA, HasAnyRecord: r.HasAnyRecord, Running: r.Running,
		UpdatedAt: r.UpdatedAt,
	}
}

// idRow is a single id column.
type idRow struct {
	ID string `gorm:"column:id"`
}

// lastPollRow is the newest repository poll time; nil when no repository was ever polled.
type lastPollRow struct {
	LastPollAt *time.Time `gorm:"column:last_poll_at"`
}

package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"

	"dev-digest-go/backend/internal/evals/domain"
)

// sourceRow is a finding joined to its review, pull request, repo and the PR's stored patch of the
// finding's file.
type sourceRow struct {
	FindingID    string     `gorm:"column:finding_id"`
	ReviewID     string     `gorm:"column:review_id"`
	PRID         string     `gorm:"column:pr_id"`
	AgentID      *string    `gorm:"column:agent_id"`
	AcceptedAt   *time.Time `gorm:"column:accepted_at"`
	DismissedAt  *time.Time `gorm:"column:dismissed_at"`
	File         string     `gorm:"column:file"`
	StartLine    int        `gorm:"column:start_line"`
	EndLine      int        `gorm:"column:end_line"`
	Severity     string     `gorm:"column:severity"`
	Category     string     `gorm:"column:category"`
	Title        string     `gorm:"column:title"`
	Patch        *string    `gorm:"column:patch"`
	PRTitle      string     `gorm:"column:pr_title"`
	PRBody       *string    `gorm:"column:pr_body"`
	PRNumber     int        `gorm:"column:pr_number"`
	HeadSHA      string     `gorm:"column:head_sha"`
	RepoFullName string     `gorm:"column:repo_full_name"`
}

// Source returns the finding of the workspace with its review, PR, repo and stored patch;
// domain.ErrNotFound when it is not in the workspace.
func (s *Store) Source(ctx context.Context, workspaceID, findingID string) (domain.FindingSource, error) {
	if !isUUID(findingID) {
		return domain.FindingSource{}, domain.ErrNotFound
	}
	var row sourceRow
	err := s.conn(ctx).Table("findings").
		Select(`findings.id AS finding_id, reviews.id AS review_id, pull_requests.id AS pr_id,
			reviews.agent_id AS agent_id, findings.accepted_at AS accepted_at, findings.dismissed_at AS dismissed_at,
			findings.file AS file, findings.start_line AS start_line, findings.end_line AS end_line,
			findings.severity AS severity, findings.category AS category, findings.title AS title,
			pr_files.patch AS patch, pull_requests.title AS pr_title, pull_requests.body AS pr_body,
			pull_requests.number AS pr_number, pull_requests.head_sha AS head_sha, repos.full_name AS repo_full_name`).
		Joins("JOIN reviews ON reviews.id = findings.review_id").
		Joins("JOIN pull_requests ON pull_requests.id = reviews.pr_id").
		Joins("JOIN repos ON repos.id = pull_requests.repo_id").
		Joins("LEFT JOIN pr_files ON pr_files.pr_id = pull_requests.id AND pr_files.path = findings.file").
		Where("findings.id = ? AND pull_requests.workspace_id = ?", findingID, workspaceID).
		Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return domain.FindingSource{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.FindingSource{}, fmt.Errorf("select finding source: %w", err)
	}
	return domain.FindingSource{
		FindingID: row.FindingID, ReviewID: row.ReviewID, PRID: row.PRID, AgentID: row.AgentID,
		AcceptedAt: row.AcceptedAt, DismissedAt: row.DismissedAt,
		File: row.File, StartLine: row.StartLine, EndLine: row.EndLine,
		Severity: row.Severity, Category: row.Category, Title: row.Title,
		Patch: row.Patch, PRTitle: row.PRTitle, PRBody: row.PRBody,
		RepoFullName: row.RepoFullName, PRNumber: row.PRNumber, HeadSHA: row.HeadSHA,
	}, nil
}

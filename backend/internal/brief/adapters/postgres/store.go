// Package postgres is the GORM-backed store of the brief context: the pull request, its files and
// current reviews (tables of other contexts, read with this package's own row types) and the
// stored brief (pr_brief, one document per pull request).
package postgres

import (
	"context"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"dev-digest-go/backend/internal/brief/app"
	"dev-digest-go/backend/internal/brief/domain"
	platformpg "dev-digest-go/backend/internal/platform/postgres"
	reviewer "dev-digest-go/backend/internal/reviewer/domain"
)

// Store implements app.Store.
type Store struct {
	db *gorm.DB
}

var _ app.Store = (*Store)(nil)

// NewStore builds a Store on a GORM handle.
func NewStore(db *gorm.DB) *Store { return &Store{db: db} }

func (s *Store) conn(ctx context.Context) *gorm.DB { return platformpg.Conn(ctx, s.db) }

type pullRow struct {
	ID          string  `gorm:"column:id"`
	WorkspaceID string  `gorm:"column:workspace_id"`
	RepoID      string  `gorm:"column:repo_id"`
	Owner       string  `gorm:"column:owner"`
	Name        string  `gorm:"column:name"`
	Number      int     `gorm:"column:number"`
	Title       string  `gorm:"column:title"`
	Body        *string `gorm:"column:body"`
	Base        string  `gorm:"column:base"`
	HeadSHA     string  `gorm:"column:head_sha"`
}

// FindPull returns a pull request of the workspace with its repository; domain.ErrNotFound for
// another workspace's or an unknown one.
func (s *Store) FindPull(ctx context.Context, workspaceID, prID string) (domain.Pull, error) {
	var row pullRow
	err := s.conn(ctx).Table("pull_requests AS p").
		Select("p.id, p.workspace_id, p.repo_id, r.owner, r.name, p.number, p.title, p.body, p.base, p.head_sha").
		Joins("JOIN repos r ON r.id = p.repo_id").
		Where("p.workspace_id = ? AND p.id = ?", workspaceID, prID).
		Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return domain.Pull{}, fmt.Errorf("pull request %s: %w", prID, domain.ErrNotFound)
	}
	if err != nil {
		return domain.Pull{}, fmt.Errorf("select pull request: %w", err)
	}
	return domain.Pull{
		ID: row.ID, WorkspaceID: row.WorkspaceID, RepoID: row.RepoID, Owner: row.Owner, Name: row.Name,
		Number: row.Number, Title: row.Title, Body: row.Body, Base: row.Base, HeadSHA: row.HeadSHA,
	}, nil
}

type fileRow struct {
	Path      string  `gorm:"column:path"`
	Additions int     `gorm:"column:additions"`
	Deletions int     `gorm:"column:deletions"`
	Patch     *string `gorm:"column:patch"`
}

// PullFiles returns the stored files by path in the database default collation (as Node's ORDER
// BY path), ties by id.
func (s *Store) PullFiles(ctx context.Context, prID string) ([]domain.StoredFile, error) {
	var rows []fileRow
	err := s.conn(ctx).Table("pr_files").Select("path, additions, deletions, patch").
		Where("pr_id = ?", prID).Order("path, id").Find(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("select pr files: %w", err)
	}
	out := make([]domain.StoredFile, len(rows))
	for i, r := range rows {
		out[i] = domain.StoredFile{Path: r.Path, Additions: r.Additions, Deletions: r.Deletions}
		if r.Patch != nil {
			out[i].Patch = *r.Patch
		}
	}
	return out, nil
}

// currentReviewsSQL selects the newest kind='review' review of each agent of the pull request;
// DISTINCT ON puts the reviews with a NULL agent in one group. Ties on created_at go to the
// smaller id (decision D5009: Node left them undefined). GORM's builder has no DISTINCT ON, hence
// Raw.
const currentReviewsSQL = `
SELECT cur.id, cur.agent_id
FROM (
	SELECT DISTINCT ON (r.agent_id) r.id, r.agent_id, r.created_at
	FROM reviews r
	WHERE r.pr_id = ? AND r.kind = 'review'
	ORDER BY r.agent_id, r.created_at DESC, r.id ASC
) cur
ORDER BY cur.created_at DESC, cur.id ASC`

type reviewRow struct {
	ID      string  `gorm:"column:id"`
	AgentID *string `gorm:"column:agent_id"`
}

type findingRow struct {
	ReviewID  string `gorm:"column:review_id"`
	Severity  string `gorm:"column:severity"`
	File      string `gorm:"column:file"`
	StartLine int    `gorm:"column:start_line"`
	EndLine   int    `gorm:"column:end_line"`
	Title     string `gorm:"column:title"`
}

// CurrentReviews returns the current review of each agent, newest first, each with its findings
// (dismissed included) by file in the database default collation, start line, id.
func (s *Store) CurrentReviews(ctx context.Context, prID string) ([]domain.ReviewFacts, error) {
	db := s.conn(ctx)
	var reviews []reviewRow
	if err := db.Raw(currentReviewsSQL, prID).Scan(&reviews).Error; err != nil {
		return nil, fmt.Errorf("select current reviews: %w", err)
	}
	if len(reviews) == 0 {
		return nil, nil
	}
	ids := make([]string, len(reviews))
	for i, r := range reviews {
		ids[i] = r.ID
	}
	var findings []findingRow
	err := db.Table("findings").Select("review_id, severity, file, start_line, end_line, title").
		Where("review_id IN ?", ids).Order("file, start_line, id").Find(&findings).Error
	if err != nil {
		return nil, fmt.Errorf("select findings: %w", err)
	}
	byReview := make(map[string][]domain.FindingFact, len(reviews))
	for _, f := range findings {
		byReview[f.ReviewID] = append(byReview[f.ReviewID], domain.FindingFact{
			Severity: reviewer.Severity(f.Severity), File: f.File, StartLine: f.StartLine, EndLine: f.EndLine, Title: f.Title,
		})
	}
	out := make([]domain.ReviewFacts, len(reviews))
	for i, r := range reviews {
		out[i] = domain.ReviewFacts{ReviewID: r.ID, AgentID: r.AgentID, Findings: byReview[r.ID]}
	}
	return out, nil
}

// jsonBytes maps the jsonb column to its text; GORM does not call Scan for a SQL NULL, which the
// NOT NULL column rules out.
type jsonBytes []byte

// Scan implements sql.Scanner.
func (j *jsonBytes) Scan(src any) error {
	switch v := src.(type) {
	case []byte:
		*j = append((*j)[:0], v...)
	case string:
		*j = jsonBytes(v)
	default:
		return fmt.Errorf("scan pr_brief.json: unsupported type %T", src)
	}
	return nil
}

// Value implements driver.Valuer; the server casts the text to jsonb.
func (j jsonBytes) Value() (driver.Value, error) { return string(j), nil }

type briefRow struct {
	PRID string    `gorm:"column:pr_id"`
	JSON jsonBytes `gorm:"column:json;type:jsonb"`
}

func (briefRow) TableName() string { return "pr_brief" }

// Brief returns the stored brief. A document that no longer satisfies the contract reads as
// absent (ok false), never as an error.
func (s *Store) Brief(ctx context.Context, prID string) (domain.Brief, bool, error) {
	var rows []briefRow
	if err := s.conn(ctx).Where("pr_id = ?", prID).Limit(1).Find(&rows).Error; err != nil {
		return domain.Brief{}, false, fmt.Errorf("select pr brief: %w", err)
	}
	if len(rows) == 0 {
		return domain.Brief{}, false, nil
	}
	b, ok := parseDocument(rows[0].JSON)
	return b, ok, nil
}

// SaveBrief upserts the brief of the pull request.
func (s *Store) SaveBrief(ctx context.Context, prID string, b domain.Brief) error {
	raw, err := json.Marshal(toDocument(b))
	if err != nil {
		return fmt.Errorf("encode pr brief: %w", err)
	}
	err = s.conn(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "pr_id"}},
		DoUpdates: clause.AssignmentColumns([]string{"json"}),
	}).Create(&briefRow{PRID: prID, JSON: raw}).Error
	if err != nil {
		return fmt.Errorf("upsert pr brief: %w", err)
	}
	return nil
}

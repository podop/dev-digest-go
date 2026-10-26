// Package postgres persists the onboarding context with GORM: the tour of a repository
// (onboarding.json, one row per repository) and the lookup of a repository in the workspace.
// Row structs are declared here; every method takes its handle from platformpg.Conn, so a store
// called inside Tx.InTx joins the caller's transaction.
package postgres

import (
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"dev-digest-go/backend/internal/onboarding/app"
	"dev-digest-go/backend/internal/onboarding/domain"
	"dev-digest-go/backend/internal/platform/codehost"
	platformpg "dev-digest-go/backend/internal/platform/postgres"
)

// foreignKeyViolation is the SQLSTATE of a row whose repository is gone.
const foreignKeyViolation = "23503"

// Store is the GORM-backed implementation of app.Store.
type Store struct {
	db *gorm.DB
}

var _ app.Store = (*Store)(nil)

// NewStore builds a Store on a GORM handle.
func NewStore(db *gorm.DB) *Store { return &Store{db: db} }

func (s *Store) conn(ctx context.Context) *gorm.DB { return platformpg.Conn(ctx, s.db) }

// repoRow maps the columns of repos the context reads.
type repoRow struct {
	ID            string `gorm:"column:id"`
	WorkspaceID   string `gorm:"column:workspace_id"`
	Owner         string `gorm:"column:owner"`
	Name          string `gorm:"column:name"`
	FullName      string `gorm:"column:full_name"`
	DefaultBranch string `gorm:"column:default_branch"`
	Provider      string `gorm:"column:provider"`
	Host          string `gorm:"column:host"`
	ProjectID     *int64 `gorm:"column:project_id"`
}

func (repoRow) TableName() string { return "repos" }

// jsonb maps a jsonb column to its text.
type jsonb []byte

// Scan implements sql.Scanner.
func (v *jsonb) Scan(src any) error {
	switch s := src.(type) {
	case nil:
		*v = nil
	case []byte:
		*v = append((*v)[:0], s...)
	case string:
		*v = jsonb(s)
	default:
		return fmt.Errorf("scan jsonb: unsupported type %T", src)
	}
	return nil
}

// Value implements driver.Valuer; the text form is cast to jsonb by the server.
func (v jsonb) Value() (driver.Value, error) {
	if v == nil {
		return nil, nil
	}
	return string(v), nil
}

// tourRow maps onboarding.
type tourRow struct {
	RepoID      string    `gorm:"column:repo_id;primaryKey;autoIncrement:false"`
	JSON        jsonb     `gorm:"column:json;type:jsonb"`
	GeneratedAt time.Time `gorm:"column:generated_at"`
}

func (tourRow) TableName() string { return "onboarding" }

// FindRepo implements app.Store: the repository when it belongs to the workspace.
func (s *Store) FindRepo(ctx context.Context, workspaceID, repoID string) (domain.Repo, error) {
	var row repoRow
	err := s.conn(ctx).Where("workspace_id = ? AND id = ?", workspaceID, repoID).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return domain.Repo{}, fmt.Errorf("repo %s: %w", repoID, domain.ErrNotFound)
	}
	if err != nil {
		return domain.Repo{}, fmt.Errorf("select repo: %w", err)
	}
	out := domain.Repo{
		ID: row.ID, WorkspaceID: row.WorkspaceID, Owner: row.Owner, Name: row.Name,
		FullName: row.FullName, DefaultBranch: row.DefaultBranch, Provider: row.Provider, Host: row.Host,
	}
	if row.ProjectID != nil {
		out.ProjectID = *row.ProjectID
	}
	// The use case hands Owner and Name to the blob reader as the clone location.
	out.Owner, out.Name = codehost.CloneKey(out.Provider, out.Host, out.ProjectID, row.Owner, row.Name)
	return out, nil
}

// Tour implements app.Store. A document that does not satisfy the contract reads as absent.
func (s *Store) Tour(ctx context.Context, repoID string) (domain.Tour, bool, error) {
	var row tourRow
	err := s.conn(ctx).Where("repo_id = ?", repoID).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return domain.Tour{}, false, nil
	}
	if err != nil {
		return domain.Tour{}, false, fmt.Errorf("select tour: %w", err)
	}
	t, ok := DecodeTour(row.JSON)
	return t, ok, nil
}

// SaveTour implements app.Store: an upsert of (repo_id, json, generated_at) where generated_at is
// the tour's own.
func (s *Store) SaveTour(ctx context.Context, t domain.Tour) error {
	doc, err := EncodeTour(t)
	if err != nil {
		return err
	}
	row := tourRow{RepoID: t.RepoID, JSON: doc, GeneratedAt: t.GeneratedAt.UTC()}
	err = s.conn(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "repo_id"}},
		DoUpdates: clause.AssignmentColumns([]string{"json", "generated_at"}),
	}).Create(&row).Error
	if err != nil {
		if sqlState(err) == foreignKeyViolation {
			return fmt.Errorf("save tour: %w", domain.ErrNotFound)
		}
		return fmt.Errorf("save tour: %w", err)
	}
	return nil
}

// sqlState returns the SQLSTATE of a database error, "" for any other error.
func sqlState(err error) string {
	var coded interface{ SQLState() string }
	if errors.As(err, &coded) {
		return coded.SQLState()
	}
	return ""
}

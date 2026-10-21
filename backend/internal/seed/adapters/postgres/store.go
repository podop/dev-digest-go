// Package postgres implements the seed store with GORM. Every write is an insert that
// ignores a conflict on the natural key, so a re-run changes nothing.
package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"dev-digest-go/backend/internal/seed"
)

// seedLockKey serialises concurrent seed runs (ASCII "devdgsed"); workspaces and users have
// no unique constraint on their natural key, so two parallel runs could otherwise both insert.
const seedLockKey int64 = 0x6465766467_736564

type workspaceRow struct {
	ID   string `gorm:"column:id;primaryKey;default:gen_random_uuid()"`
	Name string `gorm:"column:name"`
}

func (workspaceRow) TableName() string { return "workspaces" }

type userRow struct {
	ID    string `gorm:"column:id;primaryKey;default:gen_random_uuid()"`
	Email string `gorm:"column:email"`
	Name  string `gorm:"column:name"`
}

func (userRow) TableName() string { return "users" }

type memberRow struct {
	WorkspaceID string `gorm:"column:workspace_id"`
	UserID      string `gorm:"column:user_id"`
	Role        string `gorm:"column:role"`
}

func (memberRow) TableName() string { return "workspace_members" }

type settingRow struct {
	ID          string          `gorm:"column:id;primaryKey;default:gen_random_uuid()"`
	WorkspaceID string          `gorm:"column:workspace_id"`
	UserID      string          `gorm:"column:user_id"`
	Key         string          `gorm:"column:key"`
	Value       json.RawMessage `gorm:"column:value;type:jsonb"`
}

func (settingRow) TableName() string { return "settings" }

type repoRow struct {
	ID            string `gorm:"column:id;primaryKey;default:gen_random_uuid()"`
	WorkspaceID   string `gorm:"column:workspace_id"`
	Owner         string `gorm:"column:owner"`
	Name          string `gorm:"column:name"`
	FullName      string `gorm:"column:full_name"`
	DefaultBranch string `gorm:"column:default_branch"`
	CreatedBy     string `gorm:"column:created_by"`
}

func (repoRow) TableName() string { return "repos" }

type pullRequestRow struct {
	ID          string `gorm:"column:id;primaryKey;default:gen_random_uuid()"`
	WorkspaceID string `gorm:"column:workspace_id"`
	RepoID      string `gorm:"column:repo_id"`
	Number      int    `gorm:"column:number"`
	Title       string `gorm:"column:title"`
	Author      string `gorm:"column:author"`
	Branch      string `gorm:"column:branch"`
	Base        string `gorm:"column:base"`
	HeadSHA     string `gorm:"column:head_sha"`
	Additions   int    `gorm:"column:additions"`
	Deletions   int    `gorm:"column:deletions"`
	FilesCount  int    `gorm:"column:files_count"`
	Status      string `gorm:"column:status"`
	Body        string `gorm:"column:body"`
}

func (pullRequestRow) TableName() string { return "pull_requests" }

type fileRow struct {
	ID        string  `gorm:"column:id;primaryKey;default:gen_random_uuid()"`
	PRID      string  `gorm:"column:pr_id"`
	Path      string  `gorm:"column:path"`
	Additions int     `gorm:"column:additions"`
	Deletions int     `gorm:"column:deletions"`
	Patch     *string `gorm:"column:patch"`
}

func (fileRow) TableName() string { return "pr_files" }

type commitRow struct {
	ID      string `gorm:"column:id;primaryKey;default:gen_random_uuid()"`
	PRID    string `gorm:"column:pr_id"`
	SHA     string `gorm:"column:sha"`
	Message string `gorm:"column:message"`
	Author  string `gorm:"column:author"`
}

func (commitRow) TableName() string { return "pr_commits" }

// Store runs the seed against Postgres.
type Store struct {
	db *gorm.DB
}

var _ seed.Store = (*Store)(nil)

// NewStore builds a Store on a GORM handle.
func NewStore(db *gorm.DB) *Store { return &Store{db: db} }

// InTx runs fn in one transaction, holding a transaction-scoped advisory lock.
func (s *Store) InTx(ctx context.Context, fn func(ctx context.Context, tx seed.Tx) error) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec(`SELECT pg_advisory_xact_lock(?)`, seedLockKey).Error; err != nil {
			return fmt.Errorf("seed lock: %w", err)
		}
		return fn(ctx, &txStore{tx: tx})
	})
}

type txStore struct {
	tx *gorm.DB
}

var _ seed.Tx = (*txStore)(nil)

func (t *txStore) EnsureWorkspace(ctx context.Context, name string) (string, bool, error) {
	var row workspaceRow
	err := t.tx.WithContext(ctx).Where("name = ?", name).Order("created_at, id").Take(&row).Error
	if err == nil {
		return row.ID, false, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return "", false, fmt.Errorf("find workspace: %w", err)
	}
	row = workspaceRow{Name: name}
	if err := t.tx.WithContext(ctx).Create(&row).Error; err != nil {
		return "", false, fmt.Errorf("insert workspace: %w", err)
	}
	return row.ID, true, nil
}

func (t *txStore) EnsureUser(ctx context.Context, email, name string) (string, bool, error) {
	var row userRow
	err := t.tx.WithContext(ctx).Where("email = ?", email).Order("created_at, id").Take(&row).Error
	if err == nil {
		return row.ID, false, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return "", false, fmt.Errorf("find user: %w", err)
	}
	row = userRow{Email: email, Name: name}
	if err := t.tx.WithContext(ctx).Create(&row).Error; err != nil {
		return "", false, fmt.Errorf("insert user: %w", err)
	}
	return row.ID, true, nil
}

func (t *txStore) EnsureMember(ctx context.Context, workspaceID, userID, role string) (bool, error) {
	res := t.tx.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).
		Create(&memberRow{WorkspaceID: workspaceID, UserID: userID, Role: role})
	if res.Error != nil {
		return false, fmt.Errorf("insert workspace member: %w", res.Error)
	}
	return res.RowsAffected == 1, nil
}

func (t *txStore) EnsureSetting(ctx context.Context, workspaceID, userID, key string, value json.RawMessage) (bool, error) {
	res := t.tx.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).
		Create(&settingRow{WorkspaceID: workspaceID, UserID: userID, Key: key, Value: value})
	if res.Error != nil {
		return false, fmt.Errorf("insert setting: %w", res.Error)
	}
	return res.RowsAffected == 1, nil
}

func (t *txStore) EnsureRepo(ctx context.Context, r seed.Repo) (string, bool, error) {
	row := repoRow{
		WorkspaceID: r.WorkspaceID, Owner: r.Owner, Name: r.Name, FullName: r.FullName,
		DefaultBranch: r.DefaultBranch, CreatedBy: r.CreatedBy,
	}
	db := t.tx.WithContext(ctx)
	res := db.Clauses(clause.OnConflict{DoNothing: true}).Create(&row)
	if res.Error != nil {
		return "", false, fmt.Errorf("insert repo: %w", res.Error)
	}
	if res.RowsAffected == 1 {
		return row.ID, true, nil
	}
	var existing repoRow
	if err := db.Where("workspace_id = ? AND full_name = ?", r.WorkspaceID, r.FullName).Take(&existing).Error; err != nil {
		return "", false, fmt.Errorf("find repo: %w", err)
	}
	return existing.ID, false, nil
}

func (t *txStore) EnsurePullRequest(ctx context.Context, p seed.PullRequest) (string, bool, error) {
	row := pullRequestRow{
		WorkspaceID: p.WorkspaceID, RepoID: p.RepoID, Number: p.Number, Title: p.Title,
		Author: p.Author, Branch: p.Branch, Base: p.Base, HeadSHA: p.HeadSHA,
		Additions: p.Additions, Deletions: p.Deletions, FilesCount: p.FilesCount,
		Status: p.Status, Body: p.Body,
	}
	db := t.tx.WithContext(ctx)
	res := db.Clauses(clause.OnConflict{DoNothing: true}).Create(&row)
	if res.Error != nil {
		return "", false, fmt.Errorf("insert pull request: %w", res.Error)
	}
	if res.RowsAffected == 1 {
		return row.ID, true, nil
	}
	var existing pullRequestRow
	if err := db.Where("repo_id = ? AND number = ?", p.RepoID, p.Number).Take(&existing).Error; err != nil {
		return "", false, fmt.Errorf("find pull request: %w", err)
	}
	return existing.ID, false, nil
}

func (t *txStore) EnsureFile(ctx context.Context, prID string, f seed.File) (bool, bool, error) {
	patch := f.Patch
	row := fileRow{PRID: prID, Path: f.Path, Additions: f.Additions, Deletions: f.Deletions}
	if patch != "" {
		row.Patch = &patch
	}
	db := t.tx.WithContext(ctx)
	res := db.Clauses(clause.OnConflict{DoNothing: true}).Create(&row)
	if res.Error != nil {
		return false, false, fmt.Errorf("insert pr file: %w", res.Error)
	}
	if res.RowsAffected == 1 || patch == "" {
		return res.RowsAffected == 1, false, nil
	}
	fill := db.Model(&fileRow{}).
		Where("pr_id = ? AND path = ? AND patch IS NULL", prID, f.Path).
		Update("patch", patch)
	if fill.Error != nil {
		return false, false, fmt.Errorf("fill pr file patch: %w", fill.Error)
	}
	return false, fill.RowsAffected == 1, nil
}

func (t *txStore) EnsureCommit(ctx context.Context, prID string, c seed.Commit) (bool, error) {
	res := t.tx.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).
		Create(&commitRow{PRID: prID, SHA: c.SHA, Message: c.Message, Author: c.Author})
	if res.Error != nil {
		return false, fmt.Errorf("insert pr commit: %w", res.Error)
	}
	return res.RowsAffected == 1, nil
}

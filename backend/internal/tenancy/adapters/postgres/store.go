// Package postgres reads the single-tenant identity rows with GORM.
package postgres

import (
	"context"
	"errors"
	"fmt"

	"gorm.io/gorm"

	"dev-digest-go/backend/internal/tenancy"
)

type workspaceRow struct {
	ID string `gorm:"column:id"`
}

func (workspaceRow) TableName() string { return "workspaces" }

type userRow struct {
	ID string `gorm:"column:id"`
}

func (userRow) TableName() string { return "users" }

// Store finds the default workspace and the system user.
type Store struct {
	db *gorm.DB
}

// NewStore builds a Store on a GORM handle.
func NewStore(db *gorm.DB) *Store { return &Store{db: db} }

// FindIdentity returns the ids of the workspace named workspaceName and the user with email
// userEmail. When several rows share a natural key (neither column is unique) the oldest wins.
// It returns tenancy.ErrNotProvisioned when either row is missing.
func (s *Store) FindIdentity(ctx context.Context, workspaceName, userEmail string) (tenancy.Identity, error) {
	db := s.db.WithContext(ctx)

	var ws workspaceRow
	if err := db.Where("name = ?", workspaceName).Order("created_at, id").Take(&ws).Error; err != nil {
		return tenancy.Identity{}, notFoundOr(err, "find workspace")
	}
	var user userRow
	if err := db.Where("email = ?", userEmail).Order("created_at, id").Take(&user).Error; err != nil {
		return tenancy.Identity{}, notFoundOr(err, "find user")
	}
	return tenancy.Identity{WorkspaceID: ws.ID, UserID: user.ID}, nil
}

func notFoundOr(err error, what string) error {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return fmt.Errorf("%s: %w", what, tenancy.ErrNotProvisioned)
	}
	return fmt.Errorf("%s: %w", what, err)
}

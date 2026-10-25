// Package postgres is the GORM adapter of the workspace context. It reads the workspace's
// counts and export records, deletes its run history and prunes expired traces. The methods
// live in reads.go, history.go and prune.go.
package postgres

import "gorm.io/gorm"

// Store implements the Reader, History, Workspaces and TracePruner ports of the app package.
type Store struct {
	db *gorm.DB
}

// NewStore builds a Store on a GORM handle.
func NewStore(db *gorm.DB) *Store { return &Store{db: db} }

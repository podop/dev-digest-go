// Package app holds the use cases of the spend context: the month's spend against the cap, and
// the admission check the LLM entry points run before starting work. It reads cost records and
// the cap through the ports below; it never writes.
//
// This file holds the ports the use cases depend on; api.go holds the use cases' own interface,
// which transport and the other contexts' budget ports depend on.
package app

import (
	"context"
	"time"

	"dev-digest-go/backend/internal/spend/domain"
)

// Clock returns the current time (injectable for tests).
type Clock func() time.Time

// Ledger is the read-only view of the cost records of all contexts.
type Ledger interface {
	// MonthSpend returns the workspace's spend with cost records dated in [from, to), NULL
	// costs counted as 0 and mirror records (CI runs, eval suite runs) excluded.
	MonthSpend(ctx context.Context, workspaceID string, from, to time.Time) (domain.Micros, error)
}

// Caps provides the workspace's monthly spend cap, owned by the settings context.
type Caps interface {
	// Cap returns the monthly cap; nil means no cap is set.
	Cap(ctx context.Context, workspaceID string) (*domain.Micros, error)
}

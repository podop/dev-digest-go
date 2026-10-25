// Package app holds the use cases of the spend context: the month's spend against the cap, and
// the admission check the LLM entry points run before starting work, and the recorder that appends
// one ledger entry per answered LLM attempt and per new CI run. It reads the stored month totals
// and the cap, and appends entries, through the ports below.
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

// Totals is the read view of the stored month totals (FR9). The spend read and the cap check use
// only this port, never the individual entries or the per-feature cost records.
type Totals interface {
	// MonthTotal returns the stored total of the workspace and the UTC calendar month starting at
	// month (domain.MonthStart): the sum of the known costs and the unknown-cost count. A month
	// without entries is the zero total, not an error (EC19).
	MonthTotal(ctx context.Context, workspaceID string, month time.Time) (domain.MonthTotal, error)
}

// Entries is the append port of the ledger.
type Entries interface {
	// Append stores the entry and adds it to its month's total in one all-or-nothing step (FR9).
	// stored is false, with a nil error, when an entry with the same DedupeKey already exists:
	// appending twice is not an error. When ctx carries a transaction of the caller (CI ingest),
	// the append joins it.
	Append(ctx context.Context, e domain.Entry) (stored bool, err error)
}

// Caps provides the workspace's monthly spend cap, owned by the settings context.
type Caps interface {
	// Cap returns the monthly cap; nil means no cap is set.
	Cap(ctx context.Context, workspaceID string) (*domain.Micros, error)
}

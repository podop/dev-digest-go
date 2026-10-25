// Package postgres is the storage of the cost ledger: it appends entries to cost_ledger, keeps
// the stored month totals of spend_month_totals in step with them, and reads those totals. It
// implements the app.Totals and app.Entries ports of the spend context. Every call gets its
// handle from platformpg.Conn, so a store called inside a transaction joins it.
package postgres

import (
	"context"
	"fmt"
	"hash/fnv"
	"time"

	"gorm.io/gorm"

	platformpg "dev-digest-go/backend/internal/platform/postgres"
	"dev-digest-go/backend/internal/spend/app"
	"dev-digest-go/backend/internal/spend/domain"
)

var (
	_ app.Totals  = (*Store)(nil)
	_ app.Entries = (*Store)(nil)
)

// slots is the number of rows that hold the total of one workspace and month (the CHECK of
// spend_month_totals.slot allows 0..7). Concurrent appends of one workspace land on different
// rows, so they rarely wait for each other's row lock (NFR3, A8).
const slots = 8

// Store is the GORM-backed ledger of the spend context.
type Store struct {
	db *gorm.DB
}

// NewStore builds a Store on a GORM handle.
func NewStore(db *gorm.DB) *Store { return &Store{db: db} }

// appendSQL stores one entry and, only when it was stored, adds it to the total of its UTC month
// in the slot given by @slot. It is one statement, so it is all-or-nothing on its own and joins
// the caller's transaction when there is one. It is Raw because the builder cannot express a
// data-modifying CTE feeding an upsert.
//
// A duplicate dedupe_key inserts nothing (also when a concurrent append of the same key has not
// committed yet: the insert waits for it), the CTE returns no row and the upsert does nothing, so
// the statement affects 0 rows. A stored entry affects 1 row. A NULL cost adds 0 to spent_micros
// and 1 to unpriced_count. The slot total saturates at the bigint maximum instead of failing the
// append with an overflow (D19): the sum is taken in numeric and clamped.
const appendSQL = `
WITH ins AS (
	INSERT INTO cost_ledger
		(workspace_id, occurred_at, kind, origin, provider, model, tokens_in, tokens_out,
		 cost_micros, ref_id, dedupe_key)
	VALUES (@ws, @at, @kind, @origin, @provider, @model, @tin, @tout, @cost, @ref, @key)
	ON CONFLICT (dedupe_key) DO NOTHING
	RETURNING workspace_id, occurred_at, cost_micros
)
INSERT INTO spend_month_totals (workspace_id, month, slot, spent_micros, unpriced_count, entry_count)
SELECT workspace_id,
       date_trunc('month', occurred_at AT TIME ZONE 'UTC')::date,
       CAST(@slot AS smallint),
       COALESCE(cost_micros, 0),
       CASE WHEN cost_micros IS NULL THEN 1 ELSE 0 END,
       1
FROM ins
ON CONFLICT (workspace_id, month, slot) DO UPDATE SET
	spent_micros   = LEAST(spend_month_totals.spent_micros::numeric + EXCLUDED.spent_micros, 9223372036854775807::numeric)::bigint,
	unpriced_count = spend_month_totals.unpriced_count + EXCLUDED.unpriced_count,
	entry_count    = spend_month_totals.entry_count + EXCLUDED.entry_count`

// Append implements app.Entries. An empty RefID is stored as NULL; an empty Provider or Model
// stays ”.
func (s *Store) Append(ctx context.Context, e domain.Entry) (bool, error) {
	var cost, ref any // nil binds as NULL
	if e.Cost != nil {
		cost = int64(*e.Cost)
	}
	if e.RefID != "" {
		ref = e.RefID
	}
	res := platformpg.Conn(ctx, s.db).Exec(appendSQL, map[string]any{
		"ws":       e.WorkspaceID,
		"at":       e.OccurredAt.UTC(),
		"kind":     string(e.Kind),
		"origin":   string(e.Origin),
		"provider": e.Provider,
		"model":    e.Model,
		"tin":      e.TokensIn,
		"tout":     e.TokensOut,
		"cost":     cost,
		"ref":      ref,
		"key":      e.DedupeKey,
		"slot":     slotOf(e.DedupeKey),
	})
	if res.Error != nil {
		return false, fmt.Errorf("append ledger entry: %w", res.Error)
	}
	return res.RowsAffected > 0, nil
}

// slotOf picks the total slot of an entry from its dedupe key: random for live entries, so the
// appends spread evenly, and the same for the same key.
func slotOf(key string) int {
	h := fnv.New32a()
	_, _ = h.Write([]byte(key))
	return int(h.Sum32() % slots)
}

// maxBigint is the largest spent_micros; the month total saturates here (D19).
const maxBigint = "9223372036854775807"

// MonthTotal implements app.Totals: the stored total of the workspace and the UTC month that
// contains month.
func (s *Store) MonthTotal(ctx context.Context, workspaceID string, month time.Time) (domain.MonthTotal, error) {
	// SUM of bigint is numeric, so it is cast back after clamping the sum of the (at most 8) slot
	// rows at the bigint maximum; no row sums to 0.
	var row struct{ Spent, Unpriced int64 }
	err := platformpg.Conn(ctx, s.db).Table("spend_month_totals").
		Select("CAST(LEAST(COALESCE(SUM(spent_micros), 0), "+maxBigint+") AS bigint) AS spent, "+
			"CAST(COALESCE(SUM(unpriced_count), 0) AS bigint) AS unpriced").
		Where("workspace_id = ? AND month = ?", workspaceID, domain.MonthStart(month).Format(time.DateOnly)).
		Scan(&row).Error
	if err != nil {
		return domain.MonthTotal{}, fmt.Errorf("read month total: %w", err)
	}
	return domain.MonthTotal{Spent: domain.Micros(row.Spent), Unpriced: int(row.Unpriced)}, nil
}

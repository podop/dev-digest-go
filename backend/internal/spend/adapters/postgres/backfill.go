package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	platformpg "dev-digest-go/backend/internal/platform/postgres"
	"dev-digest-go/backend/internal/spend/app"
	"dev-digest-go/backend/internal/spend/domain"
)

// backfillRow is the single summary row (id = 1) the ledger migration leaves.
type backfillRow struct {
	Created    []byte `gorm:"column:created"`
	Skipped    int64  `gorm:"column:skipped"`
	DurationMs int64  `gorm:"column:duration_ms"`
}

func (backfillRow) TableName() string { return "public.cost_ledger_backfill" }

// ClaimBackfillSummary returns the summary of the ledger backfill if no earlier start has claimed
// it: the row is stamped logged_at = now() as it is read, so a second call (or a concurrent one)
// gets claimed = false. A kind this build does not know is left out of the counts.
func (s *Store) ClaimBackfillSummary(ctx context.Context) (summary app.BackfillSummary, claimed bool, err error) {
	// One UPDATE ... RETURNING takes the row and stamps it, so of several starts exactly one gets
	// it (once-only report, FR15); the others update no row.
	var row backfillRow
	res := platformpg.Conn(ctx, s.db).Model(&row).
		Clauses(clause.Returning{Columns: []clause.Column{{Name: "created"}, {Name: "skipped"}, {Name: "duration_ms"}}}).
		Where("id = 1 AND logged_at IS NULL").
		Updates(map[string]any{"logged_at": gorm.Expr("now()")})
	if res.Error != nil {
		return app.BackfillSummary{}, false, fmt.Errorf("claim ledger backfill summary: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return app.BackfillSummary{}, false, nil
	}
	var perKind map[string]int
	if err := json.Unmarshal(row.Created, &perKind); err != nil {
		return app.BackfillSummary{}, false, fmt.Errorf("decode ledger backfill counts: %w", err)
	}
	created := make(map[domain.Kind]int, len(perKind))
	for name, n := range perKind {
		if kind, err := domain.ParseKind(name); err == nil {
			created[kind] = n
		}
	}
	return app.BackfillSummary{
		Created:  created,
		Skipped:  int(row.Skipped),
		Duration: time.Duration(row.DurationMs) * time.Millisecond,
	}, true, nil
}

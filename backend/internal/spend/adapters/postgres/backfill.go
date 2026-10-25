package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	platformpg "dev-digest-go/backend/internal/platform/postgres"
	"dev-digest-go/backend/internal/spend/app"
	"dev-digest-go/backend/internal/spend/domain"
)

// claimBackfillSQL takes the summary row the ledger migration left and stamps it in the same
// statement, so of several starts exactly one gets the row (once-only report, FR15). It is Raw
// because GORM's builder has no UPDATE ... RETURNING.
const claimBackfillSQL = `
UPDATE public.cost_ledger_backfill
   SET logged_at = now()
 WHERE id = 1 AND logged_at IS NULL
 RETURNING created, skipped, duration_ms`

// ClaimBackfillSummary returns the summary of the ledger backfill if no earlier start has claimed
// it: the row is stamped logged_at = now() as it is read, so a second call (or a concurrent one)
// gets claimed = false. A kind this build does not know is left out of the counts.
func (s *Store) ClaimBackfillSummary(ctx context.Context) (summary app.BackfillSummary, claimed bool, err error) {
	var rows []struct {
		Created    []byte
		Skipped    int64
		DurationMs int64
	}
	if err := platformpg.Conn(ctx, s.db).Raw(claimBackfillSQL).Scan(&rows).Error; err != nil {
		return app.BackfillSummary{}, false, fmt.Errorf("claim ledger backfill summary: %w", err)
	}
	if len(rows) == 0 {
		return app.BackfillSummary{}, false, nil
	}
	row := rows[0]
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

package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"dev-digest-go/backend/internal/platform/postgres"
	"dev-digest-go/backend/internal/workspace/app"
)

var _ app.TracePruner = (*Store)(nil)

// pruneBatch caps the traces one transaction deletes, so a prune never holds the advisory lock
// (and a long delete) for the whole backlog (spec EC11).
const pruneBatch = 5000

// PruneTraces deletes the run traces of the workspace's agent runs whose ran_at is strictly
// before cutoff, at most pruneBatch per transaction, and returns the total. Each batch takes the
// advisory lock a reset takes (waiting up to pruneLockTimeout, longer than a reset runs), so the
// two never run at once and neither fails; ctx is checked between batches and a
// cancelled ctx returns what was deleted so far with the error. It deletes nothing but traces:
// runs, reviews and findings stay.
func (s *Store) PruneTraces(ctx context.Context, workspaceID string, cutoff time.Time) (int64, error) {
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return total, fmt.Errorf("prune traces: %w", err)
		}
		var n int64
		err := postgres.NewTx(s.db).InTx(ctx, func(ctx context.Context) error {
			tx := postgres.Conn(ctx, s.db)
			if err := lockHistory(tx, workspaceID, pruneLockTimeout); err != nil {
				return err
			}
			// A set delete over a LIMITed subselect has no GORM builder, hence Exec.
			res := tx.Exec(`DELETE FROM run_traces WHERE run_id IN (
					SELECT t.run_id FROM run_traces t JOIN agent_runs r ON r.id = t.run_id
					WHERE r.workspace_id = @ws AND r.ran_at < @cutoff
					LIMIT @batch)`,
				sql.Named("ws", workspaceID), sql.Named("cutoff", cutoff), sql.Named("batch", pruneBatch))
			if res.Error != nil {
				return fmt.Errorf("delete traces: %w", res.Error)
			}
			n = res.RowsAffected
			return nil
		})
		if err != nil {
			return total, fmt.Errorf("prune traces: %w", err)
		}
		total += n
		if n < pruneBatch {
			return total, nil
		}
	}
}

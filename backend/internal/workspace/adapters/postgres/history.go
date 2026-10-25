package postgres

import (
	"context"
	"database/sql"
	"fmt"

	"gorm.io/gorm"

	"dev-digest-go/backend/internal/platform/postgres"
	"dev-digest-go/backend/internal/workspace/app"
	"dev-digest-go/backend/internal/workspace/domain"
)

var _ app.History = (*Store)(nil)

// resetLockTimeout bounds every wait of a reset for the workspace-history lock and the table
// locks (spec A4): a reset that cannot get them in time fails with an error and changes nothing.
//
// pruneLockTimeout is the wait of a prune batch for the history lock. It is longer than a reset
// can run (NFR3: about 30 s), so a prune that meets a reset waits it out instead of failing (spec
// EC11); a cancelled ctx still ends the wait at once.
const (
	resetLockTimeout = "10s"
	pruneLockTimeout = "60s"
)

// lockHistory is the first step of every writing transaction of this package. It sets the lock
// timeout and takes the advisory lock `workspace-history:<workspace>`, which serialises a reset
// and the prune batches of one workspace (spec EC11). The lock is released with the transaction.
// timeout is one of the package constants above, never request input. Advisory locks have no GORM
// builder, hence Exec.
func lockHistory(tx *gorm.DB, workspaceID, timeout string) error {
	if err := tx.Exec("SET LOCAL lock_timeout = '" + timeout + "'").Error; err != nil {
		return fmt.Errorf("set lock timeout: %w", err)
	}
	if err := tx.Exec(`SELECT pg_advisory_xact_lock(hashtextextended(?, 0))`, "workspace-history:"+workspaceID).Error; err != nil {
		return fmt.Errorf("lock workspace history: %w", err)
	}
	return nil
}

// Reset deletes the workspace's run history in one transaction. It takes the advisory lock, then
// a SHARE ROW EXCLUSIVE lock on agent_runs and eval_suite_runs: from then on no run can be
// inserted or change status until the transaction ends, so the check for running runs and the
// deletes are atomic (spec EC5). With a running agent run or eval suite run it deletes nothing
// and returns the count as active.
//
// Rows go children before parents, each kind with one set-based statement whose affected-row
// count is the answer. Every other table is kept (spec section 8); last_reviewed_sha of the
// workspace's pull requests is cleared (spec Q3).
func (s *Store) Reset(ctx context.Context, workspaceID string) (domain.Deleted, int, error) {
	var (
		deleted domain.Deleted
		active  int
	)
	err := postgres.NewTx(s.db).InTx(ctx, func(ctx context.Context) error {
		tx := postgres.Conn(ctx, s.db)
		if err := lockHistory(tx, workspaceID, resetLockTimeout); err != nil {
			return err
		}
		if err := tx.Exec(`LOCK TABLE agent_runs, eval_suite_runs IN SHARE ROW EXCLUSIVE MODE`).Error; err != nil {
			return fmt.Errorf("lock run tables: %w", err)
		}
		if err := tx.Raw(`SELECT
				(SELECT count(*) FROM agent_runs WHERE workspace_id = ? AND status = 'running') +
				(SELECT count(*) FROM eval_suite_runs WHERE workspace_id = ? AND status = 'running')`,
			workspaceID, workspaceID).Scan(&active).Error; err != nil {
			return fmt.Errorf("count running runs: %w", err)
		}
		if active > 0 {
			return nil
		}
		for _, step := range resetSteps(&deleted) {
			res := tx.Exec(step.sql, sql.Named("ws", workspaceID))
			if res.Error != nil {
				return fmt.Errorf("delete %s: %w", step.name, res.Error)
			}
			if step.count != nil {
				*step.count = res.RowsAffected
			}
		}
		return nil
	})
	if err != nil {
		return domain.Deleted{}, 0, fmt.Errorf("reset history: %w", err)
	}
	if active > 0 {
		return domain.Deleted{}, active, nil
	}
	return deleted, 0, nil
}

// resetStep is one set-based statement of the reset; its only argument is the named parameter @ws.
type resetStep struct {
	name  string
	sql   string
	count *int64 // receives the affected rows; nil for rows that are not reported
}

// Subqueries shared by the steps. A review belongs to the workspace by its own column or by its
// run; a CI run by an installation of one of the workspace's agents or by a run of the workspace
// (spec A3); an eval run by a case of the workspace or by a suite run of the workspace.
const (
	wsRuns   = `SELECT id FROM agent_runs WHERE workspace_id = @ws`
	wsReview = `SELECT id FROM reviews WHERE workspace_id = @ws OR run_id IN (` + wsRuns + `)`
)

// resetSteps lists the deletes in order: children before parents, and every table that
// references a deleted row is cleared before it (so no ON DELETE SET NULL or CASCADE fires on a
// row the answer should have counted).
func resetSteps(d *domain.Deleted) []resetStep {
	return []resetStep{
		{"findings", `DELETE FROM findings WHERE review_id IN (` + wsReview + `)`, &d.Findings},
		{"reviews", `DELETE FROM reviews WHERE id IN (` + wsReview + `)`, &d.Reviews},
		{"run traces", `DELETE FROM run_traces WHERE run_id IN (` + wsRuns + `)`, &d.RunTraces},
		{"run skills", `DELETE FROM agent_run_skills WHERE run_id IN (` + wsRuns + `)`, nil},
		{"ci runs", `DELETE FROM ci_runs WHERE ci_installation_id IN (
				SELECT i.id FROM ci_installations i JOIN agents a ON a.id = i.agent_id WHERE a.workspace_id = @ws)
			OR agent_run_id IN (` + wsRuns + `)`, &d.CIRuns},
		{"agent runs", `DELETE FROM agent_runs WHERE workspace_id = @ws`, &d.AgentRuns},
		{"multi agent runs", `DELETE FROM multi_agent_runs WHERE workspace_id = @ws`, &d.MultiAgentRuns},
		{"eval runs", `DELETE FROM eval_runs WHERE case_id IN (SELECT id FROM eval_cases WHERE workspace_id = @ws)
			OR run_id IN (SELECT id FROM eval_suite_runs WHERE workspace_id = @ws)`, &d.EvalRuns},
		{"eval suite runs", `DELETE FROM eval_suite_runs WHERE workspace_id = @ws`, &d.EvalSuiteRuns},
		{"composed reviews", `DELETE FROM composed_reviews WHERE pr_id IN (SELECT id FROM pull_requests WHERE workspace_id = @ws)`, &d.ComposedReviews},
		{"learnings", `DELETE FROM memory WHERE workspace_id = @ws`, &d.Learnings},
		{"reviewed markers", `UPDATE pull_requests SET last_reviewed_sha = NULL WHERE workspace_id = @ws AND last_reviewed_sha IS NOT NULL`, &d.PRsUnmarked},
	}
}

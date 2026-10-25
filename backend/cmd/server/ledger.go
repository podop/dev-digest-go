package main

import (
	"context"
	"log/slog"
	"time"

	"gorm.io/gorm"

	ciapp "dev-digest-go/backend/internal/ci/app"
	spendpg "dev-digest-go/backend/internal/spend/adapters/postgres"
	spendapp "dev-digest-go/backend/internal/spend/app"
)

// ciLedger adapts the spend recorder to CI ingest's Ledger port, so ci never imports spend and
// spend never imports ci: only this composition root knows both (cost ledger FR6).
type ciLedger struct{ recorder *spendapp.Recorder }

var _ ciapp.Ledger = ciLedger{}

// RecordCIRun maps the stored run field by field and appends its entry on ctx, the transaction
// that stores the run. The recorder builds the dedupe key from the run id and attempt and treats
// a run that already has its entry as a no-op.
func (l ciLedger) RecordCIRun(ctx context.Context, run ciapp.CIRun) error {
	return l.recorder.RecordCIRun(ctx, spendapp.CIRun{
		WorkspaceID: run.WorkspaceID,
		AgentRunID:  run.AgentRunID,
		GithubRunID: run.GithubRunID,
		Attempt:     run.Attempt,
		OccurredAt:  run.OccurredAt,
		Provider:    &run.Provider,
		Model:       run.Model,
		CostUSD:     run.CostUSD,
	})
}

// reportLedgerBackfill logs the summary of the ledger backfill once (FR15): the store claims the
// summary row the migration left, if no earlier start has claimed it, and the recorder logs its
// counts. Without such a row (nothing to report, or already reported) it does nothing. It runs
// after the migrations.
func reportLedgerBackfill(ctx context.Context, db *gorm.DB, log *slog.Logger) error {
	store := spendpg.NewStore(db)
	summary, claimed, err := store.ClaimBackfillSummary(ctx)
	if err != nil {
		return err
	}
	if claimed {
		spendapp.NewRecorder(store, time.Now, log).ReportBackfill(ctx, summary)
	}
	return nil
}

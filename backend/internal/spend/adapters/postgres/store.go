// Package postgres reads the cost records of every context to total a workspace's spend. It
// implements the app.Ledger port of the spend context and never writes. Every call gets its
// handle from platformpg.Conn, so a store called inside a transaction joins it.
package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"gorm.io/gorm"

	platformpg "dev-digest-go/backend/internal/platform/postgres"
	"dev-digest-go/backend/internal/spend/app"
	"dev-digest-go/backend/internal/spend/domain"
)

var _ app.Ledger = (*Store)(nil)

// Store is the GORM-backed ledger of the spend context.
type Store struct {
	db *gorm.DB
}

// NewStore builds a Store on a GORM handle.
func NewStore(db *gorm.DB) *Store { return &Store{db: db} }

// monthSpendSQL totals the cost of the six record kinds that carry a real LLM cost, for one
// workspace and a half-open time range [@from, @to). It is Raw because the GORM builder cannot
// express a UNION ALL over six tables with different joins, time columns and cost sources;
// everything is summed in the database (one row back, no per-record transfer).
//
// Sources (cost column / time column): agent_runs.cost_usd / ran_at; eval_runs.cost_usd / ran_at
// (workspace through eval_cases); convention_scans.cost_usd / started_at; pr_intent.cost_usd /
// derived_at (workspace through pull_requests); pr_brief.json cost_usd / json generated_at
// (workspace through pull_requests); onboarding.json cost_usd / generated_at (workspace through
// repos). ci_runs and eval_suite_runs are left out on purpose: they mirror the cost of an
// agent_runs row and of its eval_runs rows, so counting them would count the money twice.
//
// The pr_brief and onboarding costs live in a jsonb document that is not schema-checked, and
// pr_brief's time is a string inside it. A cast of a bad value raises an error for the whole
// query, so each cast sits in a CASE (evaluated in order, unlike a WHERE conjunction) behind
// jsonb_typeof / pg_input_is_valid (PostgreSQL 16+; a JSON number such as 20261015 would pass
// pg_input_is_valid as a date, hence the string check); a document that fails the guard yields NULL
// and is skipped, never an error. NULL costs and NULL times drop out of SUM / the range.
//
// The sum is rounded to 6 decimals (the precision of the columns) and returned as integer
// micro-dollars.
const monthSpendSQL = `
SELECT (round(COALESCE(SUM(c), 0), 6) * 1000000)::bigint AS micros
FROM (
	SELECT cost_usd::numeric AS c
	FROM agent_runs
	WHERE workspace_id = @ws AND ran_at >= @from AND ran_at < @to
	UNION ALL
	SELECT r.cost_usd::numeric
	FROM eval_runs r
	JOIN eval_cases k ON k.id = r.case_id
	WHERE k.workspace_id = @ws AND r.ran_at >= @from AND r.ran_at < @to
	UNION ALL
	SELECT cost_usd::numeric
	FROM convention_scans
	WHERE workspace_id = @ws AND started_at >= @from AND started_at < @to
	UNION ALL
	SELECT i.cost_usd::numeric
	FROM pr_intent i
	JOIN pull_requests p ON p.id = i.pr_id
	WHERE p.workspace_id = @ws AND i.derived_at >= @from AND i.derived_at < @to
	UNION ALL
	SELECT CASE WHEN jsonb_typeof(b.json -> 'cost_usd') = 'number'
	            THEN (b.json ->> 'cost_usd')::numeric END
	FROM pr_brief b
	JOIN pull_requests p ON p.id = b.pr_id
	WHERE p.workspace_id = @ws
	  AND CASE WHEN jsonb_typeof(b.json -> 'generated_at') = 'string'
	            AND pg_input_is_valid(b.json ->> 'generated_at', 'timestamptz')
	           THEN (b.json ->> 'generated_at')::timestamptz END >= @from
	  AND CASE WHEN jsonb_typeof(b.json -> 'generated_at') = 'string'
	            AND pg_input_is_valid(b.json ->> 'generated_at', 'timestamptz')
	           THEN (b.json ->> 'generated_at')::timestamptz END < @to
	UNION ALL
	SELECT CASE WHEN jsonb_typeof(o.json -> 'cost_usd') = 'number'
	            THEN (o.json ->> 'cost_usd')::numeric END
	FROM onboarding o
	JOIN repos rp ON rp.id = o.repo_id
	WHERE rp.workspace_id = @ws AND o.generated_at >= @from AND o.generated_at < @to
) costs`

// MonthSpend implements app.Ledger: the workspace's spend with cost records dated in [from, to).
func (s *Store) MonthSpend(ctx context.Context, workspaceID string, from, to time.Time) (domain.Micros, error) {
	var micros int64
	err := platformpg.Conn(ctx, s.db).Raw(monthSpendSQL,
		sql.Named("ws", workspaceID), sql.Named("from", from.UTC()), sql.Named("to", to.UTC()),
	).Scan(&micros).Error
	if err != nil {
		return 0, fmt.Errorf("sum month spend: %w", err)
	}
	return domain.Micros(micros), nil
}

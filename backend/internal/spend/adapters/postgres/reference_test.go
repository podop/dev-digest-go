package postgres_test

import (
	"database/sql"
	"time"

	"dev-digest-go/backend/internal/spend/domain"
)

// The month spend as the system derived it before the cost ledger (Settings A FR12): the
// query below is the old store's, verbatim and test-only. The backfill parity tests (AC13) run
// it on the pre-upgrade data and compare its result with the stored totals after the upgrade.

// referenceMonthSpendSQL totals the cost of the six record kinds that carry a real LLM cost, for one
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
//
// Shape, for NFR4 (measured with 100,000 agent_runs and 50,000 eval_runs in the month): each
// source is summed inside its own branch, so the 150,000 cost rows never travel through the
// Append node (about 25 ms less); agent_runs and eval_runs are read from the covering indexes of
// migration 0003 (index-only scans); and the pr_brief document is detoasted once per row: every
// jsonb operator detoasts its argument again, and the old shape did so about eight times per
// brief (about 165 ms for 2,000 briefs). "|| '{}'" turns the stored value into one in-memory
// document that the later operators read without decompressing; OFFSET 0 keeps the planner from
// folding it back into the operators. For a document that is not an object the result is an
// array, whose keys are all NULL, so it is skipped exactly as before.
const referenceMonthSpendSQL = `
SELECT (round(COALESCE(SUM(c), 0), 6) * 1000000)::bigint AS micros
FROM (
	SELECT SUM(cost_usd::numeric) AS c
	FROM agent_runs
	WHERE workspace_id = @ws AND ran_at >= @from AND ran_at < @to
	UNION ALL
	SELECT SUM(r.cost_usd::numeric)
	FROM eval_runs r
	JOIN eval_cases k ON k.id = r.case_id
	WHERE k.workspace_id = @ws AND r.ran_at >= @from AND r.ran_at < @to
	UNION ALL
	SELECT SUM(cost_usd::numeric)
	FROM convention_scans
	WHERE workspace_id = @ws AND started_at >= @from AND started_at < @to
	UNION ALL
	SELECT SUM(i.cost_usd::numeric)
	FROM pr_intent i
	JOIN pull_requests p ON p.id = i.pr_id
	WHERE p.workspace_id = @ws AND i.derived_at >= @from AND i.derived_at < @to
	UNION ALL
	SELECT SUM(CASE WHEN jsonb_typeof(d.doc -> 'cost_usd') = 'number' THEN (d.doc ->> 'cost_usd')::numeric END)
	FROM pr_brief b
	JOIN pull_requests p ON p.id = b.pr_id
	CROSS JOIN LATERAL (SELECT b.json || '{}'::jsonb AS doc OFFSET 0) d
	WHERE p.workspace_id = @ws
	  AND CASE WHEN jsonb_typeof(d.doc -> 'generated_at') = 'string'
	            AND pg_input_is_valid(d.doc ->> 'generated_at', 'timestamptz')
	           THEN (d.doc ->> 'generated_at')::timestamptz END >= @from
	  AND CASE WHEN jsonb_typeof(d.doc -> 'generated_at') = 'string'
	            AND pg_input_is_valid(d.doc ->> 'generated_at', 'timestamptz')
	           THEN (d.doc ->> 'generated_at')::timestamptz END < @to
	UNION ALL
	SELECT SUM(CASE WHEN jsonb_typeof(o.json -> 'cost_usd') = 'number'
	                THEN (o.json ->> 'cost_usd')::numeric END)
	FROM onboarding o
	JOIN repos rp ON rp.id = o.repo_id
	WHERE rp.workspace_id = @ws AND o.generated_at >= @from AND o.generated_at < @to
) costs`

// referenceSpend runs the pre-ledger query for the workspace and the half-open range [from, to).
func (l *ledger) referenceSpend(ws string, from, to time.Time) domain.Micros {
	l.t.Helper()
	var micros int64
	err := l.db.Raw(referenceMonthSpendSQL,
		sql.Named("ws", ws), sql.Named("from", from.UTC()), sql.Named("to", to.UTC()),
	).Scan(&micros).Error
	if err != nil {
		l.t.Fatalf("reference month spend(%s, %v, %v): %v", ws, from, to, err)
	}
	return domain.Micros(micros)
}

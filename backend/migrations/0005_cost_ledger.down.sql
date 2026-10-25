-- Drops exactly what 0005_cost_ledger.up.sql created. The per-feature cost columns (agent_runs,
-- eval_runs, convention_scans, pr_intent, pr_brief, onboarding, ci_runs) are untouched, so the
-- next Up rebuilds the same backfilled entries from them (FR10, EC18). Entries written live since
-- the upgrade are removed: this is the one place the ledger loses entries.
DROP TABLE public.cost_ledger_backfill;
DROP TABLE public.spend_month_totals;
DROP TABLE public.cost_ledger;

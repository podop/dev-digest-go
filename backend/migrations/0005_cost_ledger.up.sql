-- Cost ledger (spec 2026-10-25-cost-ledger): an append-only record of what every answered LLM
-- attempt and every ingested CI run cost, the stored per-month totals that the spend read and the
-- cap check use, and a one-row summary of the backfill below.

-- One entry per answered attempt (FR2). No foreign key to the unit of work: an entry outlives
-- the run, PR, repo, scan or eval case it paid for (ref_id is informational). The workspace key
-- has no cascade either: a ledger is never deleted with anything (FR4).
--   provider, model  '' when unknown (CI runs without a model, backfilled sources without one).
--   cost_micros      micro-USD; NULL = unknown cost (counted as unpriced, never summed).
--   ref_id           NULL = no reference.
--   dedupe_key       unique: random for live entries, 'ci:<github run id>:<attempt>' for CI
--                    runs, 'backfill:<source>:<id>' for backfilled ones (appending a key that
--                    exists is a no-op).
CREATE TABLE public.cost_ledger (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    workspace_id uuid NOT NULL,
    occurred_at timestamp with time zone NOT NULL,
    kind text NOT NULL,
    origin text NOT NULL,
    provider text DEFAULT ''::text NOT NULL,
    model text DEFAULT ''::text NOT NULL,
    tokens_in bigint,
    tokens_out bigint,
    cost_micros bigint,
    ref_id uuid,
    dedupe_key text NOT NULL,
    CONSTRAINT cost_ledger_pkey PRIMARY KEY (id),
    CONSTRAINT cost_ledger_dedupe_key_key UNIQUE (dedupe_key),
    CONSTRAINT cost_ledger_workspace_id_workspaces_id_fk FOREIGN KEY (workspace_id) REFERENCES public.workspaces(id),
    CONSTRAINT cost_ledger_kind_chk CHECK ((kind = ANY (ARRAY['review_run'::text, 'review_intent'::text, 'intent_refresh'::text, 'brief'::text, 'onboarding'::text, 'conventions_scan'::text, 'eval_case'::text, 'ci_run'::text]))),
    CONSTRAINT cost_ledger_origin_chk CHECK ((origin = ANY (ARRAY['live'::text, 'ci_ingest'::text, 'backfill'::text]))),
    CONSTRAINT cost_ledger_tokens_in_chk CHECK ((tokens_in >= 0)),
    CONSTRAINT cost_ledger_tokens_out_chk CHECK ((tokens_out >= 0)),
    CONSTRAINT cost_ledger_cost_micros_chk CHECK ((cost_micros >= 0))
);
-- Also the index of the workspace foreign key.
CREATE INDEX cost_ledger_ws_occurred_idx ON public.cost_ledger USING btree (workspace_id, occurred_at);

-- The stored totals of a workspace and UTC calendar month (FR9), striped over 8 slot rows so that
-- concurrent appends of one workspace rarely update the same row (NFR3). The month spend is the
-- sum of the slots' spent_micros, the unknown-cost count the sum of unpriced_count.
-- month = date_trunc('month', occurred_at AT TIME ZONE 'UTC')::date.
CREATE TABLE public.spend_month_totals (
    workspace_id uuid NOT NULL,
    month date NOT NULL,
    slot smallint NOT NULL,
    spent_micros bigint DEFAULT 0 NOT NULL,
    unpriced_count bigint DEFAULT 0 NOT NULL,
    entry_count bigint DEFAULT 0 NOT NULL,
    CONSTRAINT spend_month_totals_pkey PRIMARY KEY (workspace_id, month, slot),
    CONSTRAINT spend_month_totals_workspace_id_workspaces_id_fk FOREIGN KEY (workspace_id) REFERENCES public.workspaces(id),
    CONSTRAINT spend_month_totals_slot_chk CHECK (((slot >= 0) AND (slot <= 7))),
    CONSTRAINT spend_month_totals_month_chk CHECK ((month = date_trunc('month', month::timestamp)::date)),
    CONSTRAINT spend_month_totals_spent_chk CHECK ((spent_micros >= 0)),
    CONSTRAINT spend_month_totals_unpriced_chk CHECK ((unpriced_count >= 0)),
    CONSTRAINT spend_month_totals_entries_chk CHECK ((entry_count >= 0))
);

-- The backfill below leaves one summary row (FR15): entries created per kind, records skipped as
-- malformed, duration. The server logs it once after migrating and sets logged_at.
CREATE TABLE public.cost_ledger_backfill (
    id smallint NOT NULL,
    created jsonb NOT NULL,
    skipped bigint NOT NULL,
    duration_ms bigint NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    logged_at timestamp with time zone,
    CONSTRAINT cost_ledger_backfill_pkey PRIMARY KEY (id),
    CONSTRAINT cost_ledger_backfill_single_chk CHECK ((id = 1))
);

-- Backfill (FR10): one entry, origin 'backfill', for each record that the pre-ledger month-spend
-- query counted (spend/adapters/postgres monthSpendSQL: agent_runs, eval_runs, convention_scans,
-- pr_intent, pr_brief, onboarding; the mirrors ci_runs and eval_suite_runs are left out) and that
-- has a known cost, at that query's time and in the record's workspace, for every month (D11).
-- The seed runs after the migrations, so a fresh database backfills nothing and the seed's demo
-- costs get no entry (D18).
--   * Cost: round half away from zero to 6 decimals (D15); a NULL cost gets no entry (EC17). A
--     cost that is NaN, negative or >= 1e12 (PostgreSQL sorts NaN and Infinity above every number),
--     or a record whose time is NULL or not finite, is malformed: skipped and counted (A6).
--   * pr_brief and onboarding keep their cost in a jsonb document that is not schema-checked, and
--     the brief's time is a string in it. Every cast sits in a CASE behind jsonb_typeof /
--     pg_input_is_valid (a bad value would fail the whole migration). The brief document is
--     detoasted once ("|| '{}'" + OFFSET 0 keep the planner from reading the stored value again in
--     each operator).
--   * An agent run of source 'ci' is a CI run: kind ci_run and the dedupe key
--     'ci:<github run id>:<attempt>' of its ci_runs row (via ci_runs.agent_run_id), so a later
--     re-ingest of that run appends nothing (A11, D16). Without such a row its key is the
--     source-derived one like every other record.
--   * ref_id: agent run, eval case, scan, PR (intent and brief), repo (onboarding) (D16).
--   * review_intent is used for every intent: the source does not say whether it was a refresh.
WITH src AS MATERIALIZED (
    SELECT a.workspace_id AS ws, a.ran_at AS ts,
           CASE WHEN a.source = 'ci' THEN 'ci_run' ELSE 'review_run' END AS kind,
           COALESCE(a.provider, '') AS provider, COALESCE(a.model, '') AS model,
           CASE WHEN a.tokens_in >= 0 THEN a.tokens_in END::bigint AS tin,
           CASE WHEN a.tokens_out >= 0 THEN a.tokens_out END::bigint AS tout,
           a.cost_usd::numeric AS cost, a.id AS ref,
           CASE WHEN l.gh IS NOT NULL THEN 'ci:' || l.gh || ':' || l.att
                ELSE 'backfill:agent_run:' || a.id END AS dkey
    FROM agent_runs a
    LEFT JOIN LATERAL (
        SELECT c.github_run_id AS gh, c.run_attempt AS att
        FROM ci_runs c
        WHERE c.agent_run_id = a.id AND c.github_run_id IS NOT NULL AND c.run_attempt IS NOT NULL
        ORDER BY c.github_run_id, c.run_attempt
        LIMIT 1
    ) l ON a.source = 'ci'
    WHERE a.cost_usd IS NOT NULL
  UNION ALL
    SELECT k.workspace_id, r.ran_at, 'eval_case', '', '', NULL::bigint, NULL::bigint,
           r.cost_usd::numeric, k.id, 'backfill:eval_run:' || r.id
    FROM eval_runs r
    JOIN eval_cases k ON k.id = r.case_id
    WHERE r.cost_usd IS NOT NULL
  UNION ALL
    SELECT s.workspace_id, s.started_at, 'conventions_scan', '', COALESCE(s.model, ''),
           CASE WHEN s.tokens_in >= 0 THEN s.tokens_in END::bigint,
           CASE WHEN s.tokens_out >= 0 THEN s.tokens_out END::bigint,
           s.cost_usd::numeric, s.id, 'backfill:convention_scan:' || s.id
    FROM convention_scans s
    WHERE s.cost_usd IS NOT NULL
  UNION ALL
    SELECT p.workspace_id, i.derived_at, 'review_intent', COALESCE(i.provider, ''), COALESCE(i.model, ''),
           CASE WHEN i.tokens_in >= 0 THEN i.tokens_in END::bigint,
           CASE WHEN i.tokens_out >= 0 THEN i.tokens_out END::bigint,
           i.cost_usd::numeric, i.pr_id, 'backfill:pr_intent:' || i.pr_id
    FROM pr_intent i
    JOIN pull_requests p ON p.id = i.pr_id
    WHERE i.cost_usd IS NOT NULL
  UNION ALL
    SELECT p.workspace_id,
           CASE WHEN jsonb_typeof(d.doc -> 'generated_at') = 'string'
                 AND pg_input_is_valid(d.doc ->> 'generated_at', 'timestamptz')
                THEN (d.doc ->> 'generated_at')::timestamptz END,
           'brief', '', '', NULL::bigint, NULL::bigint,
           CASE WHEN jsonb_typeof(d.doc -> 'cost_usd') = 'number' THEN (d.doc ->> 'cost_usd')::numeric END,
           b.pr_id, 'backfill:pr_brief:' || b.pr_id
    FROM pr_brief b
    JOIN pull_requests p ON p.id = b.pr_id
    CROSS JOIN LATERAL (SELECT b.json || '{}'::jsonb AS doc OFFSET 0) d
    WHERE jsonb_typeof(d.doc -> 'cost_usd') IS NOT NULL AND jsonb_typeof(d.doc -> 'cost_usd') <> 'null'
  UNION ALL
    SELECT rp.workspace_id, o.generated_at, 'onboarding', '', '', NULL::bigint, NULL::bigint,
           CASE WHEN jsonb_typeof(o.json -> 'cost_usd') = 'number' THEN (o.json ->> 'cost_usd')::numeric END,
           o.repo_id, 'backfill:onboarding:' || o.repo_id
    FROM onboarding o
    JOIN repos rp ON rp.id = o.repo_id
    WHERE jsonb_typeof(o.json -> 'cost_usd') IS NOT NULL AND jsonb_typeof(o.json -> 'cost_usd') <> 'null'
), checked AS (
    SELECT *, (cost IS NOT NULL AND cost >= 0 AND cost < 1000000000000
               AND ts IS NOT NULL AND isfinite(ts)) AS usable
    FROM src
), ins AS (
    INSERT INTO public.cost_ledger
        (workspace_id, occurred_at, kind, origin, provider, model, tokens_in, tokens_out,
         cost_micros, ref_id, dedupe_key)
    SELECT ws, ts, kind, 'backfill', provider, model, tin, tout,
           (round(cost, 6) * 1000000)::bigint, ref, dkey
    FROM checked
    WHERE usable
    ON CONFLICT (dedupe_key) DO NOTHING
    RETURNING kind
)
INSERT INTO public.cost_ledger_backfill (id, created, skipped, duration_ms)
SELECT 1,
       (SELECT jsonb_object_agg(k.kind, COALESCE(c.n, 0))
          FROM (VALUES ('review_run'), ('review_intent'), ('intent_refresh'), ('brief'),
                       ('onboarding'), ('conventions_scan'), ('eval_case'), ('ci_run')) k(kind)
          LEFT JOIN (SELECT kind, count(*) AS n FROM ins GROUP BY kind) c ON c.kind = k.kind),
       (SELECT count(*) FROM checked WHERE NOT usable),
       0;

-- The stored totals, built from the backfilled entries into slot 0 (FR10).
INSERT INTO public.spend_month_totals (workspace_id, month, slot, spent_micros, unpriced_count, entry_count)
SELECT workspace_id,
       date_trunc('month', occurred_at AT TIME ZONE 'UTC')::date,
       0,
       LEAST(COALESCE(SUM(cost_micros), 0), 9223372036854775807)::bigint,
       count(*) FILTER (WHERE cost_micros IS NULL),
       count(*)
FROM public.cost_ledger
GROUP BY workspace_id, date_trunc('month', occurred_at AT TIME ZONE 'UTC')::date;

-- Time the backfill took, from the start of this migration's transaction.
UPDATE public.cost_ledger_backfill
SET duration_ms = (extract(epoch FROM clock_timestamp() - transaction_timestamp()) * 1000)::bigint
WHERE id = 1;

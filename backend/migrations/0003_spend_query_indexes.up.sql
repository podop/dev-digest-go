-- Covering indexes for the month-spend query (spend context, FR12/NFR4). Both let the planner
-- answer the range scan from the index alone, without reading the table rows: at 100,000
-- agent_runs and 50,000 eval_runs in the month the sequential scans were the second and third
-- largest cost of the query.
CREATE INDEX agent_runs_ws_ran_idx ON public.agent_runs USING btree (workspace_id, ran_at) INCLUDE (cost_usd);
CREATE INDEX eval_runs_ran_idx ON public.eval_runs USING btree (ran_at) INCLUDE (case_id, cost_usd);

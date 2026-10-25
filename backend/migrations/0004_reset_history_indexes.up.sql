-- Index for the foreign key ci_runs.agent_run_id -> agent_runs(id) ON DELETE SET NULL (Settings B,
-- decision D27). Without it every agent run a reset deletes scans all of ci_runs for the rows to
-- null: at 100,000 deleted runs and 5,005 ci_runs that was 15.8 s of the 43.5 s reset
-- (NFR3: 30 s). The reviews and run_traces lookups already have indexes (reviews_run_idx,
-- the run_traces primary key).
CREATE INDEX ci_runs_agent_run_idx ON public.ci_runs USING btree (agent_run_id);

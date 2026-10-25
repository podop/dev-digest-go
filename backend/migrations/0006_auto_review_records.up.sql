-- Automatic reviews (Settings C): the stored decision for one (PR, head SHA) and the run source
-- 'auto'.

-- One record per (PR, head SHA), written once by the trigger after a background-poll cycle and
-- never re-decided (FR8): the unique key is the at-most-once guard (the trigger claims the head
-- with INSERT ... ON CONFLICT DO NOTHING before it starts a review) and survives a restart (EC7).
--   event         'new_pr' = the PR had no record for any head SHA, 'new_commits' = it had one.
--   outcome       the decision; 'started' also covers a run that later fails (EC17); 'failed' = the
--                 review start returned an error (not retried).
--   runs_started  runs created for the head (1 per agent, or 1 multi-agent run's agent runs);
--                 0 for every outcome but 'started'.
--   decided_at    the trigger's clock at the decision (the rate limit counts 'started' records
--                 per time window from it).
-- No reference to agent_runs: the records are not run history, and Settings B's "Reset run history"
-- deletes agent runs of every source, 'auto' included, while the records stay (retention pruning
-- keeps them too). The records go with their PR, and so with its repository, and with their
-- workspace.
CREATE TABLE public.auto_review_records (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    workspace_id uuid NOT NULL,
    pr_id uuid NOT NULL,
    head_sha text NOT NULL,
    event text NOT NULL,
    outcome text NOT NULL,
    runs_started integer DEFAULT 0 NOT NULL,
    decided_at timestamp with time zone NOT NULL,
    CONSTRAINT auto_review_records_pkey PRIMARY KEY (id),
    CONSTRAINT auto_review_records_pr_head_key UNIQUE (pr_id, head_sha),
    CONSTRAINT auto_review_records_workspace_id_workspaces_id_fk FOREIGN KEY (workspace_id) REFERENCES public.workspaces(id) ON DELETE CASCADE,
    CONSTRAINT auto_review_records_pr_id_pull_requests_id_fk FOREIGN KEY (pr_id) REFERENCES public.pull_requests(id) ON DELETE CASCADE,
    CONSTRAINT auto_review_records_event_chk CHECK ((event = ANY (ARRAY['new_pr'::text, 'new_commits'::text]))),
    CONSTRAINT auto_review_records_outcome_chk CHECK ((outcome = ANY (ARRAY['started'::text, 'skipped_trigger_off'::text, 'skipped_already_reviewed'::text, 'skipped_no_agents'::text, 'skipped_spend_cap'::text, 'failed'::text]))),
    CONSTRAINT auto_review_records_runs_started_chk CHECK ((runs_started >= 0))
);
-- The rate-limit count (started records in the last hour) and the status read. Also the index
-- of the workspace foreign key; the foreign key to the PR is indexed by the unique key above.
CREATE INDEX auto_review_records_ws_decided_idx ON public.auto_review_records USING btree (workspace_id, decided_at);

-- An automatic review's runs get the source 'auto'.
ALTER TABLE public.agent_runs DROP CONSTRAINT agent_runs_source_chk;
ALTER TABLE public.agent_runs ADD CONSTRAINT agent_runs_source_chk CHECK ((source = ANY (ARRAY['local'::text, 'ci'::text, 'auto'::text])));

-- Automatic reviews, fork guard (Settings C decision D13, security review M1): the trigger starts
-- reviews only for PRs whose head branch lives in the PR's own repository, so a stranger's fork PR
-- on a polled public repository cannot spend the owner's LLM budget.

-- Whether the PR's head repository is another repository than the PR's base repository, as the
-- GitHub poll last saw it. A deleted fork (GitHub sends no head repository) counts as a fork.
--   NULL   unknown: the row predates this column, or it was not written by the GitHub poll. The
--          trigger treats unknown as a fork until the next poll refreshes the row.
-- Not part of any API response.
ALTER TABLE public.pull_requests ADD COLUMN head_is_fork boolean;

-- The trigger records a fork PR's head as 'skipped_fork' (never started, never decided again).
ALTER TABLE public.auto_review_records DROP CONSTRAINT auto_review_records_outcome_chk;
ALTER TABLE public.auto_review_records ADD CONSTRAINT auto_review_records_outcome_chk CHECK ((outcome = ANY (ARRAY['started'::text, 'skipped_trigger_off'::text, 'skipped_already_reviewed'::text, 'skipped_no_agents'::text, 'skipped_spend_cap'::text, 'skipped_fork'::text, 'failed'::text])));

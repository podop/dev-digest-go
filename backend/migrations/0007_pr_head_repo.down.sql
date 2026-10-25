-- Drops exactly what 0007_pr_head_repo.up.sql added and restores the old outcome rule.
-- LOSSY: the old rule has no 'skipped_fork', so a stored 'skipped_fork' record becomes
-- 'skipped_trigger_off' (an event and a head that were decided without a review start). The
-- records are kept, not deleted, on purpose: a deleted record would make the head a candidate
-- again and a later Up would then review fork PRs it had refused. A later Up cannot tell the
-- converted records apart again. The head_is_fork column and its values are dropped.
UPDATE public.auto_review_records SET outcome = 'skipped_trigger_off' WHERE outcome = 'skipped_fork';
ALTER TABLE public.auto_review_records DROP CONSTRAINT auto_review_records_outcome_chk;
ALTER TABLE public.auto_review_records ADD CONSTRAINT auto_review_records_outcome_chk CHECK ((outcome = ANY (ARRAY['started'::text, 'skipped_trigger_off'::text, 'skipped_already_reviewed'::text, 'skipped_no_agents'::text, 'skipped_spend_cap'::text, 'failed'::text])));
ALTER TABLE public.pull_requests DROP COLUMN head_is_fork;

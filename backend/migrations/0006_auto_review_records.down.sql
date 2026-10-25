-- Drops exactly what 0006_auto_review_records.up.sql created and restores the old run-source rule.
-- LOSSY: the old rule has no 'auto', so every automatic review's run becomes a 'local' one (its
-- other columns stay); a later Up cannot tell them apart again. The auto-review records are
-- dropped with their table: after the next Up every PR is a new candidate (the poller would
-- evaluate it as a "new PR"), and runs back to 'local' stay what they are.
UPDATE public.agent_runs SET source = 'local' WHERE source = 'auto';
ALTER TABLE public.agent_runs DROP CONSTRAINT agent_runs_source_chk;
ALTER TABLE public.agent_runs ADD CONSTRAINT agent_runs_source_chk CHECK ((source = ANY (ARRAY['local'::text, 'ci'::text])));
DROP TABLE public.auto_review_records;

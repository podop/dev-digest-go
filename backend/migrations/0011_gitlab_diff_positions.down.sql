-- Reverts 0011_gitlab_diff_positions.up.sql: drops the diff head (with its CHECK) and the previous
-- path. LOSSY only for those two optional values; they are refilled by the next GitLab detail
-- fetch, and until then a GitLab post answers diff_changed (spec AC30). No other data is touched.
ALTER TABLE public.pr_files DROP COLUMN previous_path;
ALTER TABLE public.pull_requests DROP CONSTRAINT pull_requests_diff_head_chk;
ALTER TABLE public.pull_requests DROP COLUMN diff_head;

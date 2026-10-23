-- PR merge state for the leaderboard: when a PR was merged or closed, and whether it is a draft.
-- Existing rows get NULL timestamps and draft = false until the next sync fills them in.
ALTER TABLE public.pull_requests
    ADD COLUMN merged_at timestamp with time zone,
    ADD COLUMN closed_at timestamp with time zone,
    ADD COLUMN draft boolean DEFAULT false NOT NULL;

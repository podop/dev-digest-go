ALTER TABLE public.pull_requests
    DROP COLUMN draft,
    DROP COLUMN closed_at,
    DROP COLUMN merged_at;

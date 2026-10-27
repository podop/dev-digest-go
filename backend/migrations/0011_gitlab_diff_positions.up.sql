-- GitLab B (spec gitlab-b-comments-context FR34, FR35; context pack D1).
-- A GitLab diff note needs the head SHA of the diff version the stored files came from and, for a
-- renamed file, its old path. Neither is on the wire; both are filled by the GitLab detail fetch
-- (in the transaction that replaces the files) and stay NULL for GitHub rows and for rows that no
-- detail fetch has refreshed since this migration.

-- The diff head: the MR's diff_refs.head_sha from the detail response, else its sha. A full object
-- id: 40 lower-case hex characters (SHA-1) or 64 (SHA-256). NULL = unknown; readers then fall back
-- to the stored head_sha and a GitLab post answers diff_changed until a detail fetch refills it.
ALTER TABLE public.pull_requests ADD COLUMN diff_head text;
ALTER TABLE public.pull_requests
    ADD CONSTRAINT pull_requests_diff_head_chk CHECK (diff_head IS NULL OR diff_head ~ '^[0-9a-f]{40}([0-9a-f]{24})?$');

-- The file's path before the change (GitLab old_path), for a renamed file. NULL for GitHub files,
-- for files that were not renamed and for rows not yet refreshed.
ALTER TABLE public.pr_files ADD COLUMN previous_path text;

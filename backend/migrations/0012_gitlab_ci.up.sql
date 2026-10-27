-- GitLab C (spec gitlab-c-ci FR39, EC30; plan P1, P3, A5).
-- A CI installation can target a GitLab project (`gitlab_ci`), and a CI run can come from a GitLab
-- instance. Both rows get a host, so the identities that used to be globally unique become unique
-- per host:
--   * ci_installations: `host` and `project_id` (the GitLab project id, the identity of a GitLab
--     repository; `repo` stays the display path). The unique (agent, repo, target) and (repo, slug)
--     become two partial uniques each: one for the repo-identified targets (gha, circle, ...: host +
--     repo) and one for `gitlab_ci` (host + project id), so a GitHub `acme/web` and a GitLab
--     `acme/web` coexist and a renamed GitLab project keeps its installation.
--   * ci_runs: `host`; (github_run_id, run_attempt) becomes (host, github_run_id, run_attempt), so a
--     GitLab job id equal to a GitHub run id (or to a job id of another instance) is stored.
-- Existing rows are GitHub rows: they get host `github.com` and no project id. The DEFAULT stays on
-- both host columns so every GitHub writer keeps working unchanged; the CHECKs below refuse a
-- `gitlab_ci` installation on github.com, and the GitLab store always names the host. ON CONFLICT
-- on a partial unique index must repeat its predicate (WHERE target_type <> 'gitlab_ci' or
-- WHERE target_type = 'gitlab_ci').

ALTER TABLE public.ci_installations ADD COLUMN host text NOT NULL DEFAULT 'github.com';
ALTER TABLE public.ci_installations ADD COLUMN project_id bigint;

ALTER TABLE public.ci_installations DROP CONSTRAINT ci_installations_target_type_chk;
ALTER TABLE public.ci_installations
    ADD CONSTRAINT ci_installations_target_type_chk CHECK ((target_type = ANY (ARRAY['gha'::text, 'circle'::text, 'jenkins'::text, 'cli'::text, 'gitlab_ci'::text]))),
    ADD CONSTRAINT ci_installations_host_chk CHECK (host <> '' AND host = lower(host) AND host !~ '[[:space:]]'),
    ADD CONSTRAINT ci_installations_project_id_chk CHECK (project_id IS NULL OR project_id > 0),
    -- A GitLab CI installation names its project and lives on a GitLab host; every other target is a
    -- GitHub-style installation on github.com without a project id.
    ADD CONSTRAINT ci_installations_target_cols_chk CHECK (
        (target_type = 'gitlab_ci' AND project_id IS NOT NULL
            AND host NOT IN ('github.com', 'www.github.com', 'api.github.com'))
        OR (target_type <> 'gitlab_ci' AND project_id IS NULL AND host = 'github.com')
    );

ALTER TABLE public.ci_installations DROP CONSTRAINT ci_installations_agent_repo_target_uq;
ALTER TABLE public.ci_installations DROP CONSTRAINT ci_installations_repo_slug_uq;

CREATE UNIQUE INDEX ci_installations_agent_host_repo_target_uq
    ON public.ci_installations USING btree (agent_id, host, repo, target_type) WHERE target_type <> 'gitlab_ci';
CREATE UNIQUE INDEX ci_installations_agent_host_project_target_uq
    ON public.ci_installations USING btree (agent_id, host, project_id, target_type) WHERE target_type = 'gitlab_ci';
CREATE UNIQUE INDEX ci_installations_host_repo_slug_uq
    ON public.ci_installations USING btree (host, repo, slug) WHERE target_type <> 'gitlab_ci';
CREATE UNIQUE INDEX ci_installations_host_project_slug_uq
    ON public.ci_installations USING btree (host, project_id, slug) WHERE target_type = 'gitlab_ci';

ALTER TABLE public.ci_runs ADD COLUMN host text NOT NULL DEFAULT 'github.com';
ALTER TABLE public.ci_runs
    ADD CONSTRAINT ci_runs_host_chk CHECK (host <> '' AND host = lower(host) AND host !~ '[[:space:]]');
ALTER TABLE public.ci_runs DROP CONSTRAINT ci_runs_github_run_attempt_uq;
ALTER TABLE public.ci_runs
    ADD CONSTRAINT ci_runs_host_run_attempt_uq UNIQUE (host, github_run_id, run_attempt);

-- Reverts 0012_gitlab_ci.up.sql.
-- REFUSES (plan A12, spec EC33) while any `gitlab_ci` installation or GitLab CI run (a run whose host
-- is not github.com) exists: the previous schema cannot hold them (no host, one run id space), and
-- dropping them would delete the user's CI history, which is not this migration's call. The error
-- names the GitLab CI data (counts and up to 20 installations); delete the GitLab CI installations
-- and runs, then run Down again. The migration runs in one transaction, so a refusal changes nothing.
-- Otherwise it restores the previous schema exactly: with only GitHub rows (host github.com, no
-- project id) the restored uniques (agent, repo, target), (repo, slug) and (run id, attempt) hold,
-- because the host is constant.
DO $$
DECLARE
    ninst bigint;
    nruns bigint;
    names text;
BEGIN
    SELECT count(*) INTO ninst FROM public.ci_installations WHERE target_type = 'gitlab_ci';
    SELECT count(*) INTO nruns FROM public.ci_runs WHERE host <> 'github.com';
    IF ninst > 0 OR nruns > 0 THEN
        SELECT string_agg(host || ' ' || repo, ', ' ORDER BY host, repo) INTO names
            FROM (SELECT host, repo FROM public.ci_installations WHERE target_type = 'gitlab_ci' ORDER BY host, repo LIMIT 20) i;
        RAISE EXCEPTION 'cannot revert 0012_gitlab_ci: GitLab CI data exists: % gitlab_ci installation(s), % GitLab CI run(s) (up to 20 installations listed: %); delete the GitLab CI installations and runs, then retry', ninst, nruns, coalesce(names, 'none')
            USING ERRCODE = 'restrict_violation';
    END IF;
END
$$;

ALTER TABLE public.ci_runs DROP CONSTRAINT ci_runs_host_run_attempt_uq;
ALTER TABLE public.ci_runs
    ADD CONSTRAINT ci_runs_github_run_attempt_uq UNIQUE (github_run_id, run_attempt);
ALTER TABLE public.ci_runs DROP CONSTRAINT ci_runs_host_chk;
ALTER TABLE public.ci_runs DROP COLUMN host;

DROP INDEX public.ci_installations_host_project_slug_uq;
DROP INDEX public.ci_installations_host_repo_slug_uq;
DROP INDEX public.ci_installations_agent_host_project_target_uq;
DROP INDEX public.ci_installations_agent_host_repo_target_uq;

ALTER TABLE public.ci_installations
    ADD CONSTRAINT ci_installations_agent_repo_target_uq UNIQUE (agent_id, repo, target_type),
    ADD CONSTRAINT ci_installations_repo_slug_uq UNIQUE (repo, slug);

ALTER TABLE public.ci_installations
    DROP CONSTRAINT ci_installations_target_cols_chk,
    DROP CONSTRAINT ci_installations_project_id_chk,
    DROP CONSTRAINT ci_installations_host_chk,
    DROP CONSTRAINT ci_installations_target_type_chk;
ALTER TABLE public.ci_installations
    ADD CONSTRAINT ci_installations_target_type_chk CHECK ((target_type = ANY (ARRAY['gha'::text, 'circle'::text, 'jenkins'::text, 'cli'::text])));
ALTER TABLE public.ci_installations DROP COLUMN project_id;
ALTER TABLE public.ci_installations DROP COLUMN host;

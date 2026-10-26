-- Reverts 0010_gitlab.up.sql.
-- REFUSES (plan A11, spec EC29) while any GitLab connection or GitLab repository exists: dropping
-- them would delete the user's repositories with their PRs, reviews and findings, which is not this
-- migration's call. The error names the GitLab data (counts and up to 20 repositories); delete the
-- GitLab repositories and connections (their tokens in the secrets store stay until deleted with the
-- connection), then run Down again. The migration runs in one transaction, so a refusal changes
-- nothing.
-- Otherwise it reverses everything: with only GitHub rows (provider github, host github.com) the
-- restored uniqueness (workspace, full_name) holds, because the host is constant. LOSSY only in the
-- schema sense: the provider/host/project_id/web_url columns and the connection table are dropped.
DO $$
DECLARE
    nconn bigint;
    nrepo bigint;
    names text;
BEGIN
    SELECT count(*) INTO nconn FROM public.gitlab_connections;
    SELECT count(*) INTO nrepo FROM public.repos WHERE provider = 'gitlab';
    IF nconn > 0 OR nrepo > 0 THEN
        SELECT string_agg(host || ' ' || full_name, ', ' ORDER BY host, full_name) INTO names
            FROM (SELECT host, full_name FROM public.repos WHERE provider = 'gitlab' ORDER BY host, full_name LIMIT 20) r;
        RAISE EXCEPTION 'cannot revert 0010_gitlab: GitLab data exists: % connection(s), % GitLab repositor(y/ies) (up to 20 listed: %); delete the GitLab repositories and connections, then retry', nconn, nrepo, coalesce(names, 'none')
            USING ERRCODE = 'restrict_violation';
    END IF;
END
$$;

DROP INDEX public.repos_ws_host_project_uq;
DROP INDEX public.repos_ws_host_fullname_uq;
CREATE UNIQUE INDEX repos_ws_fullname_uq ON public.repos USING btree (workspace_id, full_name);

ALTER TABLE public.repos
    DROP CONSTRAINT repos_web_url_chk,
    DROP CONSTRAINT repos_project_id_chk,
    DROP CONSTRAINT repos_provider_cols_chk,
    DROP CONSTRAINT repos_provider_host_chk,
    DROP CONSTRAINT repos_provider_chk;
ALTER TABLE public.repos DROP COLUMN web_url;
ALTER TABLE public.repos DROP COLUMN project_id;
ALTER TABLE public.repos DROP COLUMN host;
ALTER TABLE public.repos DROP COLUMN provider;

DROP TABLE public.gitlab_connections;

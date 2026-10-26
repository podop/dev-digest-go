-- GitLab A (spec gitlab-a-connection-sync FR30, D26; plan decision P8).
-- Adds the GitLab connection entity (installation-wide, no workspace) and gives repositories a
-- provider, a host, and for GitLab a project id and a stored web URL. The repository uniqueness
-- (workspace, full_name) becomes (workspace, host, full_name), plus (workspace, host, project_id)
-- for GitLab. The token of a connection is NOT stored here: it lives in the secrets store (FR4).

-- A GitLab connection. Global: it belongs to no workspace and every workspace uses it (FR1). `host`
-- is the host key (lower-cased host name, `:<port>` when the port is not 443, FR2); at most one
-- connection per host key (FR3, D4). `base_url` is stored normalised (https, no trailing slash).
-- The limit of 20 connections, the name trimming and the address rules (link-local, unspecified,
-- the /api/v4 suffix, github.com hosts) stay in the application. A repository is bound to the
-- connection whose host key equals its `host`, resolved at every read: there is deliberately no
-- foreign key from repos to this table (D26, P4), so deleting a connection keeps the repositories.
CREATE TABLE public.gitlab_connections (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    name text NOT NULL,
    base_url text NOT NULL,
    host text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT gitlab_connections_pkey PRIMARY KEY (id),
    CONSTRAINT gitlab_connections_host_uq UNIQUE (host),
    -- 1-80 characters after trimming, no control characters (FR1).
    CONSTRAINT gitlab_connections_name_chk CHECK (char_length(btrim(name)) BETWEEN 1 AND 80 AND name !~ '[[:cntrl:]]'),
    -- https only, a host without userinfo, no query or fragment, no trailing slash, at most 2,048
    -- characters (FR2).
    CONSTRAINT gitlab_connections_base_url_chk CHECK (
        char_length(base_url) <= 2048
        AND base_url ~ '^https://[^/?#@[:space:]]+(/[^?#[:space:]]*)?$'
        AND base_url !~ '/$'
    ),
    CONSTRAINT gitlab_connections_host_chk CHECK (host <> '' AND host = lower(host) AND host !~ '[[:space:]]')
);

-- Repositories. The defaults exist for the rows that exist now (GitHub on github.com) and for every
-- insert that does not name the new columns; they stay.
ALTER TABLE public.repos ADD COLUMN provider text NOT NULL DEFAULT 'github';
ALTER TABLE public.repos ADD COLUMN host text NOT NULL DEFAULT 'github.com';
-- The GitLab project id (the identity of a GitLab repository, FR47) and the stored web URL (a cache
-- of the project's current path). Both NULL for GitHub (the GitHub web URL is derived, A10).
ALTER TABLE public.repos ADD COLUMN project_id bigint;
ALTER TABLE public.repos ADD COLUMN web_url text;

ALTER TABLE public.repos
    ADD CONSTRAINT repos_provider_chk CHECK (provider IN ('github', 'gitlab')),
    -- GitHub repositories live on github.com; a GitLab repository never carries a GitHub host.
    ADD CONSTRAINT repos_provider_host_chk CHECK (
        (provider = 'github' AND host = 'github.com')
        OR (provider = 'gitlab' AND host <> '' AND host = lower(host) AND host !~ '[[:space:]]'
            AND host NOT IN ('github.com', 'www.github.com', 'api.github.com'))
    ),
    -- Project id and web URL: both set for GitLab, both NULL for GitHub (FR30).
    ADD CONSTRAINT repos_provider_cols_chk CHECK (
        (provider = 'github' AND project_id IS NULL AND web_url IS NULL)
        OR (provider = 'gitlab' AND project_id IS NOT NULL AND web_url IS NOT NULL)
    ),
    ADD CONSTRAINT repos_project_id_chk CHECK (project_id IS NULL OR project_id > 0),
    ADD CONSTRAINT repos_web_url_chk CHECK (
        web_url IS NULL
        OR (char_length(web_url) <= 2048 AND web_url ~ '^https://[^/?#@[:space:]]+(/[^?#[:space:]]*)?$')
    );

-- Uniqueness. (workspace, host, full_name) replaces (workspace, full_name): a GitHub `acme/web` and
-- a GitLab `acme/web` coexist (AC12). The repos upsert conflicts on exactly these columns.
DROP INDEX public.repos_ws_fullname_uq;
CREATE UNIQUE INDEX repos_ws_host_fullname_uq ON public.repos USING btree (workspace_id, host, full_name);
-- A GitLab project is added once per workspace and host, however its path changes (FR47). Partial:
-- an ON CONFLICT on it must repeat `WHERE provider = 'gitlab'`.
CREATE UNIQUE INDEX repos_ws_host_project_uq ON public.repos USING btree (workspace_id, host, project_id) WHERE provider = 'gitlab';

-- Custom model providers (spec custom-model-providers FR1, FR7, FR37, D28; plan decision P9).
-- Adds two tables (a workspace's custom providers and their manual models), lets an agent hold a
-- custom provider id, and stores the provider fixed at export time with a CI installation.
-- The key and the extra-header values are NOT stored here: they live in the secrets store (FR5).

-- A custom provider. `id` is the slug part of the public id `custom:<slug>` (without the prefix);
-- immutable after creation (FR1). Its scope is the workspace, so the primary key is (workspace_id, id).
-- A concurrent create of the same id fails with 23505 on custom_providers_pkey (the create maps it
-- to 409 custom_provider_exists). The limit of 20 per workspace is enforced by the application.
CREATE TABLE public.custom_providers (
    workspace_id uuid NOT NULL,
    id text NOT NULL,
    name text NOT NULL,
    protocol text NOT NULL,
    base_url text NOT NULL,
    auth_mode text NOT NULL,
    auth_header text,
    extra_header_names text[] DEFAULT '{}'::text[] NOT NULL,
    structured_output text DEFAULT 'strict' NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT custom_providers_pkey PRIMARY KEY (workspace_id, id),
    CONSTRAINT custom_providers_workspace_id_workspaces_id_fk FOREIGN KEY (workspace_id) REFERENCES public.workspaces(id) ON DELETE CASCADE,
    CONSTRAINT custom_providers_id_chk CHECK (id ~ '^[a-z0-9-]{1,32}$'),
    -- 1-80 characters after trimming, no control characters.
    CONSTRAINT custom_providers_name_chk CHECK (char_length(btrim(name)) BETWEEN 1 AND 80 AND name !~ '[[:cntrl:]]'),
    CONSTRAINT custom_providers_protocol_chk CHECK (protocol IN ('openai', 'anthropic')),
    -- http(s), a host without userinfo, no query or fragment, no trailing slash, at most 2,048
    -- characters (FR3). The address rules (link-local, unspecified) need DNS and stay in the application.
    CONSTRAINT custom_providers_base_url_chk CHECK (
        char_length(base_url) <= 2048
        AND base_url ~* '^https?://[^/?#@[:space:]]+(/[^?#[:space:]]*)?$'
        AND base_url !~ '/$'
    ),
    CONSTRAINT custom_providers_auth_mode_chk CHECK (auth_mode IN ('bearer', 'header')),
    -- bearer sends no header name; header mode needs one: an HTTP token of 1-64 characters (FR4).
    CONSTRAINT custom_providers_auth_header_chk CHECK (
        (auth_mode = 'bearer' AND auth_header IS NULL)
        OR (auth_mode = 'header' AND auth_header IS NOT NULL
            AND char_length(auth_header) BETWEEN 1 AND 64
            AND auth_header ~ '^[!#$%&''*+.^_`|~0-9A-Za-z-]+$')
    ),
    -- Names only, in order, at most 5 (FR4); the values are in the secrets store. Uniqueness and the
    -- forbidden names are checked by the application.
    CONSTRAINT custom_providers_extra_header_names_chk CHECK (
        cardinality(extra_header_names) <= 5 AND array_position(extra_header_names, NULL) IS NULL
    ),
    CONSTRAINT custom_providers_structured_output_chk CHECK (structured_output IN ('strict', 'json_object', 'prompt_only')),
    -- json_object is valid only with protocol openai (FR1, D16).
    CONSTRAINT custom_providers_json_object_protocol_chk CHECK (structured_output <> 'json_object' OR protocol = 'openai')
);

-- Manual models of a custom provider (FR1): at most 200 per provider (enforced by the application),
-- unique model id within the provider. `position` keeps the order the user gave. Deleted with the
-- provider. Prices are USD per 1M tokens: both set or both NULL, 0-10,000, at most 6 decimals.
CREATE TABLE public.custom_provider_models (
    workspace_id uuid NOT NULL,
    provider_id text NOT NULL,
    model_id text NOT NULL,
    label text,
    prompt_per_m numeric,
    completion_per_m numeric,
    "position" integer NOT NULL,
    CONSTRAINT custom_provider_models_pkey PRIMARY KEY (workspace_id, provider_id, model_id),
    CONSTRAINT custom_provider_models_provider_fk FOREIGN KEY (workspace_id, provider_id) REFERENCES public.custom_providers(workspace_id, id) ON DELETE CASCADE,
    CONSTRAINT custom_provider_models_model_id_chk CHECK (char_length(model_id) BETWEEN 1 AND 200),
    CONSTRAINT custom_provider_models_label_chk CHECK (label IS NULL OR char_length(label) <= 200),
    CONSTRAINT custom_provider_models_position_chk CHECK ("position" >= 0 AND "position" < 200),
    CONSTRAINT custom_provider_models_pricing_chk CHECK (
        (prompt_per_m IS NULL AND completion_per_m IS NULL)
        OR (prompt_per_m IS NOT NULL AND completion_per_m IS NOT NULL
            AND prompt_per_m BETWEEN 0 AND 10000 AND completion_per_m BETWEEN 0 AND 10000
            AND prompt_per_m = round(prompt_per_m, 6) AND completion_per_m = round(completion_per_m, 6))
    )
);

-- Foreign key columns are not indexed by PostgreSQL. custom_providers.workspace_id is the leading
-- column of the primary key (workspace_id, id), and (workspace_id, provider_id) of the models' FK
-- is the leading part of custom_provider_models_pkey, so both are served by the existing indexes.

-- An agent may hold `custom:<slug>`. The id is a string reference, not a foreign key (FR7): the
-- constraint only checks the shape.
ALTER TABLE public.agents DROP CONSTRAINT agents_provider_chk;
ALTER TABLE public.agents
    ADD CONSTRAINT agents_provider_chk CHECK (
        provider = ANY (ARRAY['openai'::text, 'anthropic'::text, 'openrouter'::text])
        OR provider ~ '^custom:[a-z0-9-]{1,32}$'
    );

-- The provider of a CI installation, fixed at export time (FR37, D28). Installations that exist now
-- were exported through OpenRouter, the only provider Phase 6 could export (EC35, AC48). The default
-- exists only to fill those rows and is dropped: every later insert (the open_pr upsert) must name
-- the provider. A built-in id or a `custom:<slug>` string, no foreign key.
ALTER TABLE public.ci_installations ADD COLUMN provider text NOT NULL DEFAULT 'openrouter';
ALTER TABLE public.ci_installations ALTER COLUMN provider DROP DEFAULT;
ALTER TABLE public.ci_installations
    ADD CONSTRAINT ci_installations_provider_chk CHECK (
        provider = ANY (ARRAY['openai'::text, 'anthropic'::text, 'openrouter'::text])
        OR provider ~ '^custom:[a-z0-9-]{1,32}$'
    );

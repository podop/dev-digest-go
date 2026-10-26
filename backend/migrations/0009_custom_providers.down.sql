-- Reverts 0009_custom_providers.up.sql.
-- REFUSES (plan A11) while any agent still uses a custom provider: the restored agents_provider_chk
-- accepts only openai, anthropic and openrouter, so those agents would be invalid, and rewriting or
-- deleting them is not this migration's call. The error names the agents; point them at a built-in
-- provider (or delete them), then run Down again. The migration runs in one transaction, so a
-- refusal changes nothing.
-- Otherwise it reverses everything. LOSSY: all custom providers and their manual models are dropped,
-- and so is the provider stored with each CI installation (the installations, CI runs and agents
-- are otherwise unchanged, EC35). The keys and extra-header values of custom providers live in the
-- secrets store, not here, and stay there. `default_model` and `feature_models` (settings) are not
-- touched and may keep a custom id.
DO $$
DECLARE
    n bigint;
    names text;
BEGIN
    SELECT count(*) INTO n FROM public.agents WHERE provider LIKE 'custom:%';
    IF n > 0 THEN
        SELECT string_agg(id::text || ' (' || provider || ')', ', ' ORDER BY id) INTO names
            FROM (SELECT id, provider FROM public.agents WHERE provider LIKE 'custom:%' ORDER BY id LIMIT 20) a;
        RAISE EXCEPTION 'cannot revert 0009_custom_providers: % agent(s) use a custom provider (up to 20 listed: %); switch them to openai, anthropic or openrouter, or delete them, then retry', n, names
            USING ERRCODE = 'restrict_violation';
    END IF;
END
$$;

ALTER TABLE public.ci_installations DROP CONSTRAINT ci_installations_provider_chk;
ALTER TABLE public.ci_installations DROP COLUMN provider;

ALTER TABLE public.agents DROP CONSTRAINT agents_provider_chk;
ALTER TABLE public.agents
    ADD CONSTRAINT agents_provider_chk CHECK ((provider = ANY (ARRAY['openai'::text, 'anthropic'::text, 'openrouter'::text])));

DROP TABLE public.custom_provider_models;
DROP TABLE public.custom_providers;

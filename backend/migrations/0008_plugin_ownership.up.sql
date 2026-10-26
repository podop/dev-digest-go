-- Plugins (Settings slice D, contract C2): an installed plugin owns the agents, skills and learnings
-- (memory rows) it created, and can be switched off as a whole.

-- A plugin's description from its manifest. NULL: none given, or the row predates this column.
ALTER TABLE public.installed_plugins ADD COLUMN description text;

-- A newly installed plugin starts disabled: the user reviews what it brought before it takes part in
-- reviews, evals and CI exports. Existing rows keep their value (a default change rewrites no row).
ALTER TABLE public.installed_plugins ALTER COLUMN enabled SET DEFAULT false;

-- One plugin per name in a workspace. A concurrent install of the same name fails with 23505 on
-- exactly this constraint name (the install maps it to 409). No code wrote installed_plugins before
-- this migration, so there are no duplicates to resolve and the constraint is plain.
ALTER TABLE public.installed_plugins
    ADD CONSTRAINT installed_plugins_workspace_id_name_key UNIQUE (workspace_id, name);

-- Owner of an agent, a skill or a learning: the plugin that created it. NULL: created by the user.
-- NO ACTION on delete, on purpose: removing a plugin deletes its children first (and the eval cases
-- owned by them), so a plugin row that still owns something cannot disappear silently.
ALTER TABLE public.agents ADD COLUMN plugin_id uuid;
ALTER TABLE public.skills ADD COLUMN plugin_id uuid;
ALTER TABLE public.memory ADD COLUMN plugin_id uuid;

ALTER TABLE public.agents
    ADD CONSTRAINT agents_plugin_id_installed_plugins_id_fk FOREIGN KEY (plugin_id) REFERENCES public.installed_plugins(id);
ALTER TABLE public.skills
    ADD CONSTRAINT skills_plugin_id_installed_plugins_id_fk FOREIGN KEY (plugin_id) REFERENCES public.installed_plugins(id);
ALTER TABLE public.memory
    ADD CONSTRAINT memory_plugin_id_installed_plugins_id_fk FOREIGN KEY (plugin_id) REFERENCES public.installed_plugins(id);

-- Foreign key columns are not indexed by PostgreSQL. Partial: most rows are user-owned (NULL).
CREATE INDEX agents_plugin_idx ON public.agents USING btree (plugin_id) WHERE plugin_id IS NOT NULL;
CREATE INDEX skills_plugin_idx ON public.skills USING btree (plugin_id) WHERE plugin_id IS NOT NULL;
CREATE INDEX memory_plugin_idx ON public.memory USING btree (plugin_id) WHERE plugin_id IS NOT NULL;

-- Drops exactly what 0008_plugin_ownership.up.sql added and restores the old default of enabled.
-- LOSSY (Settings spec D15 / CR1): the owner of every agent, skill and learning is dropped, so rows
-- a plugin installed become ordinary user-owned rows and a later Up cannot tell them apart again
-- (they stay, they are not deleted). Plugin descriptions are dropped. The installed_plugins rows
-- and their enabled values are kept; only the default of enabled returns to true.
DROP INDEX public.memory_plugin_idx;
DROP INDEX public.skills_plugin_idx;
DROP INDEX public.agents_plugin_idx;
ALTER TABLE public.memory DROP CONSTRAINT memory_plugin_id_installed_plugins_id_fk;
ALTER TABLE public.skills DROP CONSTRAINT skills_plugin_id_installed_plugins_id_fk;
ALTER TABLE public.agents DROP CONSTRAINT agents_plugin_id_installed_plugins_id_fk;
ALTER TABLE public.memory DROP COLUMN plugin_id;
ALTER TABLE public.skills DROP COLUMN plugin_id;
ALTER TABLE public.agents DROP COLUMN plugin_id;
ALTER TABLE public.installed_plugins DROP CONSTRAINT installed_plugins_workspace_id_name_key;
ALTER TABLE public.installed_plugins ALTER COLUMN enabled SET DEFAULT true;
ALTER TABLE public.installed_plugins DROP COLUMN description;

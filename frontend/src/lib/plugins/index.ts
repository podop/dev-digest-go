/* plugins/ — pure helpers over the owner plugin of an agent or skill (Settings › Integrations,
   specs/2026-10-23-settings-integrations.md FR13, FR18). No React, no fetching. */
import type { Agent, PluginRef } from "@devdigest/shared";

/** Anything the API returns with an owner plugin (agent and skill DTOs). */
export interface PluginOwned {
  plugin?: PluginRef | null;
}

/** True when the item belongs to a plugin that is switched off: it cannot take part in new runs. */
export function isPluginDisabled(item: PluginOwned | null | undefined): boolean {
  const plugin = item?.plugin;
  return plugin != null && !plugin.enabled;
}

/** Why an agent cannot be exported to CI. */
export type CiExportBlock = "provider_not_supported" | "plugin_disabled";

export type CiExportable = { ok: true } | { ok: false; reason: CiExportBlock };

/** CI export runs on OpenRouter only (FR3). */
export const CI_EXPORT_PROVIDER = "openrouter" as const;

/** Whether the install dialog may offer the agent: OpenRouter provider and not owned by a disabled plugin. */
export function ciExportable(agent: Pick<Agent, "provider"> & PluginOwned): CiExportable {
  if (agent.provider !== CI_EXPORT_PROVIDER) return { ok: false, reason: "provider_not_supported" };
  if (isPluginDisabled(agent)) return { ok: false, reason: "plugin_disabled" };
  return { ok: true };
}

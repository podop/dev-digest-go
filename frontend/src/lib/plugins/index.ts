/* plugins/ — pure helpers over the owner plugin of an agent or skill (Settings › Integrations,
   specs/2026-10-23-settings-integrations.md FR13, FR18). No React, no fetching. */
import type { Agent, PluginRef } from "@devdigest/shared";
import { BUILT_IN_PROVIDER_IDS, isCustomId, isUndefinedCustomId, type NamedCustomProvider } from "@/lib/providers/custom";

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
export type CiExportBlock = "provider_not_supported" | "plugin_disabled" | "custom_not_configured";

export type CiExportable = { ok: true } | { ok: false; reason: CiExportBlock };

/** Built-in providers CI export supports (custom-model-providers FR24, FR32). */
const CI_EXPORT_BUILT_IN: readonly string[] = BUILT_IN_PROVIDER_IDS;

/**
 * Whether the install dialog may offer the agent: an `openrouter`, `openai` or `anthropic` agent, or one on a
 * defined custom provider (`custom`), and not owned by a disabled plugin. An agent on an undefined custom id is
 * refused with `custom_not_configured` (EC34). `custom` undefined = the provider list is not known (loading or
 * failed): a well-formed custom id is then offered and the server decides (422 `provider_not_configured`).
 */
export function ciExportable(
  agent: Pick<Agent, "provider"> & PluginOwned,
  custom?: readonly NamedCustomProvider[] | null,
): CiExportable {
  const provider: string = agent.provider;
  if (isCustomId(provider)) {
    if (custom && isUndefinedCustomId(provider, custom)) return { ok: false, reason: "custom_not_configured" };
  } else if (!CI_EXPORT_BUILT_IN.includes(provider)) {
    return { ok: false, reason: "provider_not_supported" };
  }
  if (isPluginDisabled(agent)) return { ok: false, reason: "plugin_disabled" };
  return { ok: true };
}

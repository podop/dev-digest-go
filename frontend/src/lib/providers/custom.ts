/* providers/custom.ts — pure helpers over provider ids and custom providers (specs/2026-10-24-custom-model-providers.md
   FR19, FR20). No React, no fetching, no zod. Import as "@/lib/providers/custom": the bare "@/lib/providers"
   is the React provider stack (lib/providers.tsx) and wins over this folder. Every picker and every provider
   name in the UI goes through these functions, so the order and the naming are the same everywhere (SCR6). */

/** Built-in provider ids in picker order. */
export const BUILT_IN_PROVIDER_IDS = ["openrouter", "openai", "anthropic"] as const;
export type BuiltInProviderId = (typeof BUILT_IN_PROVIDER_IDS)[number];

/** Display names of the built-in providers (brand names, not translated). */
export const BUILT_IN_PROVIDER_NAMES: Record<BuiltInProviderId, string> = {
  openrouter: "OpenRouter",
  openai: "OpenAI",
  anthropic: "Anthropic",
};

/** `error.code` values of the custom-provider routes and of CI export (spec §7). */
export const CUSTOM_PROVIDER_ERROR_CODES = {
  notFound: "custom_provider_not_found",
  exists: "custom_provider_exists",
  limit: "custom_provider_limit",
  inUse: "provider_in_use",
  /** 422 of `POST /agents/:id/export-ci` for an undefined custom id. */
  notConfigured: "provider_not_configured",
} as const;

/** A well-formed custom provider id; mirrors `CUSTOM_PROVIDER_ID_PATTERN` of the shared contract (kept zod-free). */
const CUSTOM_ID = /^custom:[a-z0-9-]{1,32}$/;

/** Slug length limit of a custom id. */
export const CUSTOM_SLUG_MAX = 32;

/** Whether `id` is a well-formed custom provider id (`custom:<slug>`), defined or not. */
export function isCustomId(id: string | null | undefined): id is `custom:${string}` {
  return typeof id === "string" && CUSTOM_ID.test(id);
}

/** The slice of a custom provider the helpers read. */
export interface NamedCustomProvider {
  id: string;
  name: string;
}

/** One entry of a provider picker. */
export interface ProviderOption {
  value: string;
  label: string;
  custom: boolean;
}

/** Byte order, like the server's sort (not locale-aware). */
function byteCompare(a: string, b: string): number {
  return a < b ? -1 : a > b ? 1 : 0;
}

/**
 * Picker entries: OpenRouter, OpenAI, Anthropic, then the custom providers by name (then id), byte order.
 * `builtInLabels` overrides the display names (e.g. translated ones).
 */
export function providerOptions(
  custom: readonly NamedCustomProvider[] | null | undefined,
  builtInLabels: Partial<Record<BuiltInProviderId, string>> = {},
): ProviderOption[] {
  const builtIn = BUILT_IN_PROVIDER_IDS.map((id) => ({
    value: id,
    label: builtInLabels[id] ?? BUILT_IN_PROVIDER_NAMES[id],
    custom: false,
  }));
  const own = [...(custom ?? [])]
    .sort((a, b) => byteCompare(a.name, b.name) || byteCompare(a.id, b.id))
    .map((p) => ({ value: p.id, label: p.name, custom: true }));
  return [...builtIn, ...own];
}

/**
 * Display name of a provider id: a built-in's name, a defined custom provider's current name,
 * else the raw id (an undefined custom id such as `custom:ghost`).
 */
export function providerName(
  id: string | null | undefined,
  custom: readonly NamedCustomProvider[] | null | undefined,
  builtInLabels: Partial<Record<BuiltInProviderId, string>> = {},
): string {
  if (!id) return "";
  const builtIn = BUILT_IN_PROVIDER_IDS.find((b) => b === id);
  if (builtIn) return builtInLabels[builtIn] ?? BUILT_IN_PROVIDER_NAMES[builtIn];
  return custom?.find((p) => p.id === id)?.name ?? id;
}

/** Whether `id` is a custom id that no provider of `custom` defines ("Not configured"). */
export function isUndefinedCustomId(
  id: string | null | undefined,
  custom: readonly NamedCustomProvider[] | null | undefined,
): boolean {
  return isCustomId(id) && !custom?.some((p) => p.id === id);
}

/** Slug suggested from a display name: lower-cased, runs of characters outside `a-z0-9` → `-`, trimmed of `-`, cut to 32. */
export function slugFromName(name: string): string {
  return name
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, "-")
    .replace(/^-+|-+$/g, "")
    .slice(0, CUSTOM_SLUG_MAX)
    .replace(/-+$/, "");
}

/** Host (with a non-default port) of a base URL for display; the input itself when it does not parse. */
export function hostOf(baseUrl: string): string {
  try {
    return new URL(baseUrl).host;
  } catch {
    return baseUrl;
  }
}

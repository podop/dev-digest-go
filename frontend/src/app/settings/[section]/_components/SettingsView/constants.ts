/** Settings sections in sub-nav order (FR1). App-owned: the vendored SETTINGS_SECTIONS lists only two. */
export const SETTINGS_SECTION_KEYS = [
  "setup",
  "api-keys",
  "github",
  "gitlab",
  "workspace",
  "automatic-reviews",
  "integrations",
  "models",
  "about",
] as const;

export type SettingsSectionKey = (typeof SETTINGS_SECTION_KEYS)[number];

/** Default settings section (api-keys, not the first-listed setup); an unknown route key redirects here. */
export const DEFAULT_SECTION: SettingsSectionKey = "api-keys";

/** Route of a section (also the target of the unknown-key redirect). */
export const sectionHref = (key: SettingsSectionKey): string => `/settings/${key}`;

/** Narrow a route segment to a known section key. */
export function isSettingsSection(key: string): key is SettingsSectionKey {
  return (SETTINGS_SECTION_KEYS as readonly string[]).includes(key);
}

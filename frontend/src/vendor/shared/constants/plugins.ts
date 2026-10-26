/**
 * Zod-free Plugin constants (Settings › Integrations, specs/2026-10-23-settings-integrations.md §7),
 * re-exported by contracts/productionize. Import this subpath (`@devdigest/shared/constants/plugins`)
 * from client bundles and UI routes that must not pull zod in.
 */

const MIB = 1024 * 1024;

/** The one entry of an exported/imported `.zip`. */
export const PLUGIN_BUNDLE_FILE_NAME = 'devdigest-plugin.json';
export const PLUGIN_BUNDLE_FORMAT = 'devdigest-plugin/v1';

/** The two body types POST /plugins/import accepts (the zip is the only non-JSON body of the API). */
export const PLUGIN_ZIP_MEDIA_TYPE = 'application/zip';
export const PLUGIN_JSON_MEDIA_TYPE = 'application/json';

/** Import upload limits in bytes: a `.zip` body above 5 MiB or a bundle `.json` above 20 MiB is a 413. */
export const PLUGIN_ZIP_UPLOAD_MAX_BYTES = 5 * MIB;
export const PLUGIN_JSON_UPLOAD_MAX_BYTES = 20 * MIB;
/** Σ inflated size of a zip, and the largest bundle JSON an export writes (422 `export_too_large`). */
export const PLUGIN_BUNDLE_MAX_BYTES = 20 * MIB;
export const PLUGIN_ZIP_MAX_ENTRIES = 500;

/** `filename` query parameter of POST /plugins/import (the uploaded file's name, 1–255 chars). */
export const PLUGIN_UPLOAD_FILE_NAME_MIN = 1;
export const PLUGIN_UPLOAD_FILE_NAME_MAX = 255;

/** Bundle rules: plugin name (`manifest.name`) 1–80 chars after trimming, version ≤ 64. */
export const PLUGIN_NAME_MIN = 1;
export const PLUGIN_NAME_MAX = 80;
export const PLUGIN_VERSION_MAX = 64;
export const PLUGIN_MAX_AGENTS = 100;
export const PLUGIN_MAX_SKILLS = 200;
export const PLUGIN_MAX_EVAL_CASES = 1_000;
export const PLUGIN_MAX_LEARNINGS = 5_000;
export const PLUGIN_LEARNING_CONTENT_MAX = 20_000;
export const PLUGIN_AGENT_PROMPT_MAX = 100_000;
/** `details.issues` of a 422 `invalid_plugin` lists at most this many. */
export const PLUGIN_INVALID_ISSUES_MAX = 20;

/** Export request limits (POST /plugins/export). */
export const PLUGIN_EXPORT_NAME_MIN = 1;
export const PLUGIN_EXPORT_NAME_MAX = 64;
export const PLUGIN_EXPORT_DESCRIPTION_MAX = 300;
export const PLUGIN_EXPORT_AGENT_IDS_MAX = 200;
export const PLUGIN_EXPORT_SKILL_IDS_MAX = 500;
/** Defaults the server applies to an absent flag. */
export const PLUGIN_EXPORT_INCLUDE_EVAL_CASES_DEFAULT = true;
export const PLUGIN_EXPORT_INCLUDE_LEARNINGS_DEFAULT = false;
/** Export name: kebab-case (lowercase letters, digits, single dashes). */
export const PLUGIN_EXPORT_NAME_RE = /^[a-z0-9]+(?:-[a-z0-9]+)*$/;

/** ApiError codes of the plugin routes. */
export const PLUGIN_INVALID_CODE = 'invalid_plugin';
export const PLUGIN_CONFLICT_CODE = 'plugin_conflict';
export const PLUGIN_DISABLED_CODE = 'plugin_disabled';
export const PLUGIN_NOTHING_TO_EXPORT_CODE = 'nothing_to_export';
export const PLUGIN_EXPORT_TOO_LARGE_CODE = 'export_too_large';
export const PLUGIN_REMOVE_MISMATCH_CODE = 'confirmation_mismatch';
/** Codes of the import route's own refusals: 415 (neither zip nor json) and 413 (body over the limit). */
export const PLUGIN_UNSUPPORTED_MEDIA_CODE = 'unsupported_media_type';
export const PLUGIN_TOO_LARGE_CODE = 'payload_too_large';

/** What the import card accepts: a zip, or a bundle `.json` (Slice B's workspace export file). */
export type PluginUploadKind = 'zip' | 'json';

/** The upload kind of a file name by its extension (case-insensitive); null = refuse before any request. */
export function pluginUploadKind(fileName: string): PluginUploadKind | null {
  const lower = fileName.toLowerCase();
  if (lower.endsWith('.zip')) return 'zip';
  if (lower.endsWith('.json')) return 'json';
  return null;
}

/** `Content-Type` the import sends for an upload kind. */
export function pluginUploadMediaType(kind: PluginUploadKind): string {
  return kind === 'zip' ? PLUGIN_ZIP_MEDIA_TYPE : PLUGIN_JSON_MEDIA_TYPE;
}

/** Largest body in bytes the server takes for an upload kind. */
export function pluginUploadMaxBytes(kind: PluginUploadKind): number {
  return kind === 'zip' ? PLUGIN_ZIP_UPLOAD_MAX_BYTES : PLUGIN_JSON_UPLOAD_MAX_BYTES;
}

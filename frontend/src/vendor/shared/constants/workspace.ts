/**
 * Zod-free Workspace constants (Settings › Workspace), re-exported by contracts/workspace and
 * contracts/platform. Import this subpath (`@devdigest/shared/constants/workspace`) from client
 * bundles that must not pull zod in.
 */

/** Display-name length in Unicode code points (the Go side counts runes the same way). */
export const WORKSPACE_NAME_MIN = 1;
export const WORKSPACE_NAME_MAX = 80;

/** The repo switcher shows the name cut at this many code points + "…" (spec D24). */
export const WORKSPACE_SWITCHER_NAME_MAX = 32;

/** `retention_days` choices; `null` = keep forever, absent = RETENTION_DEFAULT_DAYS. */
export const RETENTION_DAYS_OPTIONS = [30, 60, 90, 180, 365] as const;
export const RETENTION_DEFAULT_DAYS = 90;

/** ApiError codes of POST /workspace/reset-history. */
export const WORKSPACE_RESET_MISMATCH_CODE = 'confirmation_mismatch';
export const WORKSPACE_RESET_ACTIVE_CODE = 'run_active';

/** `manifest.kind` of the GET /workspace/export bundle. */
export const WORKSPACE_BUNDLE_KIND = 'workspace';

/** Why a raw name is refused; `null` from {@link workspaceNameProblem} = valid. */
export type WorkspaceNameProblem = 'empty' | 'too_long' | 'control_character';

/** Name after the surrounding whitespace is trimmed (what the server stores). */
export function normalizeWorkspaceName(raw: string): string {
  return raw.trim();
}

/** Mirror of the server rule: trimmed, 1–80 code points, no U+0000–001F / U+007F. */
export function workspaceNameProblem(raw: string): WorkspaceNameProblem | null {
  const name = normalizeWorkspaceName(raw);
  let length = 0;
  for (const ch of name) {
    const code = ch.codePointAt(0) ?? 0;
    if (code <= 0x1f || code === 0x7f) return 'control_character';
    length += 1;
  }
  if (length < WORKSPACE_NAME_MIN) return 'empty';
  if (length > WORKSPACE_NAME_MAX) return 'too_long';
  return null;
}

/** Message key (under `context.store.errors`) of a rename failure the row shows inline. */
export type RenameErrorKey = "invalidPath" | "pathExists" | "stale" | "gone";

const RENAME_ERROR_BY_CODE: Record<string, RenameErrorKey> = {
  invalid_path: "invalidPath",
  path_exists: "pathExists",
  stale_version: "stale",
  doc_not_found: "gone",
};

/** Maps an ApiError code of a rename to its inline message; null = not an inline error. */
export function renameErrorKey(code: string | undefined): RenameErrorKey | null {
  return (code && RENAME_ERROR_BY_CODE[code]) || null;
}

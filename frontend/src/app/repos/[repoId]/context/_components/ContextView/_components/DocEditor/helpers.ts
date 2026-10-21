import { ApiError } from "@/lib/api";
import { isStaleVersionError } from "@/lib/hooks";

/** How long the "Saved" confirmation stays next to the Save button. */
export const SAVED_FLASH_MS = 2000;

/** A refused save the editor explains in a banner; other failures are toasted globally. */
export type SaveProblem = "stale" | "gone" | "exists";

/** ApiError code of a save/rename whose file was deleted or renamed meanwhile. */
const DOC_NOT_FOUND_CODE = "doc_not_found";
/** ApiError code of a create (re-create after "gone") whose path is taken again. */
const PATH_EXISTS_CODE = "path_exists";

export function saveProblem(err: unknown): SaveProblem | null {
  if (isStaleVersionError(err)) return "stale";
  if (err instanceof ApiError && err.code === DOC_NOT_FOUND_CODE) return "gone";
  if (err instanceof ApiError && err.code === PATH_EXISTS_CODE) return "exists";
  return null;
}

/** Ctrl+S / Cmd+S. */
export function isSaveShortcut(e: { key: string; ctrlKey: boolean; metaKey: boolean; altKey: boolean }): boolean {
  return (e.ctrlKey || e.metaKey) && !e.altKey && e.key.toLowerCase() === "s";
}

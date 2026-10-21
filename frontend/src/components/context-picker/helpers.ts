import { arrayMove } from "@dnd-kit/sortable";
import type { ContextDoc } from "@devdigest/shared";

/** One row of the picker: a listed document, or an attached path the repo no longer lists. */
export interface PickerRow {
  path: string;
  /** The listed document; absent for a `missing` row. */
  doc?: ContextDoc;
  attached: boolean;
  missing: boolean;
}

/** File name and its folder (with a trailing `/`, empty at the repo root). */
export function splitPath(path: string): { name: string; folder: string } {
  const i = path.lastIndexOf("/");
  return i < 0 ? { name: path, folder: "" } : { name: path.slice(i + 1), folder: path.slice(0, i + 1) };
}

/** Attached rows first in attach (prompt) order — those the repo no longer lists
 *  become `missing` rows — then the other documents in list order. */
export function buildRows(docs: readonly ContextDoc[], attached: readonly string[]): PickerRow[] {
  const byPath = new Map(docs.map((d) => [d.path, d]));
  const head: PickerRow[] = attached.map((path) => {
    const doc = byPath.get(path);
    return { path, doc, attached: true, missing: doc === undefined };
  });
  const attachedSet = new Set(attached);
  const tail: PickerRow[] = docs
    .filter((d) => !attachedSet.has(d.path))
    .map((doc) => ({ path: doc.path, doc, attached: false, missing: false }));
  return [...head, ...tail];
}

/** Rows whose path contains the query (case-insensitive); order is kept. */
export function filterRows(rows: readonly PickerRow[], query: string): PickerRow[] {
  const q = query.trim().toLowerCase();
  return q ? rows.filter((r) => r.path.toLowerCase().includes(q)) : [...rows];
}

/** The ordered path list after (un)checking a path: a new one goes last. */
export function toggleAttached(paths: readonly string[], path: string, on: boolean): string[] {
  const rest = paths.filter((p) => p !== path);
  return on ? [...rest, path] : rest;
}

/** The ordered path list after dropping `activeId` onto `overId` (null = no move). */
export function moveAttached(paths: readonly string[], activeId: string, overId: string | null): string[] | null {
  if (!overId || activeId === overId) return null;
  const from = paths.indexOf(activeId);
  const to = paths.indexOf(overId);
  if (from < 0 || to < 0) return null;
  return arrayMove([...paths], from, to);
}

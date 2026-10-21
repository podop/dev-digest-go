import type { Conflict } from "@devdigest/shared";

/** Groups to show: all of them, or only the real conflicts. */
export function visibleGroups(groups: readonly Conflict[], onlyConflicts: boolean): Conflict[] {
  return onlyConflicts ? groups.filter((g) => g.is_conflict) : [...groups];
}

/** "src/a.ts:11" for a single line, "src/a.ts:11-15" for a range. */
export function groupLocation(g: Pick<Conflict, "file" | "line" | "end_line">): string {
  return g.end_line > g.line ? `${g.file}:${g.line}-${g.end_line}` : `${g.file}:${g.line}`;
}

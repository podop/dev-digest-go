import type { LeaderboardRow } from "@devdigest/shared";
import type { SortKey, SortState } from "./constants";

const SIZE_ORDER: Record<NonNullable<LeaderboardRow["size"]>, number> = { S: 1, M: 2, L: 3, XL: 4 };

/** A row with its position in the server order (the "#" column stays that rank while sorting). */
export interface RankedRow {
  row: LeaderboardRow;
  rank: number;
}

export function rankRows(rows: readonly LeaderboardRow[]): RankedRow[] {
  return rows.map((row, i) => ({ row, rank: i + 1 }));
}

/** The time a PR took: merge time when merged, else how long it has been open; null when unknown. */
export function timeValue(row: LeaderboardRow): number | null {
  return row.time_to_merge_s ?? row.open_age_s;
}

/** Scalar sort value of a column, null when the row has none (null rows always sort last). */
function scalar(row: LeaderboardRow, key: Exclude<SortKey, "findings">): number | null {
  switch (key) {
    case "size":
      return row.size === null ? null : SIZE_ORDER[row.size];
    case "health":
      return row.health;
    case "rounds":
      return row.rounds;
    case "time":
      return timeValue(row);
    case "cost":
      return row.cost_usd;
  }
}

/** Findings compare by critical, then warning, then suggestion. */
function compareFindings(a: LeaderboardRow, b: LeaderboardRow): number {
  return (
    a.findings.CRITICAL - b.findings.CRITICAL ||
    a.findings.WARNING - b.findings.WARNING ||
    a.findings.SUGGESTION - b.findings.SUGGESTION
  );
}

/** Next sort after a click on `key`: descending, then ascending, then back to the server rank. */
export function nextSort(current: SortState | null, key: SortKey): SortState | null {
  if (current?.key !== key) return { key, dir: "desc" };
  return current.dir === "desc" ? { key, dir: "asc" } : null;
}

/** Rows ordered by `sort` (null ⇒ server rank). Nulls last in both directions; ties keep the rank. */
export function sortRows(rows: readonly RankedRow[], sort: SortState | null): RankedRow[] {
  if (sort === null) return [...rows];
  const sign = sort.dir === "asc" ? 1 : -1;
  return [...rows].sort((x, y) => {
    if (sort.key === "findings") return sign * compareFindings(x.row, y.row) || x.rank - y.rank;
    const a = scalar(x.row, sort.key);
    const b = scalar(y.row, sort.key);
    if (a === null || b === null) return a === b ? x.rank - y.rank : a === null ? 1 : -1;
    return sign * (a - b) || x.rank - y.rank;
  });
}

export function ariaSort(sort: SortState | null, key: SortKey): "ascending" | "descending" | "none" {
  if (sort?.key !== key) return "none";
  return sort.dir === "asc" ? "ascending" : "descending";
}

export type DurationParts = { unit: "hours" | "days"; value: string };

const HOUR_S = 3600;
const DAY_S = 86400;

/** Review / merge time: hours with one decimal, days from 48 h on. */
export function durationParts(seconds: number): DurationParts {
  if (seconds < 2 * DAY_S) return { unit: "hours", value: (seconds / HOUR_S).toFixed(1) };
  return { unit: "days", value: (seconds / DAY_S).toFixed(1) };
}

/** How long a PR has been open: whole hours under a day, whole days after. */
export function openAgeParts(seconds: number): DurationParts {
  if (seconds < DAY_S) return { unit: "hours", value: String(Math.floor(seconds / HOUR_S)) };
  return { unit: "days", value: String(Math.floor(seconds / DAY_S)) };
}

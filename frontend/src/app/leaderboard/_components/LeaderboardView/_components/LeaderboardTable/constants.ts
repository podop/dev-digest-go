import type { LeaderboardRow } from "@devdigest/shared";

/** Rows shown before "Show all N". */
export const TOP_N = 10;

/** Sortable columns, in display order; every other column keeps the server rank. */
export const SORT_KEYS = ["size", "health", "findings", "rounds", "time", "cost"] as const;
export type SortKey = (typeof SORT_KEYS)[number];

export type SortDir = "asc" | "desc";
export interface SortState {
  key: SortKey;
  dir: SortDir;
}

/** Size bucket → colour token (the PR list only knows S/M/L; the leaderboard adds XL). */
export const SIZE_COLOR: Record<NonNullable<LeaderboardRow["size"]>, string> = {
  S: "var(--ok)",
  M: "var(--warn)",
  L: "var(--crit)",
  XL: "var(--crit)",
};

/** Risk → colour token (the label carries the meaning, the dot is decoration). */
export const RISK_COLOR: Record<LeaderboardRow["risk"], string> = {
  low: "var(--ok)",
  medium: "var(--warn)",
  high: "var(--crit)",
};

/** Number of columns — the detail row spans them all. */
export const COLUMN_COUNT = 11;

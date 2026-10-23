import type { LeaderboardRow, LeaderboardTopFinding } from "@devdigest/shared";

/** Rows shown before "Show all N". */
export const TOP_N = 10;

/** Sortable columns, in display order; every other column keeps the server rank. */
export const SORT_KEYS = ["size", "health", "findings", "rounds", "time", "cost"] as const;
export type SortKey = (typeof SORT_KEYS)[number];

export type SortDir = "asc" | "desc";

/** The direction a column starts with: descending, except Time (shortest first). */
export const firstSortDir = (key: SortKey): SortDir => (key === "time" ? "asc" : "desc");
export interface SortState {
  key: SortKey;
  dir: SortDir;
}

/** Size bucket → colour token (the PR list only knows S/M/L; the leaderboard adds XS and XL). */
export const SIZE_COLOR: Record<NonNullable<LeaderboardRow["size"]>, string> = {
  XS: "var(--ok)",
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

/** A finding's state → badge colours (accepted ok, dismissed muted, open warn). */
export const STATE_COLOR: Record<LeaderboardTopFinding["state"], { c: string; bg: string }> = {
  accepted: { c: "var(--ok)", bg: "var(--ok-bg)" },
  dismissed: { c: "var(--text-muted)", bg: "var(--bg-hover)" },
  open: { c: "var(--warn)", bg: "var(--warn-bg)" },
};

/** Number of columns — the detail row spans them all. */
export const COLUMN_COUNT = 11;

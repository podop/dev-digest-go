/** Sortable columns of the agent table, in display order (the URL `sort` values). */
export const SORT_KEYS = ["agent", "runs", "avg_cost", "avg_duration", "accept_rate", "last_run"] as const;
export type SortKey = (typeof SORT_KEYS)[number];

export const SORT_DIRS = ["asc", "desc"] as const;
export type SortDir = (typeof SORT_DIRS)[number];

export interface SortState {
  key: SortKey;
  dir: SortDir;
}

/** Accept rate descending — the quality signal first (FR14). */
export const DEFAULT_SORT: SortState = { key: "accept_rate", dir: "desc" };

/** The direction a column starts with when it is clicked: names read A→Z, numbers biggest first. */
export const DEFAULT_DIR: Record<SortKey, SortDir> = {
  agent: "asc",
  runs: "desc",
  avg_cost: "desc",
  avg_duration: "desc",
  accept_rate: "desc",
  last_run: "desc",
};

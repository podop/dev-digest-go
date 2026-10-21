import type { AgentPeriodMetrics, AgentPerfRow } from "@devdigest/shared";
import { periodSearch, type StatsPeriod } from "@/lib/stats-period";
import { DEFAULT_DIR, DEFAULT_SORT, SORT_DIRS, SORT_KEYS, type SortKey, type SortState } from "./constants";

type RawParam = string | string[] | undefined;

const first = (raw: RawParam): string | undefined => (Array.isArray(raw) ? raw[0] : raw);

/** Normalize the raw ?sort=&dir= values; an unknown column → the default sort, an unknown direction → the column's own. */
export function resolveSort(rawSort: RawParam, rawDir: RawParam): SortState {
  const key = SORT_KEYS.find((k) => k === first(rawSort));
  if (!key) return DEFAULT_SORT;
  const dir = SORT_DIRS.find((d) => d === first(rawDir)) ?? DEFAULT_DIR[key];
  return { key, dir };
}

/** Dashboard URL for a period and a sort. */
export function dashboardHref(period: StatsPeriod, sort: SortState): string {
  return `/agent-performance?${periodSearch(period)}&sort=${sort.key}&dir=${sort.dir}`;
}

/** The sort after a header click: the same column flips direction, another column starts at its own default. */
export function nextSort(current: SortState, key: SortKey): SortState {
  if (current.key !== key) return { key, dir: DEFAULT_DIR[key] };
  return { key, dir: current.dir === "asc" ? "desc" : "asc" };
}

/** Accept-rate tier: enough decisions, then small samples, then no decisions at all (FR14). */
function acceptTier(m: AgentPeriodMetrics): number {
  if (m.decisions === 0) return 2;
  return m.small_sample ? 1 : 0;
}

/** The numeric value a column sorts by; null = unknown (sorted last). */
function numericValue(m: AgentPeriodMetrics, key: Exclude<SortKey, "agent">): number | null {
  switch (key) {
    case "runs":
      return m.runs;
    case "avg_cost":
      return m.avg_cost_usd;
    case "avg_duration":
      return m.avg_duration_ms;
    case "accept_rate":
      return m.accept_rate;
    case "last_run": {
      const at = m.last_run_at ? Date.parse(m.last_run_at) : Number.NaN;
      return Number.isNaN(at) ? null : at;
    }
  }
}

const byName = (a: AgentPerfRow, b: AgentPerfRow) => a.agent_name.localeCompare(b.agent_name);

/**
 * Client-side table order (FR14): nulls always last, ties by agent name ascending, and accept rate
 * ranked in three fixed tiers (≥ min decisions, small sample, no decisions) with the direction
 * applied inside each tier. Returns a new array.
 */
export function sortRows(rows: readonly AgentPerfRow[], sort: SortState): AgentPerfRow[] {
  const sign = sort.dir === "asc" ? 1 : -1;
  const { key } = sort;
  return [...rows].sort((a, b) => {
    if (key === "agent") return sign * byName(a, b);
    if (key === "accept_rate") {
      const tiers = acceptTier(a.metrics) - acceptTier(b.metrics);
      if (tiers !== 0) return tiers;
    }
    const va = numericValue(a.metrics, key);
    const vb = numericValue(b.metrics, key);
    if (va == null && vb == null) return byName(a, b);
    if (va == null) return 1;
    if (vb == null) return -1;
    return sign * (va - vb) || byName(a, b);
  });
}

/** A run's start as a local date-time in the browser's locale; null when unknown or not a date. */
export function formatLastRun(iso: string | null): string | null {
  if (!iso) return null;
  const d = new Date(iso);
  return Number.isNaN(d.getTime()) ? null : d.toLocaleString();
}

/** Pure parts of the KPI tiles and award cards (specs/2026-10-23-pr-leaderboard.md FR19, FR20). */
import type { LeaderboardKpis } from "@devdigest/shared";

export type KpiKey = keyof LeaderboardKpis;

/** How a KPI value is written: a plain count, a 1-decimal score, whole seconds, or USD. */
export type KpiKind = "count" | "health" | "duration" | "usd";

export interface KpiSpec {
  key: KpiKey;
  kind: KpiKind;
  /** Decides the colour of the delta: a rise is green when true, red when false. */
  higherIsBetter: boolean;
}

/** The eight tiles in display order. */
export const KPI_SPECS: readonly KpiSpec[] = [
  { key: "prs_reviewed", kind: "count", higherIsBetter: true },
  { key: "merged", kind: "count", higherIsBetter: true },
  { key: "avg_health", kind: "health", higherIsBetter: true },
  { key: "time_to_first_review_s", kind: "duration", higherIsBetter: false },
  { key: "time_to_merge_s", kind: "duration", higherIsBetter: false },
  { key: "critical_found", kind: "count", higherIsBetter: false },
  { key: "open_critical", kind: "count", higherIsBetter: false },
  { key: "review_cost_usd", kind: "usd", higherIsBetter: false },
];

export type DeltaDirection = "up" | "down" | "same";

export interface KpiDelta {
  direction: DeltaDirection;
  /** Absolute difference, in the unit of the KPI kind. */
  amount: number;
  /** Moved the good way; null when nothing changed. */
  good: boolean | null;
}

/** Shown precision of a value: health has one decimal, money six (the wire rounding), the rest is whole. */
function round(kind: KpiKind, value: number): number {
  if (kind === "health") return Math.round(value * 10) / 10;
  if (kind === "usd") return Math.round(value * 1e6) / 1e6;
  return value;
}

/** A move smaller than this (in the KPI's unit, FR19) reads "no change". */
const NO_CHANGE_BELOW = 0.05;

/** Change against the previous window; null when either side is unknown (no delta is shown then). */
export function kpiDelta(spec: KpiSpec, current: number | null, previous: number | null): KpiDelta | null {
  if (current == null || previous == null) return null;
  const diff = Number((round(spec.kind, current) - round(spec.kind, previous)).toFixed(6));
  if (Math.abs(diff) < NO_CHANGE_BELOW) return { direction: "same", amount: 0, good: null };
  return { direction: diff > 0 ? "up" : "down", amount: Math.abs(diff), good: spec.higherIsBetter ? diff > 0 : diff < 0 };
}

/** Plain score for a tile or award: 1 decimal at most, no trailing ".0". */
export function formatScore(value: number): string {
  return String(Math.round(value * 10) / 10);
}

export type DurationKey = "seconds" | "minutes" | "hoursMinutes" | "daysHours";

/** Whole seconds → the two most significant units ("45s", "12m", "3h 20m", "2d 4h"). */
export function durationParts(seconds: number): { key: DurationKey; values: Record<string, number> } {
  const total = Math.max(0, Math.round(seconds));
  if (total < 60) return { key: "seconds", values: { s: total } };
  if (total < 3600) return { key: "minutes", values: { m: Math.floor(total / 60) } };
  if (total < 86_400) return { key: "hoursMinutes", values: { h: Math.floor(total / 3600), m: Math.floor((total % 3600) / 60) } };
  return { key: "daysHours", values: { d: Math.floor(total / 86_400), h: Math.floor((total % 86_400) / 3600) } };
}

/** Translator for `leaderboardHighlights` keys (the `t` of useTranslations("leaderboardHighlights")). */
export type HighlightsTranslate = (key: string, values?: Record<string, number | string>) => string;

export function formatDuration(t: HighlightsTranslate, seconds: number): string {
  const { key, values } = durationParts(seconds);
  return t(`duration.${key}`, values);
}

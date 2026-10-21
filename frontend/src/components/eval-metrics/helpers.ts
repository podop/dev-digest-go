import type { EvalMetricName, EvalRunSummary } from "@devdigest/shared";

/** The three code-only metrics, in tile order. */
export const EVAL_METRICS = ["recall", "precision", "citation_accuracy"] as const satisfies readonly EvalMetricName[];

export type DeltaDirection = "up" | "down" | "flat";

/** 0–1 ratio → whole percent; null stays null (metric not defined for the run). */
export function toPercent(ratio: number | null | undefined): number | null {
  return ratio == null ? null : Math.round(ratio * 100);
}

export function metricOf(run: EvalRunSummary | null | undefined, metric: EvalMetricName): number | null {
  return run ? run[metric] : null;
}

/** Signed difference in whole percentage points (difference of the shown values, so
 *  the arrow always agrees with the two percentages on screen); null if either is missing. */
export function deltaPoints(current: number | null | undefined, previous: number | null | undefined): number | null {
  const now = toPercent(current);
  const before = toPercent(previous);
  return now == null || before == null ? null : now - before;
}

export function directionOf(delta: number): DeltaDirection {
  return delta > 0 ? "up" : delta < 0 ? "down" : "flat";
}

/** Signed change of the passed-case count; null while the latest run is still running. */
export function casesPassedDelta(latest: EvalRunSummary | null, previous: EvalRunSummary | null): number | null {
  if (!latest || !previous || latest.status === "running") return null;
  return latest.cases_passed - previous.cases_passed;
}

/** Defined values of a metric across runs (oldest first) — the sparkline series. */
export function sparkSeries(runs: readonly EvalRunSummary[], metric: EvalMetricName): number[] {
  return runs.flatMap((r) => (r[metric] == null ? [] : [r[metric]]));
}

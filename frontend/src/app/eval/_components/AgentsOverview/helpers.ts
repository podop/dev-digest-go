import type { EvalSparkPoint } from "@devdigest/shared";

/** Minimum points for a sparkline to draw a line (one point has no direction). */
export const MIN_SPARK_POINTS = 2;

/** Defined recall values of the agent's recent finished runs, oldest first. */
export function recallSeries(points: readonly EvalSparkPoint[]): number[] {
  return points.flatMap((p) => (p.recall == null ? [] : [p.recall]));
}

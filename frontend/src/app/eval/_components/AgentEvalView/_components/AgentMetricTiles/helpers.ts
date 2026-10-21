import type { EvalMetricName, EvalRunSummary } from "@devdigest/shared";
import { deltaPoints, directionOf, metricOf, type DeltaDirection } from "@/components/eval-metrics";

export interface TileDelta {
  direction: DeltaDirection;
  /** Two-decimal ratio, e.g. "0.04" (the difference of the shown percents ÷ 100). */
  amount: string;
}

/** Change of a metric vs the previous finished run; null when either run has no value. */
export function tileDelta(latest: EvalRunSummary | null, previous: EvalRunSummary | null, metric: EvalMetricName): TileDelta | null {
  const points = deltaPoints(metricOf(latest, metric), metricOf(previous, metric));
  return points == null ? null : { direction: directionOf(points), amount: (Math.abs(points) / 100).toFixed(2) };
}

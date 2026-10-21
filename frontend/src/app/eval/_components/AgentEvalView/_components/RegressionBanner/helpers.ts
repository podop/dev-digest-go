import type { EvalMetricName, EvalRegressionAlert, EvalRunSummary } from "@devdigest/shared";
import { deltaPoints, directionOf, EVAL_METRICS, metricOf, type DeltaDirection } from "@/components/eval-metrics";

export interface MetricChange {
  metric: EvalMetricName;
  direction: DeltaDirection;
}

/** Metrics that did not dip, with the direction they moved vs the previous run (sign of the
 *  shown percent difference); a metric missing in either run is left out. */
export function otherMetricChanges(
  alert: EvalRegressionAlert,
  latest: EvalRunSummary,
  previous: EvalRunSummary | null,
): MetricChange[] {
  const dipped = new Set(alert.metrics.map((m) => m.metric));
  return EVAL_METRICS.flatMap((metric) => {
    if (dipped.has(metric)) return [];
    const change = deltaPoints(metricOf(latest, metric), metricOf(previous, metric));
    return change == null ? [] : [{ metric, direction: directionOf(change) }];
  });
}

/** Shape of the closing sentence: nothing, one metric, two moving alike, or two that differ. */
export type OthersSentence =
  | { kind: "none" }
  | { kind: "one"; a: MetricChange }
  | { kind: "both"; a: MetricChange; b: MetricChange }
  | { kind: "mixed"; a: MetricChange; b: MetricChange };

export function othersSentence(changes: readonly MetricChange[]): OthersSentence {
  const [a, b] = changes;
  if (!a) return { kind: "none" };
  if (!b) return { kind: "one", a };
  return a.direction === b.direction ? { kind: "both", a, b } : { kind: "mixed", a, b };
}

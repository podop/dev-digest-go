import type { EvalCompare, EvalMetricName } from "@devdigest/shared";
import { deltaPoints } from "@/components/eval-metrics";

export interface MetricChange {
  metric: EvalMetricName;
  from: number | null;
  to: number | null;
  /** Signed whole points of the shown percentages (B − A); null if either side is undefined. */
  points: number | null;
}

export function metricChanges(compare: EvalCompare, metrics: readonly EvalMetricName[]): MetricChange[] {
  return metrics.map((metric) => ({
    metric,
    from: compare.a[metric],
    to: compare.b[metric],
    points: deltaPoints(compare.b[metric], compare.a[metric]),
  }));
}

/** The prompt is identical when no line was added or removed. */
export function isPromptUnchanged(compare: EvalCompare): boolean {
  return compare.config_diff.system_prompt.every((l) => l.op === "same");
}

export function flippedCount(compare: EvalCompare): number {
  return compare.cases.filter((c) => c.flipped).length;
}

/** Whether model, strategy and the skill set are all unchanged. */
export function isConfigUnchanged(compare: EvalCompare): boolean {
  const { model, strategy, skills_added, skills_removed } = compare.config_diff;
  return model == null && strategy == null && skills_added.length === 0 && skills_removed.length === 0;
}

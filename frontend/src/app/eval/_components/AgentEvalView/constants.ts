import type { EvalMetricName } from "@devdigest/shared";

/** Message key (under eval.metricTiles) of each metric's label. */
export const METRIC_LABEL_KEY: Record<EvalMetricName, string> = {
  recall: "recall",
  precision: "precision",
  citation_accuracy: "citationAccuracy",
};

/** Runs compared side by side. */
export const COMPARE_RUN_COUNT = 2;

import type { EvalMetricName } from "@devdigest/shared";
import type { EvalPeriodId } from "@devdigest/shared/constants/evals";

/** SVG viewBox and plot margins (px). The overlay of run buttons is sized in the same units. */
export const CHART = { width: 960, height: 240, left: 44, right: 16, top: 14, bottom: 14 } as const;

/** The axis bottom when every plotted value is at or above it (FR6). */
export const AXIS_FLOOR = 0.6;
/** Gridlines per unit on the y axis: one every 0.1. */
export const AXIS_DIVISIONS = 10;

/** Width (viewBox px) of the lone run's hover column. */
export const LONE_COLUMN_WIDTH = 40;

/** Message key under eval.dashboard.legend per metric. */
export const LEGEND_KEY: Record<EvalMetricName, string> = {
  recall: "recall",
  precision: "precision",
  citation_accuracy: "citation",
};

/** Message key under eval.trend of the empty text per period. */
export const EMPTY_KEY: Record<EvalPeriodId, string> = {
  "7d": "empty7d",
  "30d": "empty30d",
  "90d": "empty90d",
  all: "emptyAll",
};

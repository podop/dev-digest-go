import type { EvalMetricName } from "@devdigest/shared";
import type { EvalPeriodId } from "@devdigest/shared/constants/evals";

/** Period an absent or unknown ?period= falls back to. */
export const DEFAULT_EVAL_PERIOD: EvalPeriodId = "30d";

/** Rows the agent view asks for (the runs endpoint caps `limit` at 100). */
export const AGENT_RUNS_LIMIT = 100;

/** Series colour of each metric, shared by the tiles and the trend chart. */
export const METRIC_COLOR: Record<EvalMetricName, string> = {
  recall: "var(--accent)",
  precision: "var(--ok)",
  citation_accuracy: "var(--warn)",
};

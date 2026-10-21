import type { EvalOverview } from "@devdigest/shared";

/** Nothing to show yet: no agent has eval cases or runs. */
export function isOverviewEmpty(overview: EvalOverview): boolean {
  return overview.agents.length === 0 && overview.recent_runs.length === 0;
}

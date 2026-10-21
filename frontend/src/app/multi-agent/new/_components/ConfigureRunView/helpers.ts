import type { Agent, AgentRunEstimate } from "@devdigest/shared";

/** Index estimates by agent id. */
export function estimatesByAgent(list: readonly AgentRunEstimate[] | undefined): Map<string, AgentRunEstimate> {
  return new Map((list ?? []).map((e) => [e.agent_id, e]));
}

/** Agents checked before the user edits the selection: the `?agents=` ids that exist,
 *  otherwise every enabled agent. */
export function defaultSelectedIds(agents: readonly Pick<Agent, "id" | "enabled">[], requested: readonly string[] | null): string[] {
  if (requested) return agents.filter((a) => requested.includes(a.id)).map((a) => a.id);
  return agents.filter((a) => a.enabled).map((a) => a.id);
}

/** "8.2s" from milliseconds. */
export function formatSeconds(ms: number): string {
  return `${(ms / 1000).toFixed(1)}s`;
}

export interface FooterTotals {
  /** Longest average duration — agents run in parallel; null when no checked agent has one. */
  durationMs: number | null;
  /** Sum of average costs; null when no checked agent has one. */
  costUsd: number | null;
}

/** Parallel fan-out totals over the checked agents that have an estimate. */
export function footerTotals(estimates: readonly (AgentRunEstimate | undefined)[]): FooterTotals {
  const durations = estimates.flatMap((e) => (e?.avg_duration_ms != null ? [e.avg_duration_ms] : []));
  const costs = estimates.flatMap((e) => (e?.avg_cost_usd != null ? [e.avg_cost_usd] : []));
  return {
    durationMs: durations.length > 0 ? Math.max(...durations) : null,
    costUsd: costs.length > 0 ? costs.reduce((sum, c) => sum + c, 0) : null,
  };
}

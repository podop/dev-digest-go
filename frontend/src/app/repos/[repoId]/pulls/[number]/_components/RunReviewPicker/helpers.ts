import type { Agent, AgentRunEstimate } from "@devdigest/shared";

/** Agents checked when the picker opens: every enabled one. */
export function defaultSelectedIds(agents: readonly Pick<Agent, "id" | "enabled">[]): string[] {
  return agents.filter((a) => a.enabled).map((a) => a.id);
}

/** Whole seconds of an average duration (at least 1), or null without history. */
export function estimateSeconds(estimate: Pick<AgentRunEstimate, "avg_duration_ms"> | undefined): number | null {
  const ms = estimate?.avg_duration_ms;
  return typeof ms === "number" ? Math.max(1, Math.round(ms / 1000)) : null;
}

/** Index estimates by agent id. */
export function estimatesByAgent(list: readonly AgentRunEstimate[] | undefined): Map<string, AgentRunEstimate> {
  return new Map((list ?? []).map((e) => [e.agent_id, e]));
}

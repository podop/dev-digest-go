import type { Agent } from "@devdigest/shared";
import { ciExportable, isPluginDisabled, type CiExportBlock } from "@/lib/plugins";

/** An agent the dialog lists: selectable (`block` null) or shown unselectable with the reason. */
export interface AgentOffer {
  agent: Agent;
  block: CiExportBlock | null;
}

/** Agents of a disabled plugin are not offered at all (FR18, A13); the rest are listed, and
 *  the ones that cannot go to CI (not OpenRouter) carry the reason. */
export function offeredAgents(agents: readonly Agent[]): AgentOffer[] {
  return agents
    .filter((agent) => !isPluginDisabled(agent))
    .map((agent) => {
      const check = ciExportable(agent);
      return { agent, block: check.ok ? null : check.reason };
    });
}

/** What happened to one agent in the current run. */
export type AgentRun =
  | { status: "queued" }
  | { status: "running" }
  | { status: "done"; prUrl: string | null }
  | { status: "failed"; message: string | null };

export type RunMap = Readonly<Record<string, AgentRun>>;

/** Ids whose install failed, in the order of `order`. */
export function failedIds(runs: RunMap, order: readonly string[]): string[] {
  return order.filter((id) => runs[id]?.status === "failed");
}

/** True once a run was started (the repo and the selection are then locked). */
export function hasStarted(runs: RunMap): boolean {
  return Object.keys(runs).length > 0;
}

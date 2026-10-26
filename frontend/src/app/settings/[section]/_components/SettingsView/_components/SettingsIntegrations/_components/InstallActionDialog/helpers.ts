import type { Agent, CiExportInputBody } from "@devdigest/shared";
import { ciExportable, isPluginDisabled, type CiExportBlock } from "@/lib/plugins";
import { isCustomId, type NamedCustomProvider } from "@/lib/providers/custom";

/** `runs-on` choice of the generated workflow (FR31, FR32). */
export type Runner = NonNullable<CiExportInputBody["runner"]>;
export const RUNNERS: readonly Runner[] = ["github_hosted", "self_hosted"];

/** An agent the dialog lists: selectable (`block` null) or shown unselectable with the reason. */
export interface AgentOffer {
  agent: Agent;
  block: CiExportBlock | null;
}

/** Agents of a disabled plugin are not offered at all (FR18, A13); the rest are listed, and
 *  the ones that cannot go to CI (an undefined custom provider, FR32) carry the reason.
 *  `custom` is the workspace's custom providers, undefined while they are not known. */
export function offeredAgents(
  agents: readonly Agent[],
  custom?: readonly NamedCustomProvider[] | null,
): AgentOffer[] {
  return agents
    .filter((agent) => !isPluginDisabled(agent))
    .map((agent) => {
      const check = ciExportable(agent, custom);
      return { agent, block: check.ok ? null : check.reason };
    });
}

/** The Runner the dialog starts with: Self-hosted when any selected agent uses a custom provider, else
 *  GitHub-hosted (FR32, D18). */
export function defaultRunner(offers: readonly AgentOffer[], selected: ReadonlySet<string>): Runner {
  return offers.some((o) => selected.has(o.agent.id) && isCustomId(o.agent.provider)) ? "self_hosted" : "github_hosted";
}

/** What happened to one agent in the current run. */
export type AgentRun =
  | { status: "queued" }
  | { status: "running" }
  | { status: "done"; prUrl: string | null; secrets: readonly string[]; variables: readonly string[] }
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

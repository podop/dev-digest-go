import type { Agent, EvalRunSummary, SkillAgentRef } from "@devdigest/shared";

/** The newest finished run and the one before it (runs arrive newest first). "Finished" is completed or failed
 *  (same as the server's FINISHED set); a still-running run feeds no tile. */
export function latestFinishedPair(runs: readonly EvalRunSummary[] | undefined): {
  latest: EvalRunSummary | null;
  previous: EvalRunSummary | null;
} {
  const finished = (runs ?? []).filter((r) => r.status === "completed" || r.status === "failed");
  return { latest: finished[0] ?? null, previous: finished[1] ?? null };
}

/** "Run with" default: the first agent (by name) that uses the skill, else the first agent by name. */
export function defaultHostAgentId(agents: readonly Agent[], usedBy: readonly SkillAgentRef[] | undefined): string | null {
  const byName = [...agents].sort((a, b) => a.name.localeCompare(b.name));
  const users = new Set((usedBy ?? []).map((a) => a.id));
  return (byName.find((a) => users.has(a.id)) ?? byName[0])?.id ?? null;
}

/** The agent the run goes to: the author's pick while that agent still exists, else the default. */
export function effectiveHostAgentId(
  agents: readonly Agent[],
  usedBy: readonly SkillAgentRef[] | undefined,
  picked: string | null,
): string | null {
  return picked && agents.some((a) => a.id === picked) ? picked : defaultHostAgentId(agents, usedBy);
}

/** Why "Run all evals" is disabled once the data is in, or null when it can run. */
export function runAllBlocker(input: { noCases: boolean; noAgents: boolean }): "noCases" | "noAgents" | null {
  if (input.noCases) return "noCases";
  if (input.noAgents) return "noAgents";
  return null;
}

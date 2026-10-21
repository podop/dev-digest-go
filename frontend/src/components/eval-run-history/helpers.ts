import type { EvalRunDetail, EvalRunSummary } from "@devdigest/shared";

/** A run row (every run carries the agent name frozen at its start). */
export type RunsTableRow = EvalRunSummary;

/** The version a row is labelled with: the skill's for a skill run, the agent's otherwise. */
export function runVersion(run: EvalRunSummary, variant: "agent" | "skill"): number {
  return variant === "skill" ? (run.skill_version ?? run.agent_version) : run.agent_version;
}

export function formatRunDate(iso: string): string {
  const d = new Date(iso);
  return Number.isNaN(d.getTime()) ? iso : d.toLocaleString();
}

/** Only a finished run can be compared; a running run has no metrics yet. */
export function isSelectable(run: EvalRunSummary): boolean {
  return run.status !== "running";
}

/** Toggle `id` in the selection, keeping click order. */
export function toggleSelected(selected: readonly string[], id: string): string[] {
  return selected.includes(id) ? selected.filter((x) => x !== id) : [...selected, id];
}


/** The run to follow: the one the runs list says is running, else the one this tab just started. */
export function followedRunId(runs: readonly EvalRunSummary[] | undefined, startedId: string | undefined): string | undefined {
  return runs?.find((r) => r.status === "running")?.id ?? startedId;
}

/** The run that is currently executing, from the freshest data we have (the polled detail beats the list). */
export function runningRun(detail: EvalRunDetail | undefined, runs: readonly EvalRunSummary[] | undefined): EvalRunSummary | null {
  const fromList = runs?.find((r) => r.status === "running") ?? null;
  const run = detail?.run ?? fromList;
  return run?.status === "running" ? run : null;
}

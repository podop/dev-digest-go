import type { EvalRunDetail, EvalRunSummary } from "@devdigest/shared";
import { COMPARE_RUN_COUNT } from "./constants";

/** The run to follow: the one the runs list says is running, else the one this view just started. */
export function followedRunId(runs: readonly EvalRunSummary[] | undefined, startedId: string | undefined): string | undefined {
  return runs?.find((r) => r.status === "running")?.id ?? startedId;
}

/** The run that is currently executing, from the freshest data we have (the polled detail beats the list). */
export function runningRun(detail: EvalRunDetail | undefined, runs: readonly EvalRunSummary[] | undefined): EvalRunSummary | null {
  const fromList = runs?.find((r) => r.status === "running") ?? null;
  const run = detail?.run ?? fromList;
  return run?.status === "running" ? run : null;
}

/** The pair to compare — exactly two selected finished runs, older (A) first — else null. */
export function comparePair(
  runs: readonly EvalRunSummary[] | undefined,
  selectedIds: readonly string[],
): [EvalRunSummary, EvalRunSummary] | null {
  if (selectedIds.length !== COMPARE_RUN_COUNT) return null;
  const chosen = (runs ?? []).filter((r) => selectedIds.includes(r.id) && r.status !== "running");
  const [first, second] = [...chosen].sort((x, y) => Date.parse(x.started_at) - Date.parse(y.started_at));
  return chosen.length === COMPARE_RUN_COUNT && first && second ? [first, second] : null;
}

/** A 0–1 ratio difference as whole percentage points (sign kept); null stays null. */
export function ratioToPoints(delta: number | null | undefined): number | null {
  return delta == null ? null : Math.round(delta * 100);
}

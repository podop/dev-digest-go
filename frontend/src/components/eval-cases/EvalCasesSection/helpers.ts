import type { AgentEvalCase } from "@devdigest/shared";

/** Cases whose last run passed (the "n / m passing" header). */
export function countPassing(cases: readonly AgentEvalCase[]): number {
  return cases.filter((c) => c.last_result?.status === "passed").length;
}

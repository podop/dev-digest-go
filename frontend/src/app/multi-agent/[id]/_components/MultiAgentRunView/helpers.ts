import type { AgentColumn } from "@devdigest/shared";
import { DEFAULT_VIEW, TRACE_PARAM, VIEW_PARAM, type ResultsView } from "./constants";

/** Results page URL for a run with its URL-backed view state; defaults are left out. */
export function resultsPath(id: string, state: { view: ResultsView; traceRunId: string | null }): string {
  const qs = new URLSearchParams();
  if (state.view !== DEFAULT_VIEW) qs.set(VIEW_PARAM, state.view);
  if (state.traceRunId) qs.set(TRACE_PARAM, state.traceRunId);
  const q = qs.toString();
  return `/multi-agent/${encodeURIComponent(id)}${q ? `?${q}` : ""}`;
}

/** Configure run URL with this PR and these agents preselected (deleted agents have no id and are skipped). */
export function configurePath(prId: string, columns: readonly Pick<AgentColumn, "agent_id">[]): string {
  const qs = new URLSearchParams({ pr: prId });
  const agents = columns.map((c) => c.agent_id).filter(Boolean);
  if (agents.length > 0) qs.set("agents", agents.join(","));
  return `/multi-agent/new?${qs.toString()}`;
}

/** The run's PR page, or null when the PR number is unknown. */
export function prPagePath(repoId: string, prNumber: number | null | undefined): string | null {
  return prNumber == null ? null : `/repos/${encodeURIComponent(repoId)}/pulls/${prNumber}`;
}

/** Columns that did not finish with a result — the disagreement block does not compare them. */
export function countNotCompared(columns: readonly Pick<AgentColumn, "status">[]): number {
  return columns.filter((c) => c.status !== "done").length;
}

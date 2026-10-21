/* hooks/multi-agent.ts — React Query hooks for multi-agent review
   (specs/multi-agent-review.md §7): the results read model (polled while a column
   runs), the per-repo list, per-agent estimates and optimistic finding actions. */
"use client";

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import type {
  AgentRunEstimate,
  FindingActionKind,
  FindingRecord,
  MultiAgentRun,
  MultiAgentRunSummary,
} from "@devdigest/shared";
import { api } from "../api";
import { multiAgentKeys, prKeys } from "./keys";

const enc = encodeURIComponent;

/** The results page refreshes the read model at this interval while any column runs. */
const POLL_MS = 3000;

/** GET /multi-agent-runs/:id — columns, disagreement groups and totals. Polls while any column is running. */
export function useMultiAgentRun(id: string | null | undefined) {
  return useQuery({
    queryKey: multiAgentKeys.run(id),
    queryFn: () => api.get<MultiAgentRun>(`/multi-agent-runs/${enc(id ?? "")}`),
    enabled: !!id,
    refetchInterval: (query) =>
      query.state.data?.columns.some((c) => c.status === "running") ? POLL_MS : false,
  });
}

/** GET /repos/:id/multi-agent-runs — the repo's recent multi-agent runs, newest first. */
export function useRepoMultiAgentRuns(repoId: string | null | undefined, limit?: number) {
  return useQuery({
    queryKey: multiAgentKeys.repoRuns(repoId, limit),
    queryFn: () =>
      api.get<MultiAgentRunSummary[]>(
        `/repos/${enc(repoId ?? "")}/multi-agent-runs${limit === undefined ? "" : `?limit=${limit}`}`,
      ),
    enabled: !!repoId,
    refetchInterval: (query) =>
      query.state.data?.some((r) => r.running_count > 0) ? POLL_MS : false,
  });
}

/** GET /runs/estimates — avg time/cost per agent over its last runs; agents without history are absent. */
export function useRunEstimates({ enabled = true }: { enabled?: boolean } = {}) {
  return useQuery({
    queryKey: multiAgentKeys.estimates(),
    queryFn: () => api.get<AgentRunEstimate[]>("/runs/estimates"),
    enabled,
  });
}

export interface MultiAgentFindingActionInput {
  findingId: string;
  action: FindingActionKind;
}

/** Placeholder `memory_id` shown between a Learn click and the server's answer. */
const PENDING_MEMORY_ID = "pending";

/** Stamp the acted-on finding inside a cached multi-agent run (optimistic update). */
export function applyMultiAgentFindingAction(
  run: MultiAgentRun,
  findingId: string,
  action: FindingActionKind,
  at: string,
): MultiAgentRun {
  const stamp = (f: FindingRecord): FindingRecord =>
    action === "accept"
      ? { ...f, accepted_at: at }
      : action === "dismiss"
        ? { ...f, dismissed_at: at }
        : action === "learn"
          ? { ...f, memory_id: f.memory_id ?? PENDING_MEMORY_ID }
          : f;
  return {
    ...run,
    columns: run.columns.map((c) =>
      c.findings.some((f) => f.id === findingId)
        ? { ...c, findings: c.findings.map((f) => (f.id === findingId ? stamp(f) : f)) }
        : c,
    ),
  };
}

/** Accept / dismiss / learn a finding on the results page. The run's cache is updated
   optimistically (rolled back on error — the global MutationCache toasts it) and refetched when settled. */
export function useMultiAgentFindingAction(multiAgentRunId: string) {
  const qc = useQueryClient();
  const key = multiAgentKeys.run(multiAgentRunId);
  return useMutation({
    mutationFn: ({ findingId, action }: MultiAgentFindingActionInput) =>
      api.post<{ finding: FindingRecord; memoryId?: string }>(`/findings/${enc(findingId)}/${action}`),
    onMutate: async ({ findingId, action }) => {
      await qc.cancelQueries({ queryKey: key });
      const previous = qc.getQueryData<MultiAgentRun>(key);
      if (previous) {
        qc.setQueryData<MultiAgentRun>(
          key,
          applyMultiAgentFindingAction(previous, findingId, action, new Date().toISOString()),
        );
      }
      return { previous };
    },
    onError: (_err, _vars, context) => {
      if (context?.previous) qc.setQueryData(key, context.previous);
    },
    onSettled: () => {
      qc.invalidateQueries({ queryKey: key });
      // The same finding is shown on the PR page: refresh its reviews and Smart Diff.
      const prId = qc.getQueryData<MultiAgentRun>(key)?.pr_id;
      if (prId) {
        qc.invalidateQueries({ queryKey: prKeys.reviews(prId) });
        qc.invalidateQueries({ queryKey: prKeys.smartDiff(prId) });
      }
    },
  });
}

/* hooks/ci.ts — React Query hooks of Export to CI (specs/export-to-ci.md): the agent's
   installations, the CI runs list, the wizard's generated-files preview, the "open PR"
   export and the manual Refresh that pulls results from GitHub. Mutations own invalidation. */
"use client";

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import type { CiExport, CiExportInputBody, CiInstallation, CiRefreshResult, CiRun } from "@devdigest/shared";
import { api } from "../api";
import { ciKeys } from "./keys";

/** Newest runs shown on the agent's CI tab (FR1). */
export const CI_TAB_RUNS_LIMIT = 20;

/** What the wizard chooses; the same options drive the preview and the PR. */
export interface CiExportOptions {
  repo: string;
  triggers: readonly string[];
  post_as: NonNullable<CiExportInputBody["post_as"]>;
}

/** Input of the "open PR" export: the options plus the workflow edited in Preview (omitted → generated). */
export interface OpenCiPrInput extends CiExportOptions {
  workflow?: string | null;
}

/** GET /ci/runs — newest first; `agentId` narrows to one agent. */
export function useCiRuns(opts: { agentId?: string | null; limit?: number } = {}) {
  const { agentId, limit } = opts;
  return useQuery({
    queryKey: ciKeys.runs(agentId, limit),
    queryFn: () => {
      const q = new URLSearchParams();
      if (agentId) q.set("agent_id", agentId);
      if (limit) q.set("limit", String(limit));
      const qs = q.toString();
      return api.get<{ runs: CiRun[] }>(`/ci/runs${qs ? `?${qs}` : ""}`);
    },
    select: (data) => data.runs,
  });
}

/** GET /agents/:id/ci-installations. */
export function useCiInstallations(agentId: string | null | undefined) {
  return useQuery({
    queryKey: ciKeys.installations(agentId),
    queryFn: () => api.get<{ installations: CiInstallation[] }>(`/agents/${agentId}/ci-installations`),
    enabled: !!agentId,
    select: (data) => data.installations,
  });
}

/** POST /agents/:id/export-ci with action "files": generates the files, writes nothing and
 *  calls no GitHub. A query (not a mutation) so the wizard gets caching and loading state;
 *  the wizard shows its error inline, and a 422 is not worth a retry. */
export function useCiExportFiles(agentId: string | null | undefined, opts: CiExportOptions | null) {
  return useQuery({
    queryKey: ciKeys.exportFiles(agentId, opts?.repo, opts?.triggers ?? [], opts?.post_as),
    queryFn: () =>
      api.post<CiExport>(`/agents/${agentId}/export-ci`, {
        repo: opts?.repo,
        target: "gha",
        action: "files",
        post_as: opts?.post_as,
        triggers: opts?.triggers,
      }),
    enabled: !!agentId && opts != null,
    retry: false,
  });
}

/** POST /agents/:id/export-ci with action "open_pr". The Install step shows every error
 *  inline (workflow_insecure rule, github_permission_denied scopes), so no global toast. */
export function useOpenCiPr(agentId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (input: OpenCiPrInput) =>
      api.post<CiExport>(`/agents/${agentId}/export-ci`, {
        repo: input.repo,
        target: "gha",
        action: "open_pr",
        post_as: input.post_as,
        triggers: input.triggers,
        ...(input.workflow ? { workflow: input.workflow } : {}),
      }),
    meta: { quietErrorCodes: ["*"] },
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ciKeys.installations(agentId) });
    },
  });
}

/** POST /ci/refresh — pulls new runs from GitHub (one agent or all). The result carries
 *  `degraded` + per-installation errors for the banner; the lists refetch afterwards. */
export function useRefreshCi() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (agentId?: string | null) =>
      api.post<CiRefreshResult>("/ci/refresh", agentId ? { agent_id: agentId } : undefined),
    onSettled: () => {
      qc.invalidateQueries({ queryKey: ciKeys.runsAll() });
      qc.invalidateQueries({ queryKey: ciKeys.installationsAll() });
    },
  });
}

/* hooks/ci.ts — React Query hooks of Export to CI (specs/export-to-ci.md): the agent's
   installations, the CI runs list, the wizard's generated-files preview, the "open PR"
   export and the manual Refresh that pulls results from GitHub. Mutations own invalidation. */
"use client";

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import type {
  CiExport,
  CiExportInputBody,
  CiInstallation,
  CiRefreshResult,
  CiRepoInstallations,
  CiRun,
  CiTarget,
} from "@devdigest/shared";
import { api } from "../api";
import { ciKeys } from "./keys";

/** Newest runs shown on the agent's CI tab (FR1). */
export const CI_TAB_RUNS_LIMIT = 20;

/** What the wizard chooses; the same options drive the preview and the PR. */
export interface CiExportOptions {
  /** "owner/name" (a GitLab `full_name` too); the server ignores it when `repo_id` is sent. */
  repo: string;
  /** UUID of the repository of the workspace; picks the GitLab repository unambiguously (GitLab C FR1). */
  repo_id?: string;
  /** Omitted → "gha" (GitHub bodies are unchanged); a GitLab repository sends "gitlab_ci". */
  target?: CiTarget;
  triggers: readonly string[];
  post_as: NonNullable<CiExportInputBody["post_as"]>;
  /** `runs-on` of the generated job; omitted → the server default (GitHub-hosted). */
  runner?: NonNullable<CiExportInputBody["runner"]>;
}

/** Input of the "open PR" export: the options plus the workflow edited in Preview (omitted → generated). */
export interface OpenCiPrInput extends CiExportOptions {
  workflow?: string | null;
}

/** The repository and target part of an export request body: `repo_id` only when given, `target` defaults to "gha". */
function exportRepoBody(o: { repo: string; repo_id?: string; target?: CiTarget }) {
  return { repo: o.repo, ...(o.repo_id ? { repo_id: o.repo_id } : {}), target: o.target ?? "gha" };
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

/** GET /ci/installations — the workspace's GitHub Action installations per repository, repos in name order. */
export function useWorkspaceCiInstallations() {
  return useQuery({
    queryKey: ciKeys.repoInstallations(),
    queryFn: () => api.get<CiRepoInstallations>("/ci/installations"),
    select: (data) => data.repos,
  });
}

/** POST /agents/:id/export-ci with action "files": generates the files, writes nothing and
 *  calls no GitHub. A query (not a mutation) so the wizard gets caching and loading state;
 *  the wizard shows its error inline, and a 422 is not worth a retry. */
export function useCiExportFiles(agentId: string | null | undefined, opts: CiExportOptions | null) {
  return useQuery({
    // The key's repo slot carries the repository identity and the target: two hosts can share a name.
    queryKey: ciKeys.exportFiles(
      agentId,
      opts ? `${opts.repo_id ?? opts.repo}|${opts.target ?? "gha"}` : undefined,
      opts?.triggers ?? [],
      opts?.post_as,
      opts?.runner,
    ),
    queryFn: () =>
      api.post<CiExport>(`/agents/${agentId}/export-ci`, {
        ...(opts ? exportRepoBody(opts) : {}),
        action: "files",
        post_as: opts?.post_as,
        triggers: opts?.triggers,
        ...(opts?.runner ? { runner: opts.runner } : {}),
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
        ...exportRepoBody(input),
        action: "open_pr",
        post_as: input.post_as,
        triggers: input.triggers,
        ...(input.runner ? { runner: input.runner } : {}),
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

/** What the install dialog sends per agent. */
export interface InstallCiInput {
  agentId: string;
  /** "owner/name". */
  repo: string;
  /** UUID of the repository of the workspace (GitLab C FR1); omitted for GitHub as before. */
  repo_id?: string;
  /** Omitted → "gha"; a GitLab repository sends "gitlab_ci". */
  target?: CiTarget;
  /** `runs-on` of the generated job (GitHub) or the runner tag choice (GitLab); omitted = the server default. */
  runner?: CiExportInputBody["runner"];
}

/** POST /agents/:id/export-ci { action: "open_pr" } for the agent in the variables, so one hook
 *  serves the dialog's one-at-a-time loop. The dialog shows every error per agent, so no global
 *  toast. Both installation lists refetch after each success (the card behind the dialog). */
export function useInstallCi() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ agentId, runner, ...repoRef }: InstallCiInput) =>
      api.post<CiExport>(`/agents/${agentId}/export-ci`, {
        ...exportRepoBody(repoRef),
        action: "open_pr",
        ...(runner ? { runner } : {}),
      }),
    meta: { quietErrorCodes: ["*"] },
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ciKeys.installationsAll() });
      qc.invalidateQueries({ queryKey: ciKeys.repoInstallations() });
    },
  });
}

/* hooks/repo-intel.ts — React Query hooks for the repo-intel (T3) index state.
   Mirrors hooks/context.ts (useIndexStatus/useReindex) but targets the
   repo-intel facade's HTTP surface:
     GET  /repos/:id/index-state  → RepoIndexState (@devdigest/shared)
     POST /repos/:id/resync       → fetch latest from origin + incremental
                                     reindex (202). NOT a destructive re-clone. */
"use client";

import React from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api } from "../api";
import { repoKeys } from "./keys";
import type { RepoIndexState } from "@devdigest/shared";

/** GET /repos/:id/index-state → current repo-intel index state.
    While `poll` is true, refetch on an interval so a running resync's result
    becomes visible. The caller (ProjectContextView) owns when to stop polling
    (the status enum is terminal-only, so completion is detected by watching
    `lastIndexedSha`/`updatedAt` advance, not by status). */
export function useRepoIntelStatus(repoId: string | null | undefined, poll = false) {
  return useQuery({
    queryKey: repoKeys.intelState(repoId),
    queryFn: () => api.get<RepoIndexState>(`/repos/${repoId}/index-state`),
    enabled: !!repoId,
    refetchInterval: poll ? 1500 : false,
  });
}

/** The index state has real content: the API answers a synthesised state with an empty
    `lastIndexedSha` while a repo has no index row (never indexed, A4). */
export function isRepoIndexed(state: RepoIndexState | undefined): boolean {
  return !!state && state.lastIndexedSha !== "";
}

/** The repo was indexed and held no file in a supported language (TypeScript, JavaScript, Go, Java): the
    API answers `reason: "no_files"` with zero files. A never-indexed or failed repo is not this (EC13). */
export function hasNoSourceFiles(state: RepoIndexState | undefined): boolean {
  return isRepoIndexed(state) && state?.reason === "no_files" && state.filesIndexed === 0;
}

/** Waits for a repo's first index state (first-run setup, FR25): refetches GET
    /repos/:id/index-state every `intervalMs` until it is indexed or `maxMs` has passed,
    then stops. `timedOut` flips once that deadline passes without an indexed state. */
export function useRepoIndexWait(repoId: string | null | undefined, intervalMs: number, maxMs: number) {
  const [timedOut, setTimedOut] = React.useState(false);
  React.useEffect(() => {
    if (!repoId) return;
    const id = setTimeout(() => setTimedOut(true), maxMs);
    return () => clearTimeout(id);
  }, [repoId, maxMs]);

  const query = useQuery({
    queryKey: repoKeys.intelState(repoId),
    queryFn: () => api.get<RepoIndexState>(`/repos/${repoId}/index-state`),
    enabled: !!repoId,
    refetchInterval: (q) => (timedOut || isRepoIndexed(q.state.data) ? false : intervalMs),
  });
  const indexed = isRepoIndexed(query.data);
  return { state: query.data, indexed, timedOut: timedOut && !indexed };
}

/** POST /repos/:id/resync → fetch latest + incremental reindex (resync, not re-clone). */
export function useResyncRepoIntel(repoId: string | null | undefined) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: () => api.post<{ status: string }>(`/repos/${repoId}/resync`),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: repoKeys.intelState(repoId) });
    },
  });
}

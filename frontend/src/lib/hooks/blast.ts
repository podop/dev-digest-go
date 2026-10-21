/* hooks/blast.ts — server/specs/07-blast-radius.md: GET /pulls/:id/blast (an index
   read, no model call). The resync mutation re-uses the repo-intel endpoint and
   invalidates the blast read + the index state. */
"use client";

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import type { BlastRadius, PrHistory } from "@devdigest/shared";
import { api } from "../api";
import { prKeys, repoKeys } from "./keys";

/** GET /pulls/:id/blast. */
export function useBlastRadius(prId: string | null | undefined) {
  return useQuery({
    queryKey: prKeys.blast(prId),
    queryFn: () => api.get<BlastRadius>(`/pulls/${prId}/blast`),
    enabled: !!prId,
  });
}

/** How long a PR's prior-PR list stays fresh: it comes from GitHub (rate limits) and rarely changes. */
const HISTORY_STALE_MS = 5 * 60 * 1000;

/** GET /pulls/:id/history — prior merged PRs touching the same files (the server answers [] when GitHub is unavailable). */
export function usePrHistory(prId: string | null | undefined) {
  return useQuery({
    queryKey: prKeys.history(prId),
    queryFn: () => api.get<PrHistory>(`/pulls/${prId}/history`),
    enabled: !!prId,
    staleTime: HISTORY_STALE_MS,
  });
}

/** POST /repos/:id/resync (202 job, fetch latest + incremental reindex) → refetch the blast read and the index state.
    No polling: the job finishes later; the user reloads the card to see the new result. */
export function useResyncBlast(repoId: string | null | undefined, prId: string | null | undefined) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: () => api.post<{ status: string }>(`/repos/${repoId}/resync`),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: prKeys.blast(prId) });
      qc.invalidateQueries({ queryKey: repoKeys.intelState(repoId) });
    },
  });
}

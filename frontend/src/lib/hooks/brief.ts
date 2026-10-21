/* hooks/brief.ts — React Query hooks for the PR Brief (specs/2026-10-01-pr-brief.md §7):
   the stored brief of a PR and its generate/refresh action. */
"use client";

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import type { PrBriefResponse } from "@devdigest/shared";
import { api } from "../api";
import { prKeys } from "./keys";

const enc = encodeURIComponent;

/** GET /pulls/:id/brief — `{ brief: null, stale: false }` or the stored brief with its derived stale flag. Never calls a model. */
export function usePrBrief(prId: string | null | undefined) {
  return useQuery({
    queryKey: prKeys.brief(prId),
    queryFn: () => api.get<PrBriefResponse>(`/pulls/${enc(prId ?? "")}/brief`),
    enabled: !!prId,
  });
}

/** POST /pulls/:id/brief (empty body) — one LLM call, 200 = the fresh brief. The card reports
 *  404/409/422/502 itself (it reads `error` off the mutation), so the global error toast stays quiet.
 *  The cache write is hook-level so it still lands when the card unmounted during the call. */
export function useGenerateBrief(prId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: () => api.post<PrBriefResponse>(`/pulls/${enc(prId)}/brief`),
    meta: { quietErrorCodes: ["*"] },
    onSuccess: (state) => {
      qc.setQueryData<PrBriefResponse>(prKeys.brief(prId), state);
    },
  });
}

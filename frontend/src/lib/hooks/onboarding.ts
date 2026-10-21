/* hooks/onboarding.ts — React Query hooks for the Onboarding Tour
   (specs/2026-10-01-onboarding-generator.md §7): the stored tour of a repo and
   its generate/regenerate action. */
"use client";

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import type { OnboardingTourState } from "@devdigest/shared";
import { api } from "../api";
import { repoKeys } from "./keys";

const enc = encodeURIComponent;

/** GET /repos/:id/onboarding — `{ status: 'none' }` or the stored tour with its derived stale flag. */
export function useOnboardingTour(repoId: string | null | undefined) {
  return useQuery({
    queryKey: repoKeys.onboarding(repoId),
    queryFn: () => api.get<OnboardingTourState>(`/repos/${enc(repoId ?? "")}/onboarding`),
    enabled: !!repoId,
  });
}

/** POST /repos/:id/onboarding (empty body) — one LLM call, 200 = the fresh tour. The screen
 *  reports 409/422/502 itself (per-call `mutate` callbacks), so the global error toast stays quiet. */
export function useGenerateOnboardingTour(repoId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: () => api.post<OnboardingTourState>(`/repos/${enc(repoId)}/onboarding`),
    meta: { quietErrorCodes: ["*"] },
    onSuccess: (state) => {
      qc.setQueryData<OnboardingTourState>(repoKeys.onboarding(repoId), state);
    },
  });
}

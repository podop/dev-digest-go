/* hooks/core.ts — typed React Query hooks over the F1 API (contracts):
   settings, secrets, repos and pulls. Scaffolding screens use
   these; feature-domain hooks live in the sibling files (agents/reviews/trace/…)
   and are re-exported alongside these from hooks/index.ts. */
"use client";

import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "../api";
import { aboutKeys, providerKeys, repoKeys, prKeys, settingsKeys } from "./keys";
import type {
  Settings,
  SettingsUpdate,
  ConnTestProvider,
  ConnTestResult,
  SecretsStatus,
  SpendStatus,
  AutomaticReviewsStatus,
  About,
  Repo,
  PrMeta,
  PrDetail,
} from "../types";

// ---- Settings (F1: GET/PUT /settings, POST /settings/test-connection) ----
export function useSettings() {
  return useQuery({
    queryKey: settingsKeys.current(),
    queryFn: () => api.get<Settings>("/settings"),
  });
}

export function useUpdateSettings() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (patch: SettingsUpdate) => api.put<Settings>("/settings", patch),
    // The saved cap is part of GET /settings/spend, so that read is refetched too.
    onSuccess: (data) => {
      qc.setQueryData(settingsKeys.current(), data);
      qc.invalidateQueries({ queryKey: settingsKeys.spend() });
      // A save can change the automatic-reviews state (toggle, agents, triggers, interval).
      qc.invalidateQueries({ queryKey: settingsKeys.autoReviewsStatus() });
    },
  });
}

export function useTestConnection() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (input: ConnTestProvider | { provider: ConnTestProvider; key?: string }) => {
      const body = typeof input === "string" ? { provider: input } : input;
      return api.post<ConnTestResult>("/settings/test-connection", body);
    },
    // Saving/validating a provider key can change which models resolve — drop the
    // cached (possibly empty) model lists so the agent picker refetches, and
    // refresh the "Configured / Not set" key-status badges.
    onSuccess: (res) => {
      if (res.ok) {
        qc.invalidateQueries({ queryKey: providerKeys.all });
        qc.invalidateQueries({ queryKey: settingsKeys.secretsStatus() });
      }
    },
  });
}

/** Which provider keys are configured (booleans only — never the values). */
export function useSecretsStatus() {
  return useQuery({
    queryKey: settingsKeys.secretsStatus(),
    queryFn: () => api.get<SecretsStatus>("/settings/secrets-status"),
    staleTime: 30_000,
  });
}

/** GET /settings/spend — month-to-date spend, the cap and whether it is reached. */
export function useSpend() {
  return useQuery({
    queryKey: settingsKeys.spend(),
    queryFn: () => api.get<SpendStatus>("/settings/spend"),
  });
}

/** Status line refresh period while a consumer is mounted (spec FR17). */
export const AUTO_REVIEWS_STATUS_REFETCH_MS = 30_000;

/** GET /settings/automatic-reviews-status — refetched every 30 s while mounted and after every save. */
export function useAutomaticReviewsStatus() {
  return useQuery({
    queryKey: settingsKeys.autoReviewsStatus(),
    queryFn: () => api.get<AutomaticReviewsStatus>("/settings/automatic-reviews-status"),
    refetchInterval: AUTO_REVIEWS_STATUS_REFETCH_MS,
  });
}

/** GET /about — version, build id, runner, config format, data dir and secrets file. */
export function useAbout() {
  return useQuery({
    queryKey: aboutKeys.all,
    queryFn: () => api.get<About>("/about"),
    staleTime: Infinity,
  });
}

// ---- Repos (F1: GET/POST /repos, refresh, delete) ----
export function useRepos() {
  return useQuery({
    queryKey: repoKeys.list(),
    queryFn: () => api.get<Repo[]>("/repos"),
  });
}

export function useAddRepo() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (url: string) => api.post<Repo>("/repos", { url }),
    onSuccess: () => qc.invalidateQueries({ queryKey: repoKeys.list() }),
  });
}

export function useRefreshRepo() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (repoId: string) => api.post<Repo>(`/repos/${repoId}/refresh`),
    onSuccess: (_d, repoId) => {
      qc.invalidateQueries({ queryKey: repoKeys.list() });
      qc.invalidateQueries({ queryKey: repoKeys.pulls(repoId) });
    },
  });
}

export function useDeleteRepo() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (repoId: string) => api.del<{ deleted: string }>(`/repos/${repoId}`),
    onSuccess: () => qc.invalidateQueries({ queryKey: repoKeys.list() }),
  });
}

// ---- Pull requests (F1: GET /repos/:id/pulls, GET /pulls/:id) ----
export function usePulls(repoId: string | null | undefined) {
  return useQuery({
    queryKey: repoKeys.pulls(repoId),
    queryFn: () => api.get<PrMeta[]>(`/repos/${repoId}/pulls`),
    enabled: !!repoId,
    // Auto-refresh PR statuses: re-sync from GitHub every 60s while the page is
    // open, and whenever the window regains focus.
    refetchInterval: 60_000,
    refetchOnWindowFocus: true,
  });
}

export function usePullDetail(prId: string | number | null | undefined) {
  return useQuery({
    queryKey: prKeys.detail(prId),
    queryFn: () => api.get<PrDetail>(`/pulls/${prId}`),
    enabled: prId != null,
  });
}

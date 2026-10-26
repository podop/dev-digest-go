/* hooks/custom-providers.ts — React Query hooks of Settings › API Keys › Custom providers
   (specs/2026-10-24-custom-model-providers.md §7): GET/POST /settings/custom-providers and
   PUT/DELETE /settings/custom-providers/:id. Secret values only go out (write-only); responses carry
   `key_configured` / `value_configured`. Components use these only; keys come from `customProviderKeys`. */
"use client";

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api } from "../api";
import { CUSTOM_PROVIDER_ERROR_CODES } from "../providers/custom";
import type { CustomProvider, CustomProviderDeleted, CustomProviderInput, CustomProviderUpdate } from "../types";
import { agentKeys, customProviderKeys, providerKeys, settingsKeys } from "./keys";

const BASE = "/settings/custom-providers";

/** GET /settings/custom-providers — sorted by name then id by the server. */
export function useCustomProviders() {
  return useQuery({
    queryKey: customProviderKeys.list(),
    queryFn: () => api.get<CustomProvider[]>(BASE),
  });
}

/** What `useSaveCustomProvider` sends: POST for `create`, PUT `/:id` for `update` (the id never changes). */
export type SaveCustomProviderVars =
  | { mode: "create"; input: CustomProviderInput }
  | { mode: "update"; id: string; input: CustomProviderUpdate };

/**
 * Create or replace a custom provider. The dialog shows field errors (`validation_error`), the limit and the
 * duplicate id inline, so those codes are quiet; anything else (network, 404, 500) still toasts.
 * Refetches the list and every model list (a changed gateway or manual model list changes the pickers).
 */
export function useSaveCustomProvider() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (vars: SaveCustomProviderVars) =>
      vars.mode === "create"
        ? api.post<CustomProvider>(BASE, vars.input)
        : api.put<CustomProvider>(`${BASE}/${encodeURIComponent(vars.id)}`, vars.input),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: customProviderKeys.all });
      qc.invalidateQueries({ queryKey: providerKeys.all });
    },
    meta: {
      quietErrorCodes: [
        "validation_error",
        CUSTOM_PROVIDER_ERROR_CODES.limit,
        CUSTOM_PROVIDER_ERROR_CODES.exists,
      ],
    },
  });
}

/**
 * DELETE /settings/custom-providers/:id (the id is the full `custom:<slug>`). A 409 `provider_in_use` is shown by the
 * dialog (its `details` list the users), so it is quiet. On success the provider's model list is dropped and the
 * agent and settings reads are refetched (best effort: nothing references a deleted provider, but a stale
 * "Not configured" must not linger).
 */
export function useDeleteCustomProvider() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => api.del<CustomProviderDeleted>(`${BASE}/${encodeURIComponent(id)}`),
    onSuccess: (_d, id) => {
      qc.invalidateQueries({ queryKey: customProviderKeys.all });
      qc.removeQueries({ queryKey: providerKeys.models(id) });
      qc.invalidateQueries({ queryKey: agentKeys.list() });
      qc.invalidateQueries({ queryKey: settingsKeys.current() });
    },
    meta: { quietErrorCodes: [CUSTOM_PROVIDER_ERROR_CODES.inUse] },
  });
}

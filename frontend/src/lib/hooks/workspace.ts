/* hooks/workspace.ts — React Query hooks of Settings › Workspace (specs/2026-10-23-settings-b-workspace.md §7):
   GET/PUT /workspace, GET /workspace/stats, GET /workspace/export (a file download) and
   POST /workspace/reset-history. Components use these only; query keys come from `workspaceKeys`. */
"use client";

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import type {
  WorkspaceInfo,
  WorkspaceRenameRequest,
  WorkspaceResetRequest,
  WorkspaceResetResult,
  WorkspaceStats,
} from "@devdigest/shared";
import { WORKSPACE_RESET_ACTIVE_CODE, WORKSPACE_RESET_MISMATCH_CODE } from "@devdigest/shared/constants/workspace";
import { api } from "../api";
import { saveBlob } from "../download";
import {
  ciKeys,
  evalKeys,
  leaderboardKeys,
  multiAgentKeys,
  performanceKeys,
  prKeys,
  repoKeys,
  runKeys,
  settingsKeys,
  workspaceKeys,
} from "./keys";

/** File name used when the server's `Content-Disposition` cannot be read. */
export const WORKSPACE_EXPORT_FALLBACK_FILE_NAME = "devdigest-workspace.json";

/** GET /workspace — id, display name, data dir, clone dir and repositories. */
export function useWorkspace() {
  return useQuery({
    queryKey: workspaceKeys.info(),
    queryFn: () => api.get<WorkspaceInfo>("/workspace"),
  });
}

/** PUT /workspace {name} — the answer replaces the cached workspace (the switcher reads it too). */
export function useRenameWorkspace() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (name: WorkspaceRenameRequest["name"]) => api.put<WorkspaceInfo>("/workspace", { name }),
    onSuccess: (info) => qc.setQueryData(workspaceKeys.info(), info),
    // The name field shows a rejected name inline; other failures (network, 500) still toast.
    meta: { quietErrorCodes: ["validation_error"] },
  });
}

/** GET /workspace/stats — the six counts of the stat tiles. */
export function useWorkspaceStats() {
  return useQuery({
    queryKey: workspaceKeys.stats(),
    queryFn: () => api.get<WorkspaceStats>("/workspace/stats"),
  });
}

/** GET /workspace/export — downloads the bundle under the server's file name. Errors toast. */
export function useExportWorkspace() {
  return useMutation({
    mutationFn: async () => {
      const { blob, fileName } = await api.download("/workspace/export");
      const name = fileName ?? WORKSPACE_EXPORT_FALLBACK_FILE_NAME;
      saveBlob(blob, name);
      return { fileName: name, bytes: blob.size };
    },
  });
}

/** POST /workspace/reset-history {confirm} — a 200 refetches everything the deleted rows fed. */
export function useResetHistory() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (confirm: WorkspaceResetRequest["confirm"]) =>
      api.post<WorkspaceResetResult>("/workspace/reset-history", { confirm }),
    onSuccess: () => {
      for (const queryKey of [
        workspaceKeys.all,
        prKeys.all,
        repoKeys.all,
        runKeys.all,
        leaderboardKeys.all,
        performanceKeys.all,
        evalKeys.all,
        multiAgentKeys.all,
        ciKeys.all,
        settingsKeys.spend(),
      ]) {
        qc.invalidateQueries({ queryKey });
      }
    },
    // The dialog shows these refusals itself; any other failure (500, network) still toasts.
    meta: { quietErrorCodes: [WORKSPACE_RESET_MISMATCH_CODE, WORKSPACE_RESET_ACTIVE_CODE, "validation_error"] },
  });
}

/* hooks/plugins — React Query hooks of Settings › Integrations plugins
   (specs/2026-10-23-settings-integrations.md §7): the installed list with the last export, the
   export download, the import preview and install (a raw upload), enable/disable and remove.
   Mutations own their invalidation; a plugin switch changes the agent and skill lists (the badge). */
"use client";

import { useMutation, useQuery, useQueryClient, type QueryClient } from "@tanstack/react-query";
import type {
  InstalledPlugin,
  PluginEnabledRequest,
  PluginExportRequest,
  PluginImportPreview,
  PluginImportResult,
  PluginList,
  PluginRemoveRequest,
  PluginRemoveResult,
} from "@devdigest/shared";
import {
  PLUGIN_REMOVE_MISMATCH_CODE,
  pluginUploadKind,
  pluginUploadMediaType,
} from "@devdigest/shared/constants/plugins";
import { api } from "../../api";
import { saveBlob } from "../../download";
import { agentKeys, ciKeys, evalKeys, pluginKeys, skillKeys, workspaceKeys } from "../keys";

/** File name used when the server's `Content-Disposition` cannot be read. */
export const PLUGIN_EXPORT_FALLBACK_FILE_NAME = "devdigest-plugin.zip";

/** The import card shows EVERY preview/install failure inline (broken bundle, conflict, 413/415, 5xx
 *  "Import failed — try again"), so no global toast. */
export const PLUGIN_IMPORT_QUIET_CODES = ["*"] as const;

/** The export dialog shows EVERY failure inline (4xx message, 5xx/network "Export failed — try again"),
 *  so no global toast. */
const PLUGIN_EXPORT_QUIET_CODES = ["*"] as const;

/** The file the user picked in the import card. */
export interface PluginImportInput {
  file: File;
}

/** GET /plugins — installed plugins (newest first) and the last export. */
export function usePlugins() {
  return useQuery({
    queryKey: pluginKeys.list(),
    queryFn: () => api.get<PluginList>("/plugins"),
  });
}

/** POST /plugins/export — downloads the zip under the server's file name; the list refetches for "Last export". */
export function useExportPlugin() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (request: PluginExportRequest) => {
      const { blob, fileName } = await api.download("/plugins/export", {
        method: "POST",
        body: JSON.stringify(request),
      });
      const name = fileName ?? PLUGIN_EXPORT_FALLBACK_FILE_NAME;
      saveBlob(blob, name);
      return { fileName: name, bytes: blob.size };
    },
    // A failed export wrote nothing, so the list is only worth refetching after a success.
    onSuccess: () => qc.invalidateQueries({ queryKey: pluginKeys.list() }),
    meta: { quietErrorCodes: PLUGIN_EXPORT_QUIET_CODES },
  });
}

/** `POST /plugins/import?dry_run=…&filename=…` with the file as the raw body. */
function importPlugin<T>({ file }: PluginImportInput, dryRun: boolean): Promise<T> {
  const kind = pluginUploadKind(file.name);
  // The card refuses other files before it calls; this keeps a direct caller from sending one.
  if (kind === null) return Promise.reject(new Error(`Not a .zip or .json file: ${file.name}`));
  const query = new URLSearchParams({ dry_run: String(dryRun), filename: file.name });
  return api.upload<T>(`/plugins/import?${query.toString()}`, file, pluginUploadMediaType(kind));
}

/** Dry run: validates the upload and returns the preview; nothing is stored. */
export function usePreviewPlugin() {
  return useMutation({
    mutationFn: (input: PluginImportInput) => importPlugin<PluginImportPreview>(input, true),
    meta: { quietErrorCodes: PLUGIN_IMPORT_QUIET_CODES },
  });
}

/** Refetch what a plugin install, switch or removal changes: the plugin list, the agent and skill
 *  lists (the badge, the pickers) and the workspace counts. */
function invalidatePluginOwned(qc: QueryClient) {
  qc.invalidateQueries({ queryKey: pluginKeys.all });
  qc.invalidateQueries({ queryKey: agentKeys.all });
  qc.invalidateQueries({ queryKey: skillKeys.all });
  qc.invalidateQueries({ queryKey: workspaceKeys.stats() });
}

/** Install: the plugin arrives disabled with its agents, skills, eval cases and learnings. */
export function useInstallPlugin() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (input: PluginImportInput) => importPlugin<PluginImportResult>(input, false),
    onSuccess: () => invalidatePluginOwned(qc),
    meta: { quietErrorCodes: PLUGIN_IMPORT_QUIET_CODES },
  });
}

export interface SetPluginEnabledInput extends PluginEnabledRequest {
  id: string;
}

/** PATCH /plugins/:id {enabled}. A failure toasts and the row keeps its state (nothing is written optimistically). */
export function useSetPluginEnabled() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ id, enabled }: SetPluginEnabledInput) =>
      api.patch<InstalledPlugin>(`/plugins/${id}`, { enabled }),
    onSuccess: () => invalidatePluginOwned(qc),
  });
}

export interface RemovePluginInput extends PluginRemoveRequest {
  id: string;
}

/** DELETE /plugins/:id {confirm} — also deletes the plugin's agents, skills, eval cases, learnings and CI installations. */
export function useRemovePlugin() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ id, confirm }: RemovePluginInput) =>
      api.del<PluginRemoveResult>(`/plugins/${id}`, { confirm }),
    onSuccess: () => {
      invalidatePluginOwned(qc);
      qc.invalidateQueries({ queryKey: evalKeys.all });
      qc.invalidateQueries({ queryKey: ciKeys.all });
    },
    // The dialog shows a mismatched name itself; any other failure (404, 5xx, network) still toasts.
    meta: { quietErrorCodes: [PLUGIN_REMOVE_MISMATCH_CODE] },
  });
}

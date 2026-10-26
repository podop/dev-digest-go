/* hooks/gitlab-connections.ts — React Query hooks of Settings › GitLab Integration
   (specs/2026-10-24-gitlab-a-connection-sync.md §7): GET/POST /settings/gitlab-connections and
   PUT/DELETE /settings/gitlab-connections/:id. The token only goes out (write-only); responses carry
   `token_configured`. A connection test is `useTestConnection({ provider: "gitlab", connection_id, key? })`.
   Components use these only; keys come from `gitlabConnectionKeys`. */
"use client";

import { useMutation, useQuery, useQueryClient, type QueryClient } from "@tanstack/react-query";
import { api } from "../api";
import { GITLAB_CONNECTION_SAVE_QUIET_CODES, GITLAB_ERROR_CODES } from "../code-host";
import type { GitLabConnection, GitLabConnectionDeleted, GitLabConnectionInput, GitLabConnectionUpdate } from "../types";
import { gitlabConnectionKeys, repoKeys, settingsKeys } from "./keys";

const BASE = "/settings/gitlab-connections";

/** GET /settings/gitlab-connections — sorted by name then host by the server. */
export function useGitLabConnections() {
  return useQuery({
    queryKey: gitlabConnectionKeys.list(),
    queryFn: () => api.get<GitLabConnection[]>(BASE),
  });
}

/** What `useSaveGitLabConnection` sends: POST for `create`, PUT `/:id` for `update` (the id never changes). */
export type SaveGitLabConnectionVars =
  | { mode: "create"; input: GitLabConnectionInput }
  | { mode: "update"; id: string; input: GitLabConnectionUpdate };

/** A connection change re-binds repositories (web_url, connection_id), changes `repo_count` and the automatic-reviews state. */
function invalidateAfterChange(qc: QueryClient) {
  qc.invalidateQueries({ queryKey: gitlabConnectionKeys.all });
  qc.invalidateQueries({ queryKey: repoKeys.all });
  qc.invalidateQueries({ queryKey: settingsKeys.autoReviewsStatus() });
}

/**
 * Create or update a connection. The dialog shows field errors (`validation_error`, including a changed host), the
 * limit and the duplicate host inline, so those codes are quiet; anything else (network, 404, 500) still toasts.
 * Refetches the connection list, the repositories and the automatic-reviews status.
 */
export function useSaveGitLabConnection() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (vars: SaveGitLabConnectionVars) =>
      vars.mode === "create"
        ? api.post<GitLabConnection>(BASE, vars.input)
        : api.put<GitLabConnection>(`${BASE}/${encodeURIComponent(vars.id)}`, vars.input),
    onSuccess: () => invalidateAfterChange(qc),
    meta: { quietErrorCodes: GITLAB_CONNECTION_SAVE_QUIET_CODES },
  });
}

/**
 * DELETE /settings/gitlab-connections/:id — always allowed; the connection's repositories become "not connected"
 * (`disconnected_repos` says how many). A 404 (already deleted elsewhere) is shown by the confirm dialog, so it is
 * quiet; anything else still toasts. Refetches the connection list, the repositories and the automatic-reviews status.
 */
export function useDeleteGitLabConnection() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => api.del<GitLabConnectionDeleted>(`${BASE}/${encodeURIComponent(id)}`),
    onSuccess: () => invalidateAfterChange(qc),
    meta: { quietErrorCodes: [GITLAB_ERROR_CODES.connectionNotFound] },
  });
}

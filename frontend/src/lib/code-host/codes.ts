/* code-host/codes.ts — `error.code` values of the GitLab routes and of the repo routes that talk to a code host
   (specs/2026-10-24-gitlab-a-connection-sync.md §7). Zod-free; import via "@/lib/code-host". */

export const GITLAB_ERROR_CODES = {
  /** 409 on POST/PUT of a connection for a host key that already has one. */
  connectionExists: "gitlab_connection_exists",
  /** 404 on PUT/DELETE/test of an unknown connection id. */
  connectionNotFound: "gitlab_connection_not_found",
  /** 422 on the 21st connection. */
  connectionLimit: "gitlab_connection_limit",
  /** 422 on add-repo for a host without a connection, and on poll of a not-connected repository. */
  hostNotConnected: "repo_host_not_connected",
  /** 422 on add-repo: the project does not exist or the token cannot see it, or it has moved. */
  projectNotFound: "gitlab_project_not_found",
  /** 422 on add-repo: GitLab rejected the connection's token. */
  permissionDenied: "gitlab_permission_denied",
  /** 409 on add-repo: another repository holds the same path with a different project id; `details` {repo_id}. */
  pathConflict: "gitlab_path_conflict",
  /** 502 on add-repo: GitLab answered 429. */
  rateLimited: "gitlab_rate_limited",
  /** 502 on add-repo (and 400 on posting a comment): GitLab is unavailable or the feature is not there yet. */
  unavailable: "gitlab_unavailable",
  /** 422 on CI export of a GitLab repository. */
  unsupportedHost: "unsupported_host",
} as const;

/** Codes the connection dialog shows inline on save (field errors, limit, duplicate host), so its mutation stays quiet. */
export const GITLAB_CONNECTION_SAVE_QUIET_CODES = [
  "validation_error",
  GITLAB_ERROR_CODES.connectionExists,
  GITLAB_ERROR_CODES.connectionLimit,
] as const;

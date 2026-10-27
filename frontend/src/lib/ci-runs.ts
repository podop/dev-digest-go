import type { CiRefreshResult, CiRun } from "@devdigest/shared";
import { isGitLab, prRef, prUrl, repoLabel } from "./code-host";

const PLACEHOLDER = "—";
const REPO_FULL_NAME = /^[A-Za-z0-9_.-]+\/[A-Za-z0-9_.-]+$/;

/** A run's time as the viewer's local date + time; "—" when missing or not a date. */
export function formatRanAt(iso: string | null | undefined): string {
  if (!iso) return PLACEHOLDER;
  const d = new Date(iso);
  return Number.isNaN(d.getTime()) ? PLACEHOLDER : d.toLocaleString();
}

/**
 * Page of a run's PR/MR. GitHub: built from the `owner/name` repo (null for any other name). GitLab: the server's
 * `pr_url` when it is an http(s) URL — null for a host that is not connected (the server sends null, EC34), never
 * a link built here from a `full_name` that may have more than two path segments.
 */
export function pullRequestHref(run: Pick<CiRun, "repo" | "pr_number" | "provider" | "pr_url">): string | null {
  if (run.pr_number == null) return null;
  if (isGitLab(run)) return safeRunHref(run.pr_url);
  if (!run.repo || !REPO_FULL_NAME.test(run.repo)) return null;
  return prUrl({ provider: "github", full_name: run.repo }, run.pr_number);
}

/** `#<n>` for a GitHub run, `!<n>` for a GitLab one; null when the run has no PR/MR. */
export function pullRequestRef(run: Pick<CiRun, "pr_number" | "provider">): string | null {
  return run.pr_number == null ? null : prRef(run, run.pr_number);
}

/** The run's repository as text: GitLab adds its host ("acme/platform/api · git.example.com"), GitHub is as is. */
export function runRepoLabel(run: Pick<CiRun, "repo" | "provider" | "host">): string | null {
  if (!run.repo) return null;
  return repoLabel({ provider: run.provider, host: run.host, full_name: run.repo });
}

/** The run link only when it is an http(s) URL — it comes from GitHub data, never render other schemes. */
export function safeRunHref(url: string | null | undefined): string | null {
  if (!url) return null;
  try {
    const { protocol } = new URL(url);
    return protocol === "https:" || protocol === "http:" ? url : null;
  } catch {
    return null;
  }
}

/** Translation key (under the `status` namespace of the runs copy) of a run status; null for an unknown one. */
export function statusKey(status: string | null | undefined): "succeeded" | "noFindings" | "failed" | "running" | null {
  switch (status) {
    case "succeeded":
      return "succeeded";
    case "no_findings":
      return "noFindings";
    case "failed":
      return "failed";
    case "running":
      return "running";
    default:
      return null;
  }
}

export interface RefreshFailure {
  installationId: string;
  /** Repository name, followed by its host for a GitLab one. */
  repo: string;
  /** The error code, or the state (`not_checked`) when the repo was never reached. */
  code: string;
}

const GITHUB_HOST = "github.com";

/** Refresh error codes that have their own copy (`runs.ciRuns.refreshError.<code>`); others show the raw code. */
export const GITLAB_REFRESH_ERROR_CODES = [
  "gitlab_permission_denied",
  "gitlab_rate_limited",
  "gitlab_unavailable",
  "repo_host_not_connected",
  "gitlab_token_missing",
] as const;
export type GitLabRefreshErrorCode = (typeof GITLAB_REFRESH_ERROR_CODES)[number];

/** The code when it has GitLab copy, else null. */
export function gitlabRefreshErrorCode(code: string): GitLabRefreshErrorCode | null {
  return (GITLAB_REFRESH_ERROR_CODES as readonly string[]).includes(code) ? (code as GitLabRefreshErrorCode) : null;
}

/** Repositories a refresh could not read: host errors and the ones the time budget skipped. */
export function refreshFailures(result: CiRefreshResult | undefined): RefreshFailure[] {
  if (!result) return [];
  return result.installations
    .filter((i) => i.state === "error" || i.state === "not_checked")
    .map((i) => ({
      installationId: i.installation_id,
      repo: i.host && i.host !== GITHUB_HOST ? `${i.repo} · ${i.host}` : i.repo,
      code: i.error_code ?? i.state,
    }));
}

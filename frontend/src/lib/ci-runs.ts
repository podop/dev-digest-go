import type { CiRefreshResult, CiRun } from "@devdigest/shared";
import { prUrl } from "./code-host";

const PLACEHOLDER = "—";
const REPO_FULL_NAME = /^[A-Za-z0-9_.-]+\/[A-Za-z0-9_.-]+$/;

/** A run's time as the viewer's local date + time; "—" when missing or not a date. */
export function formatRanAt(iso: string | null | undefined): string {
  if (!iso) return PLACEHOLDER;
  const d = new Date(iso);
  return Number.isNaN(d.getTime()) ? PLACEHOLDER : d.toLocaleString();
}

/** GitHub PR page of a run; null when the run has no PR or the repo name is not `owner/name`. */
export function pullRequestHref(run: Pick<CiRun, "repo" | "pr_number">): string | null {
  if (run.pr_number == null || !run.repo || !REPO_FULL_NAME.test(run.repo)) return null;
  return prUrl({ provider: "github", full_name: run.repo }, run.pr_number);
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
  repo: string;
  /** The error code, or the state (`not_checked`) when the repo was never reached. */
  code: string;
}

/** Repositories a refresh could not read: GitHub errors and the ones the time budget skipped. */
export function refreshFailures(result: CiRefreshResult | undefined): RefreshFailure[] {
  if (!result) return [];
  return result.installations
    .filter((i) => i.state === "error" || i.state === "not_checked")
    .map((i) => ({ repo: i.repo, code: i.error_code ?? i.state }));
}

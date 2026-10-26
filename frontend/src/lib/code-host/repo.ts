/* code-host/repo.ts — host-aware helpers over a repository (GitLab A, spec FR41–FR44, FR49). Pure: no React, no
   fetching, no zod. Every outbound link is built from the repository's `provider` and `web_url` only — never from
   a URL in a GitHub or GitLab response (FR41). Import as "@/lib/code-host". */

export type CodeHostProvider = "github" | "gitlab";

/** What the helpers read; a `Repo`, a `LeaderboardRepo` and a `WorkspaceRepo`-like object all fit. */
export interface CodeHostRepo {
  provider?: CodeHostProvider | null;
  host?: string | null;
  full_name?: string | null;
  web_url?: string | null;
}

/** The fields of a repository that decide the not-connected state (FR49). */
export interface ConnectableRepo {
  provider?: CodeHostProvider | null;
  /** The bound connection; null for GitHub and for a not-connected GitLab repository. */
  connection_id?: string | null;
}

const GITHUB_WEB = "https://github.com";

/** Whether the repository lives on a GitLab instance. A missing repository or provider reads as GitHub. */
export function isGitLab(repo: Pick<CodeHostRepo, "provider"> | null | undefined): boolean {
  return repo?.provider === "gitlab";
}

/** A GitLab repository without a connection for its host (FR48, FR49); a GitHub repository is never "not connected". */
export function isNotConnected(repo: ConnectableRepo | null | undefined): boolean {
  return repo?.provider === "gitlab" && repo.connection_id == null;
}

/** "GitHub" or "GitLab" — for host-specific copy such as "View on GitLab" (FR42). */
export function codeHostName(repo: Pick<CodeHostRepo, "provider"> | null | undefined): "GitHub" | "GitLab" {
  return isGitLab(repo) ? "GitLab" : "GitHub";
}

/** `!<n>` for a GitLab merge request, `#<n>` for a GitHub pull request (FR43). */
export function prRef(repo: Pick<CodeHostRepo, "provider"> | null | undefined, number: number | string): string {
  return `${isGitLab(repo) ? "!" : "#"}${number}`;
}

/** The repository name with its host for GitLab ("acme/platform/payments · gitlab.com"), as is for GitHub (FR44). */
export function repoLabel(repo: Pick<CodeHostRepo, "provider" | "host" | "full_name">): string {
  const name = repo.full_name ?? "";
  return isGitLab(repo) && repo.host ? `${name} · ${repo.host}` : name;
}

/**
 * The repository's web root without a trailing slash: its `web_url` when that is an `https:` URL (a `javascript:`
 * or `data:` value never becomes a link), else — for GitHub only — `https://github.com/<full_name>`. `null` when
 * there is no usable root, e.g. a GitLab repository whose `web_url` is empty.
 */
export function webRoot(repo: CodeHostRepo | null | undefined): string | null {
  if (!repo) return null;
  const raw = repo.web_url?.trim();
  if (raw) {
    try {
      const url = new URL(raw);
      if (url.protocol !== "https:") return null;
      return raw.replace(/\/+$/, "");
    } catch {
      return null;
    }
  }
  return !isGitLab(repo) && repo.full_name ? `${GITHUB_WEB}/${repo.full_name}` : null;
}

/** Encode a repo-relative path for a URL while keeping "/" separators. */
function encPath(file: string): string {
  return file.split("/").map(encodeURIComponent).join("/");
}

/** GitHub `<web_url>/pull/<n>`, GitLab `<web_url>/-/merge_requests/<n>`; null without a usable web root. */
export function prUrl(repo: CodeHostRepo | null | undefined, number: number | string): string | null {
  const root = webRoot(repo);
  if (!root) return null;
  return isGitLab(repo) ? `${root}/-/merge_requests/${number}` : `${root}/pull/${number}`;
}

/** GitHub `<web_url>/issues/<n>`, GitLab `<web_url>/-/issues/<n>`; null without a usable web root. */
export function issueUrl(repo: CodeHostRepo | null | undefined, number: number | string): string | null {
  const root = webRoot(repo);
  if (!root) return null;
  return isGitLab(repo) ? `${root}/-/issues/${number}` : `${root}/issues/${number}`;
}

/**
 * A file at a commit: GitHub `<web_url>/blob/<sha>/<path>#L<a>-L<b>`, GitLab `<web_url>/-/blob/<sha>/<path>#L<a>-<b>`;
 * a single line is `#L<a>` on both. `sha` pins the link to the PR's head so line numbers stay accurate. Null
 * without a usable web root.
 */
export function blobUrl(
  repo: CodeHostRepo | null | undefined,
  sha: string,
  file: string,
  startLine?: number | null,
  endLine?: number | null,
): string | null {
  const root = webRoot(repo);
  if (!root) return null;
  const gitlab = isGitLab(repo);
  let url = `${root}/${gitlab ? "-/blob" : "blob"}/${sha}/${encPath(file)}`;
  if (startLine != null) {
    url += `#L${startLine}`;
    if (endLine != null && endLine !== startLine) url += gitlab ? `-${endLine}` : `-L${endLine}`;
  }
  return url;
}

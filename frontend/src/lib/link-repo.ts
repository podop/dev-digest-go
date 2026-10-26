/* link-repo.ts — the repository the host-aware links of a PR screen are built from (GitLab A, FR41). The links
   (@/lib/code-host: prUrl / issueUrl / blobUrl) read the repository's provider and web_url, so a screen that only
   holds `owner/name` as a prop gets the full repository from the active-repo context. */
"use client";

import { useActiveRepo } from "@/lib/repo-context";
import type { CodeHostRepo } from "@/lib/code-host";

/**
 * The active repository for link building; before the repository list has loaded, a GitHub repository named
 * `fallbackFullName` (what the screens rendered before GitLab existed); null without either.
 */
export function useLinkRepo(fallbackFullName?: string | null): CodeHostRepo | null {
  const { activeRepo } = useActiveRepo();
  if (activeRepo) return activeRepo;
  return fallbackFullName ? { provider: "github", full_name: fallbackFullName } : null;
}

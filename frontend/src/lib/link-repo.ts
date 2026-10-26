/* link-repo.ts — the repository the host-aware links of a PR screen are built from (GitLab A, FR41). The links
   (@/lib/code-host: prUrl / issueUrl / blobUrl) read the repository's provider and web_url, so a screen that only
   holds `owner/name` as a prop gets the full repository from the active-repo context. */
"use client";

import { useActiveRepo } from "@/lib/repo-context";
import type { CodeHostRepo } from "@/lib/code-host";

/**
 * The active repository for link building. Null until the repository list has loaded (and without a
 * RepoProvider): the host is unknown then, so no link is built rather than a guessed github.com one (FR41).
 */
export function useLinkRepo(): CodeHostRepo | null {
  return useActiveRepo().activeRepo;
}

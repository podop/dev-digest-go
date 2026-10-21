import { githubBlobUrl } from "@/lib/github-urls";

/** Opens the file on GitHub at the tour's commit in a new tab. */
export function openPathOnGithub(repoFullName: string, sha: string, path: string): void {
  window.open(githubBlobUrl(repoFullName, sha, path), "_blank", "noopener");
}

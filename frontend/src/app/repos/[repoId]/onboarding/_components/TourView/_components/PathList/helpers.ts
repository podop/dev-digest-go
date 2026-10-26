import { blobUrl, type CodeHostRepo } from "@/lib/code-host";

/** Opens the file on the repository's code host at the tour's commit in a new tab; nothing without a usable web root. */
export function openPathOnHost(repo: CodeHostRepo | null, sha: string, path: string): void {
  const url = blobUrl(repo, sha, path);
  if (url) window.open(url, "_blank", "noopener");
}

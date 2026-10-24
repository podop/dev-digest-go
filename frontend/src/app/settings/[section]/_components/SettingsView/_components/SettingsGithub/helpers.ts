export type IndexedAge = { unit: "justNow" } | { unit: "minutes" | "hours" | "days"; count: number };

/** Coarse age of an ISO timestamp for "indexed 2 min ago" (keys: githubIntegration.repos.indexed.*). */
export function indexedAge(iso: string, now: number = Date.now()): IndexedAge {
  const minutes = Math.floor(Math.max(0, now - new Date(iso).getTime()) / 60_000);
  if (minutes < 1) return { unit: "justNow" };
  if (minutes < 60) return { unit: "minutes", count: minutes };
  const hours = Math.floor(minutes / 60);
  if (hours < 24) return { unit: "hours", count: hours };
  return { unit: "days", count: Math.floor(hours / 24) };
}

/** A repo counts as not indexed exactly when its index state has no indexed commit (A4). */
export function isNotIndexed(state: { lastIndexedSha: string }): boolean {
  return state.lastIndexedSha === "";
}

/** The scope list as shown to the user (A11). */
export function scopesText(scopes: readonly string[]): string {
  return scopes.join(" · ");
}

/** Warn only when GitHub reported scopes (a classic token) and `repo` is not among them (A11). */
export function lacksRepoScope(scopes: readonly string[] | null | undefined): boolean {
  return scopes != null && !scopes.includes("repo");
}

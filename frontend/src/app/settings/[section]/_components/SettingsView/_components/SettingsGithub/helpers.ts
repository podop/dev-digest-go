/** The scope list as shown to the user (A11). */
export function scopesText(scopes: readonly string[]): string {
  return scopes.join(" · ");
}

/** Warn only when GitHub reported scopes (a classic token) and `repo` is not among them (A11). */
export function lacksRepoScope(scopes: readonly string[] | null | undefined): boolean {
  return scopes != null && !scopes.includes("repo");
}

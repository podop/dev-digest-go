import type { GitLabConnection, Repo, SecretsStatus } from "@/lib/types";

/** Where `/` sends the user: a first-run setup step, or the first repo's PR list (FR27). */
export type HomeDestination = { kind: "setup"; step: "model" | "code-host" | "repo" } | { kind: "pulls"; repoId: string };

export const setupHref = (step: "model" | "code-host" | "repo") => `/setup?step=${step}`;
export const pullsHref = (repoId: string) => `/repos/${repoId}/pulls`;

export const destinationHref = (d: HomeDestination) => (d.kind === "setup" ? setupHref(d.step) : pullsHref(d.repoId));

/** First unmet first-run condition wins: a model provider, then a code host, then a repository.
 *  A model provider is a built-in key (env-provided keys count: secrets-status reports them
 *  configured) or any defined custom provider, keyed and tested or not (FR21, D21). A code host is a
 *  GitHub token or a GitLab connection that holds a token (FR39). */
export function homeDestination(
  secrets: SecretsStatus,
  repos: readonly Repo[],
  customProviders: readonly unknown[] = [],
  gitlabConnections: readonly Pick<GitLabConnection, "token_configured">[] = [],
): HomeDestination {
  if (!secrets.openai && !secrets.anthropic && !secrets.openrouter && customProviders.length === 0) {
    return { kind: "setup", step: "model" };
  }
  if (!secrets.github && !gitlabConnections.some((c) => c.token_configured)) {
    return { kind: "setup", step: "code-host" };
  }
  const first = repos[0];
  return first ? { kind: "pulls", repoId: first.id } : { kind: "setup", step: "repo" };
}

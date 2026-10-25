import type { Repo, SecretsStatus } from "@/lib/types";

/** Where `/` sends the user: a first-run setup step, or the first repo's PR list (FR27). */
export type HomeDestination = { kind: "setup"; step: "model" | "github" | "repo" } | { kind: "pulls"; repoId: string };

export const setupHref = (step: "model" | "github" | "repo") => `/setup?step=${step}`;
export const pullsHref = (repoId: string) => `/repos/${repoId}/pulls`;

export const destinationHref = (d: HomeDestination) => (d.kind === "setup" ? setupHref(d.step) : pullsHref(d.repoId));

/** First unmet first-run condition wins: a model key (any provider), then the GitHub token,
 *  then a repository. Env-provided keys count (secrets-status reports them configured). */
export function homeDestination(secrets: SecretsStatus, repos: readonly Repo[]): HomeDestination {
  if (!secrets.openai && !secrets.anthropic && !secrets.openrouter) return { kind: "setup", step: "model" };
  if (!secrets.github) return { kind: "setup", step: "github" };
  const first = repos[0];
  return first ? { kind: "pulls", repoId: first.id } : { kind: "setup", step: "repo" };
}

import type { Agent, CiExport, CiExportInputBody, CiTarget } from "@devdigest/shared";
import { isGitLab, isNotConnected } from "@/lib/code-host";
import type { Repo } from "@/lib/types";
import { ciExportable, isPluginDisabled, type CiExportBlock } from "@/lib/plugins";
import { isCustomId, type NamedCustomProvider } from "@/lib/providers/custom";

/** `runs-on` choice of the generated workflow (FR31, FR32). */
export type Runner = NonNullable<CiExportInputBody["runner"]>;
export const RUNNERS: readonly Runner[] = ["github_hosted", "self_hosted"];

/** The CI variable that carries the studio's GitLab token for the runner (GitLab C FR29, FR43). */
export const GITLAB_TOKEN_VARIABLE = "DEVDIGEST_GITLAB_TOKEN";

/** What `useInstallCi` needs to name the repository: GitHub sends the name only (the body stays as before),
 *  GitLab sends `repo_id` and the target (FR1, FR45). */
export interface RepoRef {
  repo: string;
  repo_id?: string;
  target?: CiTarget;
}

export function repoRefOf(repo: Repo): RepoRef {
  return isGitLab(repo) ? { repo: repo.full_name, repo_id: repo.id, target: "gitlab_ci" } : { repo: repo.full_name };
}

/** The repositories the dialog lists: the ones that can take an install, and the not-connected GitLab ones that
 *  are shown disabled with the reason (FR40, FR45). */
export function splitRepos(repos: readonly Repo[]): { usable: Repo[]; notConnected: Repo[] } {
  return {
    usable: repos.filter((r) => !isNotConnected(r)),
    notConnected: repos.filter((r) => isNotConnected(r)),
  };
}

/** Next steps of a GitLab install that the response carries (§7.1 `gitlab_ci`); null for GitHub or when absent. */
export type GitLabNextSteps = Pick<NonNullable<CiExport["gitlab_ci"]>, "root_config" | "config_path" | "include_snippet">;

export function gitlabNextSteps(result: Pick<CiExport, "gitlab_ci">): GitLabNextSteps | null {
  const g = result.gitlab_ci;
  return g ? { root_config: g.root_config, config_path: g.config_path, include_snippet: g.include_snippet } : null;
}

/** An agent the dialog lists: selectable (`block` null) or shown unselectable with the reason. */
export interface AgentOffer {
  agent: Agent;
  block: CiExportBlock | null;
}

/** Agents of a disabled plugin are not offered at all (FR18, A13); the rest are listed, and
 *  the ones that cannot go to CI (an undefined custom provider, FR32) carry the reason.
 *  `custom` is the workspace's custom providers, undefined while they are not known. */
export function offeredAgents(
  agents: readonly Agent[],
  custom?: readonly NamedCustomProvider[] | null,
): AgentOffer[] {
  return agents
    .filter((agent) => !isPluginDisabled(agent))
    .map((agent) => {
      const check = ciExportable(agent, custom);
      return { agent, block: check.ok ? null : check.reason };
    });
}

/** The Runner the dialog starts with: Self-hosted when any selected agent uses a custom provider, else
 *  GitHub-hosted (FR32, D18). */
export function defaultRunner(offers: readonly AgentOffer[], selected: ReadonlySet<string>): Runner {
  return offers.some((o) => selected.has(o.agent.id) && isCustomId(o.agent.provider)) ? "self_hosted" : "github_hosted";
}

/** What happened to one agent in the current run. */
export type AgentRun =
  | { status: "queued" }
  | { status: "running" }
  | {
      status: "done";
      prUrl: string | null;
      secrets: readonly string[];
      variables: readonly string[];
      /** GitLab only: the include to add when the project already has a root config. */
      gitlab: GitLabNextSteps | null;
    }
  | { status: "failed"; message: string | null };

export type RunMap = Readonly<Record<string, AgentRun>>;

/** Ids whose install failed, in the order of `order`. */
export function failedIds(runs: RunMap, order: readonly string[]): string[] {
  return order.filter((id) => runs[id]?.status === "failed");
}

/** True once a run was started (the repo and the selection are then locked). */
export function hasStarted(runs: RunMap): boolean {
  return Object.keys(runs).length > 0;
}

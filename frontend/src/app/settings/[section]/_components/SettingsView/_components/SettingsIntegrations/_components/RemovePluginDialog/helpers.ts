/* RemovePluginDialog helpers — what the dialog counts before it deletes (FR17, A11). */
import type { Agent } from "@devdigest/shared";
import type { CiRepoEntry } from "@/lib/types";

/** GitHub Action installations that go with the plugin: every installation of an agent the plugin owns,
 *  across all repositories (A11, installations × agents). */
export function pluginCiInstallationCount(
  pluginId: string,
  agents: readonly Pick<Agent, "id" | "plugin">[],
  repos: readonly CiRepoEntry[],
): number {
  const owned = new Set(agents.filter((a) => a.plugin?.id === pluginId).map((a) => a.id));
  let count = 0;
  for (const repo of repos) {
    for (const installation of repo.installations) {
      if (owned.has(installation.agent_id)) count += 1;
    }
  }
  return count;
}

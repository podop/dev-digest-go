/** Pure helpers for AppShell. */

import type { RepoSummary } from "@devdigest/ui";
import { WORKSPACE_SWITCHER_NAME_MAX } from "@devdigest/shared/constants/workspace";
import { repoLabel } from "@/lib/code-host";
import type { Repo } from "@/lib/types";

/**
 * The workspace name as the repo switcher shows it: cut at WORKSPACE_SWITCHER_NAME_MAX code
 * points + "…" (the vendored subtitle takes a plain string, so there is no tooltip — spec D24).
 * A missing or blank name gives "" (no name is shown).
 */
export function switcherWorkspaceName(name: string | null | undefined): string {
  const trimmed = name?.trim() ?? "";
  const points = Array.from(trimmed);
  if (points.length <= WORKSPACE_SWITCHER_NAME_MAX) return trimmed;
  return `${points.slice(0, WORKSPACE_SWITCHER_NAME_MAX).join("")}…`;
}

/**
 * Map a lib `Repo` to the `RepoSummary` shape the AppFrame shell context expects. The switcher
 * switcher label is `full_name`, with the host for a GitLab repository (FR44, "acme/platform/payments · gitlab.com"); the
 * subtitle is `<branch> · <syncedLabel>`, so a workspace name goes into the label: `<label> · <name>`.
 */
export function toShellRepo(r: Repo, workspaceName?: string | null): RepoSummary {
  const label = r.last_polled_at ? "synced" : "not synced";
  const name = switcherWorkspaceName(workspaceName);
  return {
    id: r.id,
    full_name: repoLabel(r),
    default_branch: r.default_branch,
    syncedLabel: name ? `${label} · ${name}` : label,
  };
}

/** Whether an event target is a text-entry element (guards typing-aware shortcuts). */
export function isTextInput(el: EventTarget | null): boolean {
  const node = el as HTMLElement | null;
  return (
    !!node &&
    (node.tagName === "INPUT" || node.tagName === "TEXTAREA" || node.isContentEditable)
  );
}

/** Derive the active sidebar key from the current pathname. */
export function activeKeyFor(pathname: string): string {
  if (pathname.startsWith("/settings")) return "settings";
  if (pathname.includes("/multi-agent")) return "multi-agent";
  // `/onboarding` alone is the add-repository screen; the tour lives under a repo.
  if (pathname.startsWith("/repos/") && pathname.includes("/onboarding")) return "onboarding-tour";
  if (pathname.includes("/context")) return "context";
  if (pathname.includes("/conventions")) return "conventions";
  if (pathname.startsWith("/leaderboard")) return "pr-leaderboard";
  if (pathname.includes("/pulls")) return "pulls";
  if (pathname.startsWith("/skills")) return "skills";
  if (pathname.startsWith("/agents")) return "agents";
  if (pathname.startsWith("/eval")) return "eval";
  if (pathname.startsWith("/memory")) return "memory";
  if (pathname.startsWith("/agent-performance")) return "agent-performance";
  if (pathname.startsWith("/ci-runs")) return "ci-runs";
  return "";
}

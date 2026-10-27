"use client";

import React from "react";
import Link from "next/link";
import { usePathname, useRouter } from "next/navigation";
import { type ShellContext } from "@devdigest/ui";
import { useTheme } from "@/lib/theme";
import { useActiveRepo } from "@/lib/repo-context";
import { useConfirmLeave } from "@/lib/leave-guard";
import { usePulls, useWorkspace } from "@/lib/hooks";
import { activeKeyFor, toShellRepo } from "../helpers";

interface ShellContextOptions {
  onOpenCommandPalette: () => void;
}

/**
 * Assembles the `ShellContext` consumed by AppFrame: active nav key, the repo
 * list/active repo (mapped to the shell shape), theme, PR count, and the repo
 * selection action. There is no 'Add repository' item in the switcher (repositories are added in
 * Settings › First-run setup). Removing a repository lives only in Settings › GitHub / GitLab
 * Integration, so the switcher gets no `onRemoveRepo` and its menu shows no remove button.
 */
export function useShellContext({ onOpenCommandPalette }: ShellContextOptions): ShellContext {
  const pathname = usePathname() ?? "/";
  const router = useRouter();
  const { theme, toggle } = useTheme();
  const { repoId, repos, activeRepo, setRepoId } = useActiveRepo();
  const { data: pulls } = usePulls(repoId);
  // A failed name load leaves `data` undefined → the switcher shows no name.
  const { data: workspace } = useWorkspace();
  const workspaceName = workspace?.name;
  const confirmLeave = useConfirmLeave();

  const onSelectRepo = React.useCallback(
    (id: string) => {
      if (!confirmLeave()) return;
      setRepoId(id);
      router.push(`/repos/${id}/pulls`);
    },
    [setRepoId, router, confirmLeave],
  );

  return React.useMemo<ShellContext>(
    () => ({
      Link,
      activeKey: activeKeyFor(pathname),
      repoId,
      repos: repos.map((r) => toShellRepo(r)),
      // Only the active repo's subtitle (the switcher trigger) carries the workspace name.
      activeRepo: activeRepo ? toShellRepo(activeRepo, workspaceName) : null,
      theme,
      onToggleTheme: toggle,
      onOpenCommandPalette,
      onSelectRepo,
      // Sidebar badge = PRs that still NEED review, not the total PR count.
      // 0 → undefined so the badge hides entirely when nothing needs review.
      prCount: pulls?.filter((p) => p.status === "needs_review").length || undefined,
    }),
    [
      pathname,
      repoId,
      repos,
      activeRepo,
      workspaceName,
      theme,
      toggle,
      onOpenCommandPalette,
      onSelectRepo,
      pulls,
    ],
  );
}

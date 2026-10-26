/* useExportWizard — state of the Export wizard: step, repository, options, the workflow the
   user edited, and the two ways to finish (open the PR / download the zip). The preview is
   a query keyed by the options, so changing an option regenerates the workflow; once the
   workflow is edited the options are locked (the edit would otherwise be lost). The Runner
   defaults by the agent's provider (self-hosted for a custom one) until the user chooses. Also
   useDialogFocus (re-exported from @/lib/dialog-focus): Escape, the Tab trap and focus return. */
"use client";

import React from "react";
import type { CiFile } from "@devdigest/shared";
import { useCiExportFiles, useCustomProviders, useOpenCiPr, useRepos, type CiExportOptions } from "@/lib/hooks";
import { isGitLab } from "@/lib/code-host";
import { useDialogFocus } from "@/lib/dialog-focus";
import { useActiveRepo } from "@/lib/repo-context";
import { hostOf } from "@/lib/providers/custom";
import {
  DEFAULT_POST_AS,
  DEFAULT_TRIGGERS,
  LAST_STEP,
  type InstallMethod,
  type PostAs,
  type Runner,
  type Trigger,
  type TriggerState,
} from "./constants";
import {
  defaultRunner,
  findWorkflow,
  slugFromFiles,
  triggersToList,
  withEditedWorkflow,
  zipFileName,
  type CustomInfo,
} from "./helpers";
import { buildZip, downloadBlob } from "./zip";

export function useExportWizard({
  agentId,
  provider,
  initialRepo,
}: {
  agentId: string;
  /** The agent's provider id: decides the default Runner and which custom provider the notes describe. */
  provider: string;
  initialRepo?: string;
}) {
  const repos = useRepos();
  const customProviders = useCustomProviders();
  const { activeRepo } = useActiveRepo();
  const openPr = useOpenCiPr(agentId);

  const [step, setStep] = React.useState(0);
  const [chosenRepo, setChosenRepo] = React.useState<string | null>(null);
  const [triggers, setTriggers] = React.useState<TriggerState>(DEFAULT_TRIGGERS);
  const [postAs, setPostAs] = React.useState<PostAs>(DEFAULT_POST_AS);
  const [chosenRunner, setChosenRunner] = React.useState<Runner | null>(null);
  const [edited, setEdited] = React.useState<string | null>(null);
  const [selectedPath, setSelectedPath] = React.useState<string | null>(null);
  const [method, setMethod] = React.useState<InstallMethod>("pr");
  const [zipName, setZipName] = React.useState<string | null>(null);

  // SCR3: the GitHub Actions export targets GitHub repositories only; GitLab ones are not offered.
  const githubRepos = React.useMemo(() => repos.data?.filter((r) => !isGitLab(r)), [repos.data]);
  const repoNames = React.useMemo(() => (githubRepos ?? []).map((r) => r.full_name), [githubRepos]);
  // The chosen repo, else the one "Update" came from, else the active repo — only if still imported.
  const repo = [chosenRepo, initialRepo, activeRepo?.full_name].find((r) => r != null && repoNames.includes(r)) ?? null;
  const triggerList = triggersToList(triggers);
  const runner = chosenRunner ?? defaultRunner(provider);
  const found = customProviders.data?.find((p) => p.id === provider);
  const custom: CustomInfo | null = found
    ? { name: found.name, baseUrl: found.base_url, host: hostOf(found.base_url) }
    : null;

  const options: CiExportOptions | null =
    step >= 1 && repo && triggerList.length > 0
      ? { repo, triggers: triggerList, post_as: postAs, runner }
      : null;
  const preview = useCiExportFiles(agentId, options);

  const generated: CiFile[] | undefined = preview.data?.files;
  const files = React.useMemo(() => (generated ? withEditedWorkflow(generated, edited) : undefined), [generated, edited]);
  const workflowPath = generated ? findWorkflow(generated)?.path : undefined;
  const selectedFile = files?.find((f) => f.path === (selectedPath ?? workflowPath)) ?? files?.[0];
  const slug = slugFromFiles(generated);

  const canContinue =
    step === 0 ? repo != null : step === 1 ? files != null : step === 2 ? triggerList.length > 0 : false;

  const chooseRepo = (name: string) => {
    setChosenRepo(name);
    setEdited(null); // the slug and paths can differ per repository
    setSelectedPath(null);
  };
  const toggleTrigger = (t: Trigger) => setTriggers((prev) => ({ ...prev, [t]: !prev[t] }));

  const install = () => {
    if (!repo || !files || openPr.isPending) return;
    if (method === "zip") {
      const name = zipFileName(slug ?? "agent");
      downloadBlob(buildZip(files.map((f) => ({ path: f.path, contents: f.contents }))), name);
      setZipName(name);
      return;
    }
    openPr.mutate({ repo, triggers: triggerList, post_as: postAs, runner, workflow: edited });
  };

  return {
    step,
    next: () => setStep((n) => Math.min(n + 1, LAST_STEP)),
    back: () => setStep((n) => Math.max(n - 1, 0)),
    canContinue,
    noTriggers: triggerList.length === 0,
    repos: { data: githubRepos, isLoading: repos.isLoading, isError: repos.isError, refetch: repos.refetch },
    repo,
    chooseRepo,
    triggers,
    toggleTrigger,
    postAs,
    setPostAs,
    runner,
    setRunner: setChosenRunner,
    custom,
    edited,
    setEdited,
    resetEdited: () => setEdited(null),
    preview,
    files,
    selectedFile,
    selectFile: setSelectedPath,
    slug,
    method,
    setMethod,
    zipName,
    openPr,
    install,
  };
}

export { useDialogFocus };

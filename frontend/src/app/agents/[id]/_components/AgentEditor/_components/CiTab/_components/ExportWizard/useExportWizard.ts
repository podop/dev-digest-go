/* useExportWizard — state of the Export wizard: step, repository, options, the workflow the
   user edited, and the two ways to finish (open the PR / download the zip). The preview is
   a query keyed by the options, so changing an option regenerates the workflow; once the
   workflow is edited the options are locked (the edit would otherwise be lost). The Runner
   defaults by the agent's provider (self-hosted for a custom one) until the user chooses.
   The repository is chosen by id (two hosts can share a name): a GitLab one sends `repo_id` +
   target "gitlab_ci" with all three triggers (GitLab runs MR pipelines on every push, FR4); a
   GitHub one sends the same body as before. A not-connected GitLab repository is not selectable. Also
   useDialogFocus (re-exported from @/lib/dialog-focus): Escape, the Tab trap and focus return. */
"use client";

import React from "react";
import type { CiFile } from "@devdigest/shared";
import { useCiExportFiles, useCustomProviders, useOpenCiPr, useRepos, type CiExportOptions } from "@/lib/hooks";
import { isGitLab, isNotConnected } from "@/lib/code-host";
import { useDialogFocus } from "@/lib/dialog-focus";
import { useActiveRepo } from "@/lib/repo-context";
import { hostOf } from "@/lib/providers/custom";
import {
  DEFAULT_POST_AS,
  DEFAULT_TRIGGERS,
  LAST_STEP,
  TRIGGERS,
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
  const [chosenRepoId, setChosenRepoId] = React.useState<string | null>(null);
  const [triggers, setTriggers] = React.useState<TriggerState>(DEFAULT_TRIGGERS);
  const [postAs, setPostAs] = React.useState<PostAs>(DEFAULT_POST_AS);
  const [chosenRunner, setChosenRunner] = React.useState<Runner | null>(null);
  const [edited, setEdited] = React.useState<string | null>(null);
  const [selectedPath, setSelectedPath] = React.useState<string | null>(null);
  const [method, setMethod] = React.useState<InstallMethod>("pr");
  const [zipName, setZipName] = React.useState<string | null>(null);

  // Every repository is listed; a not-connected GitLab one cannot be exported (FR40) and is never selected.
  const allRepos = repos.data;
  const selectable = React.useMemo(() => (allRepos ?? []).filter((r) => !isNotConnected(r)), [allRepos]);
  // The chosen repo, else the one "Update" came from, else the active repo — only if still imported and connected.
  const repoRecord =
    selectable.find((r) => r.id === chosenRepoId) ??
    selectable.find((r) => r.full_name === initialRepo) ??
    selectable.find((r) => r.id === activeRepo?.id) ??
    null;
  const repo = repoRecord?.full_name ?? null;
  const gitlab = isGitLab(repoRecord);
  // GitLab ignores the trigger choice: all three are sent, so the server's triggers note stays empty.
  const triggerList = gitlab ? [...TRIGGERS] : triggersToList(triggers);
  const runner = chosenRunner ?? defaultRunner(provider);
  const found = customProviders.data?.find((p) => p.id === provider);
  const custom: CustomInfo | null = found
    ? { name: found.name, baseUrl: found.base_url, host: hostOf(found.base_url) }
    : null;

  const repoRef: Pick<CiExportOptions, "repo_id" | "target"> =
    gitlab && repoRecord ? { repo_id: repoRecord.id, target: "gitlab_ci" } : {};

  const options: CiExportOptions | null =
    step >= 1 && repo && triggerList.length > 0
      ? { repo, ...repoRef, triggers: triggerList, post_as: postAs, runner }
      : null;
  const preview = useCiExportFiles(agentId, options);

  const generated: CiFile[] | undefined = preview.data?.files;
  const files = React.useMemo(() => (generated ? withEditedWorkflow(generated, edited) : undefined), [generated, edited]);
  const workflowPath = generated ? findWorkflow(generated)?.path : undefined;
  const selectedFile = files?.find((f) => f.path === (selectedPath ?? workflowPath)) ?? files?.[0];
  const slug = slugFromFiles(generated);

  const canContinue =
    step === 0 ? repo != null : step === 1 ? files != null : step === 2 ? triggerList.length > 0 : false;

  const chooseRepo = (id: string) => {
    setChosenRepoId(id);
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
    openPr.mutate({ repo, ...repoRef, triggers: triggerList, post_as: postAs, runner, workflow: edited });
  };

  return {
    step,
    next: () => setStep((n) => Math.min(n + 1, LAST_STEP)),
    back: () => setStep((n) => Math.max(n - 1, 0)),
    canContinue,
    noTriggers: triggerList.length === 0,
    gitlab,
    /** The `gitlab_ci` block of the answer: the PR response (what was committed) wins over the preview. */
    gitlabCi: openPr.data?.gitlab_ci ?? preview.data?.gitlab_ci ?? null,
    repos: { data: allRepos, isLoading: repos.isLoading, isError: repos.isError, refetch: repos.refetch },
    repo,
    repoId: repoRecord?.id ?? null,
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

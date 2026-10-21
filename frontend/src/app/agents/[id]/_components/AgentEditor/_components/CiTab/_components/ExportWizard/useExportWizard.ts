/* useExportWizard — state of the Export wizard: step, repository, options, the workflow the
   user edited, and the two ways to finish (open the PR / download the zip). The preview is
   a query keyed by the options, so changing an option regenerates the workflow; once the
   workflow is edited the options are locked (the edit would otherwise be lost). Also
   useDialogFocus: Escape, the Tab trap and focus return — the vendored Modal has none. */
"use client";

import React from "react";
import type { CiFile } from "@devdigest/shared";
import { useCiExportFiles, useOpenCiPr, useRepos, type CiExportOptions } from "@/lib/hooks";
import { useActiveRepo } from "@/lib/repo-context";
import {
  DEFAULT_POST_AS,
  DEFAULT_TRIGGERS,
  LAST_STEP,
  type InstallMethod,
  type PostAs,
  type Trigger,
  type TriggerState,
} from "./constants";
import { findWorkflow, focusableIn, slugFromFiles, triggersToList, withEditedWorkflow, wrapTarget, zipFileName } from "./helpers";
import { buildZip, downloadBlob } from "./zip";

export function useExportWizard({ agentId, initialRepo }: { agentId: string; initialRepo?: string }) {
  const repos = useRepos();
  const { activeRepo } = useActiveRepo();
  const openPr = useOpenCiPr(agentId);

  const [step, setStep] = React.useState(0);
  const [chosenRepo, setChosenRepo] = React.useState<string | null>(null);
  const [triggers, setTriggers] = React.useState<TriggerState>(DEFAULT_TRIGGERS);
  const [postAs, setPostAs] = React.useState<PostAs>(DEFAULT_POST_AS);
  const [edited, setEdited] = React.useState<string | null>(null);
  const [selectedPath, setSelectedPath] = React.useState<string | null>(null);
  const [method, setMethod] = React.useState<InstallMethod>("pr");
  const [zipName, setZipName] = React.useState<string | null>(null);

  const repoNames = React.useMemo(() => (repos.data ?? []).map((r) => r.full_name), [repos.data]);
  // The chosen repo, else the one "Update" came from, else the active repo — only if still imported.
  const repo = [chosenRepo, initialRepo, activeRepo?.full_name].find((r) => r != null && repoNames.includes(r)) ?? null;
  const triggerList = triggersToList(triggers);

  const options: CiExportOptions | null =
    step >= 1 && repo && triggerList.length > 0 ? { repo, triggers: triggerList, post_as: postAs } : null;
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
    openPr.mutate({ repo, triggers: triggerList, post_as: postAs, workflow: edited });
  };

  return {
    step,
    next: () => setStep((n) => Math.min(n + 1, LAST_STEP)),
    back: () => setStep((n) => Math.max(n - 1, 0)),
    canContinue,
    noTriggers: triggerList.length === 0,
    repos,
    repo,
    chooseRepo,
    triggers,
    toggleTrigger,
    postAs,
    setPostAs,
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

/** Escape closes (unless a Select already used it), Tab stays inside the dialog, and focus
 *  moves in on open and back to the opener on close (NFR10). Returns the wrapper's ref. */
export function useDialogFocus(onClose: () => void) {
  const ref = React.useRef<HTMLDivElement>(null);
  const closeRef = React.useRef(onClose);
  closeRef.current = onClose;

  React.useEffect(() => {
    const opener = document.activeElement instanceof HTMLElement ? document.activeElement : null;
    const dialog = () => ref.current?.querySelector<HTMLElement>('[role="dialog"]') ?? null;
    focusableIn(dialog() ?? document.body)[0]?.focus();

    const onKey = (e: KeyboardEvent) => {
      if (e.defaultPrevented) return;
      if (e.key === "Escape") {
        closeRef.current();
        return;
      }
      if (e.key !== "Tab") return;
      const root = dialog();
      if (!root) return;
      const stops = focusableIn(root);
      const target = wrapTarget(stops, document.activeElement, e.shiftKey, root.contains(document.activeElement));
      if (target) {
        e.preventDefault();
        target.focus();
      } else if (stops.length === 0) {
        e.preventDefault();
      }
    };
    document.addEventListener("keydown", onKey);
    return () => {
      document.removeEventListener("keydown", onKey);
      opener?.focus();
    };
  }, []);

  return ref;
}

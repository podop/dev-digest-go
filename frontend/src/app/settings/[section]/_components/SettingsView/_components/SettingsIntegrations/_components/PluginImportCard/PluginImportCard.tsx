/* PluginImportCard — Settings › Integrations › Import plugin (FR16, EC14, AC19). Flow: pick a
   file (drop or browse) → type/size check with no request → dry-run preview → Install. The
   preview and install hooks are quiet for every error code, so every failure is rendered here,
   inline: 413/415/422 keep the drop zone, 409 lists the conflicts, network/5xx say
   "Import failed — try again". The installed row itself is shown by the installed-plugins list,
   which the install hook invalidates. */
"use client";

import React from "react";
import { useTranslations } from "next-intl";
import type { PluginImportPreview as Preview, PluginImportResult } from "@devdigest/shared";
import { Button } from "@devdigest/ui";
import { useInstallPlugin, usePreviewPlugin } from "@/lib/hooks";
import { PluginImportPreview } from "../PluginImportPreview";
import { ImportDropZone } from "./_components/ImportDropZone";
import { ImportProblem } from "./_components/ImportProblem";
import { checkFile, classifyFailure, type ImportConflicts, type ImportProblem as Problem } from "./helpers";
import { s } from "./styles";

type State =
  | { phase: "idle"; problem: Problem | null }
  | { phase: "checking" }
  | { phase: "preview"; file: File; preview: Preview; installing: boolean; problem: Problem | null }
  | { phase: "installed"; result: PluginImportResult };

const IDLE: State = { phase: "idle", problem: null };

export function PluginImportCard() {
  const t = useTranslations("integrationsImport");
  const titleId = React.useId();
  const rootRef = React.useRef<HTMLElement>(null);
  const previewPlugin = usePreviewPlugin();
  const installPlugin = useInstallPlugin();
  const [state, setState] = React.useState<State>(IDLE);
  // A response that arrives after Cancel or after a newer pick is dropped.
  const attempt = React.useRef(0);
  const refocusBrowse = React.useRef(false);

  // Cancel / "Import another" unmount the preview; the browse button takes focus back (NFR5).
  React.useEffect(() => {
    if (state.phase !== "idle" || !refocusBrowse.current) return;
    refocusBrowse.current = false;
    rootRef.current?.querySelector<HTMLButtonElement>("[data-browse]")?.focus();
  }, [state.phase]);

  const reset = () => {
    attempt.current += 1;
    refocusBrowse.current = true;
    setState(IDLE);
  };

  const pick = async (file: File) => {
    const check = checkFile(file);
    if (!check.ok) {
      setState({ phase: "idle", problem: check.problem });
      return;
    }
    const mine = ++attempt.current;
    setState({ phase: "checking" });
    try {
      const preview = await previewPlugin.mutateAsync({ file });
      if (mine === attempt.current) setState({ phase: "preview", file, preview, installing: false, problem: null });
    } catch (err) {
      if (mine === attempt.current) {
        const problem = classifyFailure(err);
        // A dry run has no conflict list to show; the zone shows the server's message instead.
        setState({
          phase: "idle",
          problem: problem.kind === "conflict" ? { kind: "message", message: problem.message, issues: [] } : problem,
        });
      }
    }
  };

  const install = async () => {
    if (state.phase !== "preview" || state.installing) return;
    const { file, preview } = state;
    const mine = ++attempt.current;
    setState({ ...state, installing: true, problem: null });
    try {
      const result = await installPlugin.mutateAsync({ file });
      if (mine === attempt.current) setState({ phase: "installed", result });
    } catch (err) {
      if (mine === attempt.current) {
        setState({ phase: "preview", file, preview, installing: false, problem: classifyFailure(err) });
      }
    }
  };

  return (
    <section ref={rootRef} aria-labelledby={titleId} style={s.card}>
      <h3 id={titleId} style={s.title}>
        {t("title")}
      </h3>
      <p style={s.body}>{t("body")}</p>

      {(state.phase === "idle" || state.phase === "checking") && (
        <>
          <ImportDropZone checking={state.phase === "checking"} onFile={(file) => void pick(file)} />
          {state.phase === "idle" && state.problem && <ImportProblem problem={state.problem} />}
        </>
      )}

      {state.phase === "preview" && (
        <PluginImportPreview
          preview={state.preview}
          conflicts={conflictsOf(state)}
          installing={state.installing}
          notice={state.problem ? <ImportProblem problem={state.problem} /> : null}
          onInstall={() => void install()}
          onCancel={reset}
        />
      )}

      {state.phase === "installed" && (
        <div role="status" style={s.installed}>
          <div style={s.installedTitle}>
            {t("installed.title")}{" "}
            <span style={s.name} title={state.result.installed.name}>
              {state.result.installed.name}
            </span>
          </div>
          <div>
            {t("installed.created", {
              agents: state.result.created.agents,
              skills: state.result.created.skills,
              evalCases: state.result.created.eval_cases,
              learnings: state.result.created.learnings,
            })}
          </div>
          <div style={s.hint}>{t("installed.enableHint")}</div>
          <div style={s.actions}>
            <Button type="button" kind="secondary" onClick={reset}>
              {t("installed.another")}
            </Button>
          </div>
        </div>
      )}
    </section>
  );
}

/** A 409 on install replaces the preview's conflicts with the server's. */
function conflictsOf(state: Extract<State, { phase: "preview" }>): ImportConflicts {
  return state.problem?.kind === "conflict" ? state.problem.conflicts : state.preview.conflicts;
}

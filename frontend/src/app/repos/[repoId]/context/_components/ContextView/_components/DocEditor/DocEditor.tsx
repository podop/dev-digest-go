/* DocEditor — the Edit mode of a store file: a CodeMirror markdown field over the
   draft the ContextView owns, Save (button or Ctrl/Cmd+S) with base_version, and
   "Saved" for 2 s. A refused save (stale / gone) shows a banner and keeps the text;
   after "gone" + Keep editing the next Save re-creates the file at the same path. */
"use client";

import React from "react";
import dynamic from "next/dynamic";
import { useTranslations } from "next-intl";
import { Button, Skeleton } from "@devdigest/ui";
import type { ContextDocPreview } from "@devdigest/shared";
import { useCreateContextFile, useRefreshContext, useSaveContextFile } from "@/lib/hooks";
import { isSaveShortcut, saveProblem, SAVED_FLASH_MS, type SaveProblem } from "./helpers";
import { StaleBanner } from "./_components/StaleBanner";
import { s } from "./styles";

const MarkdownField = dynamic(() => import("./_components/MarkdownField").then((m) => m.MarkdownField), {
  ssr: false,
  loading: () => <Skeleton height={320} />,
});

export function DocEditor({
  repoId,
  doc,
  text,
  onChange,
  onSaved,
  onDiscard,
}: {
  repoId: string;
  doc: ContextDocPreview;
  /** The draft, or the saved content while nothing was typed. */
  text: string;
  onChange: (text: string) => void;
  /** The server stored `savedText` for `path`. */
  onSaved: (path: string, savedText: string) => void;
  /** Throw the draft away (Reload). */
  onDiscard: () => void;
}) {
  const t = useTranslations("context");
  const save = useSaveContextFile(repoId);
  const create = useCreateContextFile(repoId);
  const refresh = useRefreshContext(repoId);
  const [problem, setProblem] = React.useState<SaveProblem | null>(null);
  const [saved, setSaved] = React.useState(false);
  const [syncing, setSyncing] = React.useState(false);
  /** The file is gone and the user chose to keep the text: Save creates it again. */
  const [recreate, setRecreate] = React.useState(false);
  const dirty = text !== doc.content;
  const pending = save.isPending || create.isPending;
  const blocked = problem === "gone" || problem === "exists";

  React.useEffect(() => {
    if (!saved) return;
    const timer = setTimeout(() => setSaved(false), SAVED_FLASH_MS);
    return () => clearTimeout(timer);
  }, [saved]);

  const submit = async () => {
    if (!dirty || pending || blocked) return;
    const content = text;
    try {
      if (recreate) await create.mutateAsync({ path: doc.path, content, on_conflict: "fail" });
      else await save.mutateAsync({ path: doc.path, content, base_version: doc.version ?? 0 });
    } catch (e) {
      // Other failures were already toasted by the global mutation handler.
      setProblem(saveProblem(e));
      return;
    }
    setProblem(null);
    setRecreate(false);
    setSaved(true);
    onSaved(doc.path, content);
  };

  const reload = () => {
    setProblem(null);
    setRecreate(false);
    onDiscard();
    void refresh();
  };

  /** Keep the text. Gone: the next Save re-creates the file. Otherwise adopt the live
   *  version so the next Save overwrites it. */
  const keepEditing = async () => {
    if (problem === "gone") {
      setRecreate(true);
      setProblem(null);
      return;
    }
    setRecreate(false);
    setSyncing(true);
    try {
      await refresh();
    } finally {
      setSyncing(false);
      setProblem(null);
    }
  };

  const onKeyDown = (e: React.KeyboardEvent) => {
    if (!isSaveShortcut(e)) return;
    e.preventDefault();
    void submit();
  };

  return (
    // The field is a CodeMirror/textarea child; the wrapper only catches its Ctrl/Cmd+S.
    <div role="presentation" onKeyDown={onKeyDown}>
      {problem && <StaleBanner problem={problem} busy={syncing} onReload={reload} onKeep={() => void keepEditing()} />}
      <div style={s.bar}>
        <span role="status" aria-live="polite" style={s.status}>
          {saved ? t("editor.saved") : dirty ? t("editor.unsaved") : ""}
        </span>
        <Button icon="Check" disabled={!dirty || pending || blocked} onClick={() => void submit()}>
          {pending ? t("editor.saving") : t("editor.save")}
        </Button>
      </div>
      <div style={s.frame}>
        <MarkdownField value={text} onChange={onChange} label={t("editor.field")} />
      </div>
    </div>
  );
}

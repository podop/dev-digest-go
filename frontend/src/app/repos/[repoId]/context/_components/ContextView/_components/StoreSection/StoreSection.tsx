/* StoreSection — the `.devdigest/specs/` part of the Project Context list: the toolbar
   (New file, New folder, Upload, Refresh) and the store files with a row menu
   (rename / delete). A new file opens in Edit mode with its name ready to rename. */
"use client";

import React from "react";
import { useTranslations } from "next-intl";
import { IconBtn } from "@devdigest/ui";
import type { ContextDoc } from "@devdigest/shared";
import { useCreateContextFile, useRefreshContext } from "@/lib/hooks";
import type { ContextMode } from "../../helpers";
import { NewFolderDialog } from "./_components/NewFolderDialog";
import { StoreFileRow } from "./_components/StoreFileRow";
import { useUploadStoreFiles } from "./useUploadStoreFiles";
import { s } from "./styles";

export function StoreSection({
  repoId,
  docs,
  empty,
  selected,
  filter,
  onGo,
  onRenamed,
}: {
  repoId: string;
  /** The store files to show (already filtered), in list order. */
  docs: readonly ContextDoc[];
  /** The repo has no store files at all (and the list has loaded). */
  empty: boolean;
  selected: string | null;
  /** Rendered between the toolbar and the rows (the list filter). */
  filter?: React.ReactNode;
  /** Select a path (null = nothing) in a mode. */
  onGo: (path: string | null, mode?: ContextMode) => void;
  /** The file `from` was renamed to `to`; the owner moves the selection (and a draft) along. */
  onRenamed: (from: string, to: string) => void;
}) {
  const t = useTranslations("context");
  const refresh = useRefreshContext(repoId);
  const create = useCreateContextFile(repoId);
  const { upload, progress } = useUploadStoreFiles(create.mutateAsync);
  const fileInput = React.useRef<HTMLInputElement>(null);
  const [renaming, setRenaming] = React.useState<string | null>(null);
  const [folderOpen, setFolderOpen] = React.useState(false);
  const busy = create.isPending || progress !== null;

  const newFile = async () => {
    try {
      const doc = await create.mutateAsync({});
      onGo(doc.path, "edit");
      setRenaming(doc.path);
    } catch {
      /* toasted by the global mutation handler */
    }
  };

  const newFolder = async (path: string) => {
    try {
      const doc = await create.mutateAsync({ path, on_conflict: "suffix" });
      setFolderOpen(false);
      onGo(doc.path, "edit");
    } catch {
      /* toasted by the global mutation handler; the dialog stays open */
    }
  };

  const onPick = (e: React.ChangeEvent<HTMLInputElement>) => {
    const picked = Array.from(e.target.files ?? []);
    e.target.value = ""; // the same file can be picked again
    if (picked.length > 0) void upload(picked);
  };

  return (
    <section style={s.section}>
      <div style={s.toolbar}>
        <IconBtn icon="Plus" label={t("store.newFile")} onClick={() => !busy && void newFile()} />
        <IconBtn icon="Folder" label={t("store.newFolder")} onClick={() => !busy && setFolderOpen(true)} />
        <IconBtn icon="Upload" label={t("store.upload")} onClick={() => !busy && fileInput.current?.click()} />
        <IconBtn icon="RefreshCw" label={t("refresh")} onClick={refresh} />
        <input
          ref={fileInput}
          type="file"
          multiple
          accept=".md,text/markdown"
          aria-label={t("store.uploadInput")}
          style={s.hiddenInput}
          onChange={onPick}
        />
      </div>
      {progress && (
        <p className="tnum" aria-live="polite" style={s.muted}>
          {t("store.uploadStatus.progress", { done: progress.current, total: progress.total })}
        </p>
      )}
      {filter}
      {empty && <p style={s.muted}>{t("store.empty")}</p>}
      {docs.length > 0 && (
        <ul aria-label={t("store.list")} style={s.list}>
          {docs.map((d) => (
            <StoreFileRow
              key={d.path}
              repoId={repoId}
              doc={d}
              selected={d.path === selected}
              renaming={d.path === renaming}
              onSelect={() => onGo(d.path)}
              onStartRename={() => setRenaming(d.path)}
              onStopRename={() => setRenaming(null)}
              onRenamed={(to) => {
                setRenaming(null);
                onRenamed(d.path, to);
              }}
              onDeleted={() => {
                if (d.path === selected) onGo(null);
              }}
            />
          ))}
        </ul>
      )}
      {folderOpen && (
        <NewFolderDialog busy={create.isPending} onSubmit={newFolder} onClose={() => setFolderOpen(false)} />
      )}
    </section>
  );
}

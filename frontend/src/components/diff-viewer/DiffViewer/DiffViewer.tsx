/* DiffViewer — basic GitHub-style unified diff viewer. Renders real PrFile.patch
   (unified-diff text from the F1 API) as a list of collapsible FileCards.
   Optional inline comments (Files changed tab): hover a line → "+" → comment,
   posted live to GitHub; existing GitHub review comments render inline. */
"use client";

import React from "react";
import { useTranslations } from "next-intl";
import type { PrFile } from "@/lib/types";
import { type DiffCommentApi } from "../comments";
import type { DiffFindingApi, DiffFindingItem } from "../findings";
import type { DiffHighlight } from "../helpers";
import { s } from "../styles";
import { FileCard } from "../FileCard";

export function DiffViewer<T extends DiffFindingItem = DiffFindingItem>({
  files,
  commenting,
  findingApi,
  defaultOpen,
  highlight,
}: {
  files: PrFile[];
  commenting?: DiffCommentApi;
  /** Findings slot (server/specs/06-smart-diff.md) — DiffViewer stays feature-agnostic; the caller supplies the data + card. */
  findingApi?: DiffFindingApi<T>;
  /** Which files start expanded; omitted → files up to AUTO_EXPAND_MAX_LINES changed lines. */
  defaultOpen?: (file: PrFile) => boolean;
  /** Lines to highlight: their file opens and scrolls them into view. */
  highlight?: DiffHighlight | null;
}) {
  const t = useTranslations("shell");
  if (!files || files.length === 0) {
    return <div style={s.empty}>{t("diffViewer.noChangedFiles")}</div>;
  }
  return (
    <div style={s.list}>
      {files.map((f) => (
        <FileCard
          key={f.path}
          file={f}
          commenting={commenting}
          findingApi={findingApi}
          defaultOpen={defaultOpen?.(f)}
          highlight={highlight?.path === f.path ? highlight : undefined}
        />
      ))}
    </div>
  );
}

/* ContextPicker — the shared body of the agent and skill Context tabs: heading
   with the "k attached" badge, a filter, the document rows (attached ones first
   in prompt order, drag or keyboard to reorder) and a Preview modal. Every
   check, uncheck or drop calls `onChange` with the FULL ordered path list — the
   tab saves it at once. Filtering turns dragging off. The footer / extra block
   comes in as `children`. */
"use client";

import React from "react";
import { useTranslations } from "next-intl";
import {
  closestCenter,
  DndContext,
  KeyboardSensor,
  PointerSensor,
  TouchSensor,
  useSensor,
  useSensors,
  type DragEndEvent,
} from "@dnd-kit/core";
import { SortableContext, sortableKeyboardCoordinates, verticalListSortingStrategy } from "@dnd-kit/sortable";
import { Badge, EmptyState, ErrorState, Icon, Skeleton } from "@devdigest/ui";
import { useContextDocs } from "@/lib/hooks";
import { buildRows, filterRows, moveAttached, toggleAttached } from "./helpers";
import { ContextDocRow, SortableContextDocRow } from "./_components/ContextDocRow";
import { DocPreviewModal } from "./_components/DocPreviewModal";
import { s } from "./styles";

interface Props {
  repoId: string;
  /** The ordered attached paths. */
  attached: readonly string[];
  onChange: (paths: string[]) => void;
  title: string;
  /** Badge text from the number of attached paths and of listed documents. */
  badge: (counts: { attached: number; total: number }) => string;
  hint: React.ReactNode;
  previewAs: "button" | "icon";
  children?: React.ReactNode;
}

export function ContextPicker({ repoId, attached, onChange, title, badge, hint, previewAs, children }: Props) {
  const t = useTranslations("context");
  const tc = useTranslations("common");
  const list = useContextDocs(repoId);
  const [filter, setFilter] = React.useState("");
  const [previewPath, setPreviewPath] = React.useState<string | null>(null);
  const closePreview = React.useCallback(() => setPreviewPath(null), []);
  const sensors = useSensors(
    useSensor(PointerSensor, { activationConstraint: { distance: 4 } }),
    useSensor(TouchSensor, { activationConstraint: { delay: 150, tolerance: 5 } }),
    useSensor(KeyboardSensor, { coordinateGetter: sortableKeyboardCoordinates }),
  );

  const docs = list.data?.docs ?? [];
  const rows = buildRows(docs, attached);
  const filtering = filter.trim().length > 0;
  const shown = filterRows(rows, filter);
  const shownAttached = shown.filter((r) => r.attached);
  const shownOther = shown.filter((r) => !r.attached);
  const globs = list.data?.globs.join(", ") ?? "";

  const onCheck = (path: string, on: boolean) => onChange(toggleAttached(attached, path, on));
  const onDragEnd = ({ active, over }: DragEndEvent) => {
    const next = moveAttached(attached, String(active.id), over ? String(over.id) : null);
    if (next) onChange(next);
  };

  return (
    <div>
      <div style={s.header}>
        <h2 style={s.h2}>{title}</h2>
        {list.data && <Badge color="var(--accent-text)" bg="var(--accent-bg)">{badge({ attached: attached.length, total: docs.length })}</Badge>}
        <span style={s.spacer} />
        {rows.length > 0 && (
          <div style={s.filter}>
            <Icon.Filter size={13} style={s.filterIcon} />
            <input
              value={filter}
              onChange={(e) => setFilter(e.target.value)}
              placeholder={t("filterPlaceholder")}
              aria-label={t("filterLabel")}
              style={s.filterInput}
            />
          </div>
        )}
      </div>
      <p style={s.caption}>{hint}</p>

      {list.isLoading && (
        <div style={s.skeletons}>
          <Skeleton height={44} />
          <Skeleton height={44} />
          <Skeleton height={44} />
        </div>
      )}
      {list.isError && (
        <ErrorState title={tc("states.error")} body={t("loadError")} onRetry={() => list.refetch()} retryLabel={tc("actions.retry")} />
      )}
      {list.data?.clone_status === "not_cloned" && (
        <EmptyState icon="Folder" title={t("notCloned.title")} body={t("notCloned.body")} />
      )}
      {list.data?.clone_status === "ready" && rows.length === 0 && (
        <EmptyState icon="FileText" title={t("empty.title", { globs })} body={t("empty.body", { globs })} />
      )}
      {list.data?.truncated && <p style={s.caption}>{t("truncated", { count: docs.length })}</p>}
      {list.data && rows.length > 0 && shown.length === 0 && <p style={s.caption}>{t("noMatch")}</p>}

      {list.data && (
        <>
          <DndContext sensors={sensors} collisionDetection={closestCenter} onDragEnd={onDragEnd}>
            <SortableContext items={[...attached]} strategy={verticalListSortingStrategy} disabled={filtering}>
              <ul style={s.list} aria-label={t("picker.attachedList")}>
                {shownAttached.map((row) => (
                  <SortableContextDocRow
                    key={row.path}
                    row={row}
                    disabled={filtering}
                    previewAs={previewAs}
                    onCheck={(on) => onCheck(row.path, on)}
                    onPreview={setPreviewPath}
                  />
                ))}
              </ul>
            </SortableContext>
          </DndContext>
          <ul style={s.list} aria-label={t("docList")}>
            {shownOther.map((row) => (
              <ContextDocRow
                key={row.path}
                row={row}
                previewAs={previewAs}
                onCheck={(on) => onCheck(row.path, on)}
                onPreview={setPreviewPath}
              />
            ))}
          </ul>
        </>
      )}

      {children}
      {previewPath && <DocPreviewModal repoId={repoId} path={previewPath} onClose={closePreview} />}
    </div>
  );
}

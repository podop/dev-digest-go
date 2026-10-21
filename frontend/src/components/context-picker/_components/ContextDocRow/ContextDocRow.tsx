/* ContextDocRow — one document in a Context tab: drag handle (attached rows
   only), checkbox, name, folder, type badge and a Preview action. Long names
   and folders are cut with an ellipsis, the full path is in the tooltip. An
   attached path the repo no longer lists is a `missing` row: it can be
   unchecked and moved but not previewed. SortableContextDocRow wires the row
   to dnd-kit. */
"use client";

import React from "react";
import { useTranslations } from "next-intl";
import { useSortable } from "@dnd-kit/sortable";
import { CSS } from "@dnd-kit/utilities";
import { Badge, Button, Checkbox, Icon, IconBtn } from "@devdigest/ui";
import { DOC_TYPE_STYLE } from "../../constants";
import { splitPath, type PickerRow } from "../../helpers";
import { s } from "./styles";

interface RowProps {
  row: PickerRow;
  onCheck: (on: boolean) => void;
  onPreview: (path: string) => void;
  /** "button" = labelled Preview button (agent tab); "icon" = eye icon (skill tab). */
  previewAs: "button" | "icon";
  /** Rendered in the handle slot (attached rows); other rows keep the gap. */
  handle?: React.ReactNode;
}

export const ContextDocRow = React.forwardRef<HTMLLIElement, RowProps & { style?: React.CSSProperties }>(
  function ContextDocRow({ row, onCheck, onPreview, previewAs, handle, style }, ref) {
    const t = useTranslations("context");
    const { name, folder } = splitPath(row.path);
    const doc = row.doc;
    const previewLabel = t("picker.previewFile", { name });
    return (
      <li
        ref={ref}
        style={{ ...s.row(row.attached), ...style }}
        data-testid="context-doc-row"
        data-path={row.path}
      >
        <span style={s.handleSlot}>{handle}</span>
        <div style={s.label}>
          <Checkbox
            checked={row.attached}
            onChange={onCheck}
            label={
              <span className="mono" style={s.name} title={row.path}>
                {name}
              </span>
            }
          />
        </div>
        <span style={s.folder} title={row.path}>
          {folder}
        </span>
        <span style={s.trail}>
          {row.missing && (
            <span title={t("picker.missingTitle")}>
              <Badge color="var(--warn)" bg="var(--warn-bg)" icon="AlertTriangle">
                {t("picker.missing")}
              </Badge>
            </span>
          )}
          {doc && (
            <Badge color={DOC_TYPE_STYLE[doc.doc_type].color} bg={DOC_TYPE_STYLE[doc.doc_type].bg}>
              {t(`docType.${doc.doc_type}`)}
            </Badge>
          )}
          {doc &&
            (previewAs === "icon" ? (
              <IconBtn icon="Eye" label={previewLabel} onClick={() => onPreview(row.path)} />
            ) : (
              <Button kind="secondary" size="sm" icon="Eye" aria-label={previewLabel} onClick={() => onPreview(row.path)}>
                {t("picker.preview")}
              </Button>
            ))}
        </span>
      </li>
    );
  },
);

/** An attached row in the sortable list; `disabled` (filter on) turns dragging off. */
export function SortableContextDocRow({ disabled, ...props }: Omit<RowProps, "handle"> & { disabled: boolean }) {
  const t = useTranslations("context");
  const { attributes, listeners, setNodeRef, transform, transition, isDragging } = useSortable({
    id: props.row.path,
    disabled,
  });
  const style: React.CSSProperties = {
    transform: CSS.Transform.toString(transform),
    transition,
    ...(isDragging ? s.dragging : null),
  };
  return (
    <ContextDocRow
      ref={setNodeRef}
      style={style}
      {...props}
      handle={
        <button
          type="button"
          {...attributes}
          {...listeners}
          disabled={disabled}
          aria-label={t("picker.dragHandle", { name: props.row.path })}
          style={s.handle(disabled)}
        >
          <Icon.Menu size={14} />
        </button>
      }
    />
  );
}

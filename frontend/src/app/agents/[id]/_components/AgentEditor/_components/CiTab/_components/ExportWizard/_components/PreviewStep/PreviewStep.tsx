/* PreviewStep — the files the export will create and the selected file's contents. Only the
   workflow is editable; the runner bundle is listed by path and size, never rendered (FR10).
   File contents are plain text, never markup. */
"use client";

import React from "react";
import { useTranslations } from "next-intl";
import { Badge, Button, SectionLabel, Skeleton } from "@devdigest/ui";
import type { CiFile } from "@devdigest/shared";
import { byteSize, formatBytes, isRunnerBundle, type WizardError } from "../../helpers";
import { WizardErrorBox } from "../WizardErrorBox";
import { s } from "./styles";

export function PreviewStep({
  loading,
  error,
  noTriggers,
  files,
  selected,
  edited,
  onSelect,
  onEdit,
  onReset,
}: {
  loading: boolean;
  error: WizardError | null;
  noTriggers: boolean;
  files: readonly CiFile[] | undefined;
  selected: CiFile | undefined;
  /** True once the user changed the workflow. */
  edited: boolean;
  onSelect: (path: string) => void;
  onEdit: (contents: string) => void;
  onReset: () => void;
}) {
  const t = useTranslations("ci.exportWizard");
  const bundleBytes = React.useMemo(
    () => (selected && isRunnerBundle(selected.path) ? byteSize(selected.contents) : null),
    [selected],
  );

  if (noTriggers) {
    return (
      <div style={s.body}>
        <p role="alert" style={s.muted}>
          {t("triggerRequired")}
        </p>
      </div>
    );
  }
  if (error) {
    return (
      <div style={s.body}>
        <WizardErrorBox error={error} />
      </div>
    );
  }
  if (loading || !files || !selected) {
    return (
      <div style={s.body} role="status">
        <p style={{ ...s.muted, marginBottom: 12 }}>{t("generating")}</p>
        <Skeleton height={14} width={160} style={{ marginBottom: 12 }} />
        <Skeleton height={220} />
      </div>
    );
  }

  return (
    <div style={s.layout}>
      <nav aria-label={t("filesToCreate")} style={s.list}>
        <SectionLabel>{t("filesToCreate")}</SectionLabel>
        <ul style={s.items}>
          {files.map((f) => (
            <li key={f.path}>
              <button
                type="button"
                aria-current={f.path === selected.path ? "true" : undefined}
                onClick={() => onSelect(f.path)}
                style={f.path === selected.path ? { ...s.item, ...s.itemActive } : s.item}
              >
                <span className="mono" style={s.itemPath}>
                  {f.path}
                </span>
                {f.editable && (
                  <Badge color="var(--accent-text)" bg="var(--accent-bg)">
                    {edited ? t("edited") : t("editable")}
                  </Badge>
                )}
              </button>
            </li>
          ))}
        </ul>
      </nav>

      <div style={s.viewer}>
        <div style={s.viewerHead}>
          <span className="mono" style={s.viewerPath}>
            {selected.path}
          </span>
          {selected.editable && edited && (
            <Button kind="ghost" size="sm" onClick={onReset}>
              {t("resetToGenerated")}
            </Button>
          )}
        </div>
        {bundleBytes != null ? (
          <p style={s.muted}>{t("bundleNote", { size: formatBytes(bundleBytes) })}</p>
        ) : (
          <textarea
            className="mono"
            style={s.text}
            value={selected.contents}
            rows={18}
            readOnly={!selected.editable}
            spellCheck={false}
            aria-label={selected.editable ? t("workflowEditorLabel") : selected.path}
            onChange={selected.editable ? (e) => onEdit(e.target.value) : undefined}
          />
        )}
      </div>
    </div>
  );
}

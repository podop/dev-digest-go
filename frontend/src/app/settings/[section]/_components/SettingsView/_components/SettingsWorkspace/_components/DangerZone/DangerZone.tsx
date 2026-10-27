/* DangerZone — Settings › Workspace › Danger zone (FR18): "Export workspace" (a file
   download, disabled while in flight, AC20) and "Reset run history", which opens the
   typed-confirm dialog. */
"use client";

import React from "react";
import { useTranslations } from "next-intl";
import { Button } from "@devdigest/ui";
import { useExportWorkspace, useWorkspace } from "@/lib/hooks";
import { useToast } from "@/lib/toast";
import { ResetHistoryDialog } from "../ResetHistoryDialog";
import { s } from "./styles";

export function DangerZone() {
  const t = useTranslations("workspaceDanger");
  const toast = useToast();
  const workspace = useWorkspace();
  const exporter = useExportWorkspace();
  const titleId = React.useId();
  const reasonId = React.useId();
  const [resetOpen, setResetOpen] = React.useState(false);

  const name = workspace.data?.name;

  const exportWorkspace = () => {
    if (exporter.isPending) return;
    // A failed export toasts through the global mutation error handler.
    exporter.mutate(undefined, { onSuccess: ({ fileName }) => toast.success(t("export.done", { fileName })) });
  };

  return (
    <section aria-labelledby={titleId} style={s.card}>
      <h3 id={titleId} style={s.title}>
        {t("title")}
      </h3>
      <p style={s.body}>{t("body")}</p>
      <p style={s.note}>{t("runHistoryNote")}</p>
      <div style={s.actions}>
        <Button type="button" kind="secondary" size="sm" icon="Upload" onClick={exportWorkspace} disabled={exporter.isPending}>
          {exporter.isPending ? t("export.exporting") : t("export.label")}
        </Button>
        <Button
          type="button"
          kind="secondary"
          size="sm"
          icon="Trash"
          onClick={() => setResetOpen(true)}
          disabled={name === undefined}
          aria-describedby={name === undefined ? reasonId : undefined}
        >
          {t("reset.label")}
        </Button>
      </div>
      {name === undefined && (
        <div id={reasonId} style={s.reason}>
          {t("reset.nameUnavailable")}
        </div>
      )}
      {resetOpen && name !== undefined && <ResetHistoryDialog workspaceName={name} onClose={() => setResetOpen(false)} />}
    </section>
  );
}

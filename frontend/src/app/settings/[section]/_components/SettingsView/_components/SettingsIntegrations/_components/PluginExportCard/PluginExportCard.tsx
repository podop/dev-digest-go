/* PluginExportCard — Settings › Integrations › "Export workspace as plugin" (FR15, EC15, §9).
   Owns the export mutation so the button reads "Exporting…" while the zip is built, whatever
   the dialog does. Disabled with a hint when the workspace has no agents and no skills; the
   "Last export" line comes from GET /plugins and is absent when nothing was exported. */
"use client";

import React from "react";
import { useTranslations } from "next-intl";
import { Button, Icon } from "@devdigest/ui";
import { useAgents, useExportPlugin, usePlugins, useSkills } from "@/lib/hooks";
import { PluginExportDialog } from "../PluginExportDialog";
import { s } from "./styles";

export function PluginExportCard() {
  const t = useTranslations("integrationsExport.card");
  const plugins = usePlugins();
  const agents = useAgents();
  const skills = useSkills();
  const exporter = useExportPlugin();
  const titleId = React.useId();
  const hintId = React.useId();
  const [open, setOpen] = React.useState(false);

  const empty = agents.data?.length === 0 && skills.data?.length === 0;
  const last = plugins.data?.last_export ?? null;

  const close = () => {
    exporter.reset();
    setOpen(false);
  };

  return (
    <section aria-labelledby={titleId} style={s.card}>
      <div style={s.head}>
        <div style={s.headIcon} aria-hidden>
          <Icon.Upload size={18} />
        </div>
        <div style={s.headText}>
          <h3 id={titleId} style={s.title}>
            {t("title")}
          </h3>
          <p style={s.body}>{t("body")}</p>
        </div>
      </div>
      <div style={s.actions}>
        <Button
          type="button"
          kind="secondary"
          size="sm"
          icon="Boxes"
          onClick={() => setOpen(true)}
          disabled={empty || exporter.isPending}
          aria-describedby={empty ? hintId : undefined}
        >
          {exporter.isPending ? t("busy") : t("button")}
        </Button>
      </div>
      {empty && (
        <div id={hintId} style={s.hint}>
          {t("emptyHint")}
        </div>
      )}
      {last && (
        <p style={s.last}>
          <span>{t("lastExport.label")}</span>{" "}
          <span title={last.file_name} className="mono" style={s.file}>
            {last.file_name}
          </span>{" "}
          <span>{t("lastExport.counts", { skills: last.counts.skills, agents: last.counts.agents })}</span>
        </p>
      )}
      {open && (
        <PluginExportDialog
          onClose={close}
          onExport={exporter.mutateAsync}
          pending={exporter.isPending}
          error={exporter.error}
        />
      )}
    </section>
  );
}

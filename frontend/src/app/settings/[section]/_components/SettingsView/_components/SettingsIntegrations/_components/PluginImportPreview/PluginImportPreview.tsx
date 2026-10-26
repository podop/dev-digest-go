/* PluginImportPreview — what a dry run found, before anything is installed (FR16, AC19):
   counts, agents with their skills, skills, ignored parts, warnings and conflicts. Install is
   disabled while a conflict exists. The region takes focus when it opens and Escape cancels
   (NFR5); the card gives focus back to "Browse files…". */
"use client";

import React from "react";
import { useTranslations } from "next-intl";
import type { PluginImportPreview as Preview } from "@devdigest/shared";
import { Button } from "@devdigest/ui";
import type { ImportConflicts } from "../PluginImportCard/helpers";
import { hasConflicts } from "../PluginImportCard/helpers";
import { s } from "./styles";

export interface PluginImportPreviewProps {
  preview: Preview;
  /** The preview's conflicts, or the ones a 409 on install reported. */
  conflicts: ImportConflicts;
  installing: boolean;
  /** An install failure shown above the buttons. */
  notice?: React.ReactNode;
  onInstall: () => void;
  onCancel: () => void;
}

function Name({ value }: { value: string }) {
  return (
    <span style={s.name} title={value}>
      {value}
    </span>
  );
}

export function PluginImportPreview({ preview, conflicts, installing, notice, onInstall, onCancel }: PluginImportPreviewProps) {
  const t = useTranslations("integrationsImport.preview");
  const titleId = React.useId();
  const ref = React.useRef<HTMLElement>(null);
  const blocked = hasConflicts(conflicts);
  const { counts, manifest } = preview;

  React.useEffect(() => {
    ref.current?.focus();
  }, []);

  const onKeyDown = (e: React.KeyboardEvent) => {
    if (e.key !== "Escape" || e.defaultPrevented || installing) return;
    e.preventDefault();
    onCancel();
  };

  return (
    <section ref={ref} tabIndex={-1} aria-labelledby={titleId} onKeyDown={onKeyDown} style={s.root}>
      <h4 id={titleId} style={s.title}>
        {t("title")} <Name value={manifest.name} />
      </h4>
      <div style={s.meta}>
        {manifest.version && <span>{t("version", { version: manifest.version })}</span>}
        <span>{preview.kind === "workspace" ? t("kindWorkspace") : t("kindPlugin")}</span>
      </div>
      {manifest.description && <p style={s.description}>{manifest.description}</p>}
      <p style={s.counts}>
        {t("counts", {
          agents: counts.agents,
          skills: counts.skills,
          evalCases: counts.eval_cases,
          learnings: counts.learnings,
        })}
      </p>
      {counts.conventions > 0 && <p style={s.note}>{t("conventionsNotInstalled", { count: counts.conventions })}</p>}

      {blocked && (
        <div role="alert" style={s.conflicts}>
          <div style={s.heading}>{t("conflictsHeading")}</div>
          {conflicts.plugin && (
            <div>{t("conflictPlugin", { name: manifest.name })}</div>
          )}
          {conflicts.skills.length > 0 && (
            <div>{t("conflictSkills", { skills: conflicts.skills.join(", ") })}</div>
          )}
          <div style={s.note}>{t("conflictHelp")}</div>
        </div>
      )}

      {preview.agents.length > 0 && (
        <>
          <div style={s.heading}>{t("agentsHeading")}</div>
          <ul style={s.list}>
            {preview.agents.map((agent) => (
              <li key={agent.name} style={s.item}>
                <Name value={agent.name} />
                <span style={s.sub}>
                  {agent.skills.length > 0 ? t("agentSkills", { skills: agent.skills.join(", ") }) : t("agentNoSkills")}
                </span>
              </li>
            ))}
          </ul>
        </>
      )}

      {preview.skills.length > 0 && (
        <>
          <div style={s.heading}>{t("skillsHeading")}</div>
          <ul style={s.list}>
            {preview.skills.map((skill) => (
              <li key={skill.name} style={s.item}>
                <Name value={skill.name} />
              </li>
            ))}
          </ul>
        </>
      )}

      {preview.ignored.length > 0 && (
        <>
          <div style={s.heading}>{t("ignoredHeading")}</div>
          <ul style={s.list}>
            {preview.ignored.map((part) => (
              <li key={`${part.path_or_section}\u0000${part.reason}`} style={s.sub}>
                {t("ignoredItem", { part: part.path_or_section, reason: part.reason })}
              </li>
            ))}
          </ul>
        </>
      )}

      {preview.warnings.length > 0 && (
        <>
          <div style={s.heading}>{t("warningsHeading")}</div>
          <ul style={s.list}>
            {[...new Set(preview.warnings)].map((warning) => (
              <li key={warning} style={s.warning}>
                {warning}
              </li>
            ))}
          </ul>
        </>
      )}

      {notice}
      <div style={s.actions}>
        <Button type="button" kind="ghost" onClick={onCancel} disabled={installing}>
          {t("cancel")}
        </Button>
        <Button type="button" kind="primary" onClick={onInstall} disabled={blocked || installing}>
          {installing ? t("installing") : t("install")}
        </Button>
      </div>
    </section>
  );
}

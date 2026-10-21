/* VersionsTab — version history, newest first (mockup Skill Editor · Versions):
   a header with the version count, then one card per version — version tile,
   message over the date, and either the "Current" pill or Diff + Restore. Diff
   expands a line diff (jsdiff) of that version's body against the current one.
   Restore asks for confirmation and writes vK as a new version; it is disabled
   while the editor has unsaved changes (the restore would silently drop them). */
"use client";

import React from "react";
import { useTranslations } from "next-intl";
import { Badge, Button, ErrorState, Skeleton } from "@devdigest/ui";
import type { Skill, SkillVersion } from "@devdigest/shared";
import { useRestoreSkillVersion, useSkillVersions } from "@/lib/hooks";
import { useToast } from "@/lib/toast";
import { formatDay, formatWhen, hasChanges, lineDiff } from "./helpers";
import { s } from "./styles";

export function VersionsTab({ skill, dirty }: { skill: Skill; dirty: boolean }) {
  const t = useTranslations("skills");
  const toast = useToast();
  const { data: versions, isLoading, isError, refetch } = useSkillVersions(skill.id);
  const restore = useRestoreSkillVersion();
  const [open, setOpen] = React.useState<number | null>(null);

  const onRestore = (v: SkillVersion) => {
    if (!window.confirm(t("versions.restoreConfirm", { version: v.version }))) return;
    restore.mutate(
      { id: skill.id, version: v.version },
      { onSuccess: (saved) => toast.success(t("versions.restored", { from: v.version, version: saved.version })) },
    );
  };

  if (isLoading) return <Skeleton height={160} />;
  if (isError) return <ErrorState title={t("versions.loadError")} onRetry={() => refetch()} />;
  const list = versions ?? [];

  return (
    <div style={s.wrap}>
      <div style={s.titleRow}>
        <h2 style={s.h2}>{t("versions.title")}</h2>
        <span className="tnum" style={s.count}>
          {t("versions.count", { count: list.length })}
        </span>
      </div>
      <p style={s.caption}>{t("versions.caption")}</p>
      {list.length === 0 && <p style={s.caption}>{t("versions.empty")}</p>}
      <ul style={s.list}>
        {list.map((v) => {
          const current = v.version === skill.version;
          const expanded = open === v.version && !current;
          const diff = expanded ? lineDiff(v.body, skill.body) : [];
          return (
            <li key={v.version} style={s.row(current)} data-testid={`version-v${v.version}`}>
              <div style={s.rowHead}>
                <span className="mono tnum" style={s.version(current)}>
                  v{v.version}
                </span>
                <div style={s.text}>
                  <span style={s.message}>{v.message || t("versions.noMessage")}</span>
                  <span className="tnum" style={s.date} title={formatWhen(v.created_at)}>
                    {formatDay(v.created_at)}
                  </span>
                </div>
                {current ? (
                  <Badge color="var(--ok)" bg="var(--ok-bg)" dot>
                    {t("versions.current")}
                  </Badge>
                ) : (
                  <div style={s.actions}>
                    <Button
                      kind="ghost"
                      size="sm"
                      icon="Eye"
                      aria-expanded={expanded}
                      title={t("versions.showDiff")}
                      onClick={() => setOpen(expanded ? null : v.version)}
                      style={s.diffButton(expanded)}
                    >
                      {t("versions.diff")}
                    </Button>
                    <span title={dirty ? t("versions.restoreDirty") : undefined}>
                      <Button
                        kind="secondary"
                        size="sm"
                        icon="History"
                        onClick={() => onRestore(v)}
                        disabled={dirty || restore.isPending}
                      >
                        {restore.isPending && restore.variables?.version === v.version
                          ? t("versions.restoring")
                          : t("versions.restore")}
                      </Button>
                    </span>
                  </div>
                )}
              </div>
              {expanded && (
                <div style={s.diff}>
                  {hasChanges(diff) ? (
                    diff.map((line, i) => (
                      <div key={`${i}-${line.kind}`} className="mono" data-kind={line.kind} style={s.diffLine(line.kind)}>
                        <span style={s.sign}>{line.kind === "add" ? "+" : line.kind === "del" ? "-" : " "}</span>
                        {line.text}
                      </div>
                    ))
                  ) : (
                    <p style={s.caption}>{t("versions.noDiff")}</p>
                  )}
                </div>
              )}
            </li>
          );
        })}
      </ul>
    </div>
  );
}

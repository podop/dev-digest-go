/* HotFiles — the leaderboard's "Hot files" card (FR22, mockup): mono path, repo and #numbers on the
   left, "N findings" and a red "N critical" on the right; rows with criticals get a reddish background. */
"use client";

import { useTranslations } from "next-intl";
import type { LeaderboardHotFile } from "@devdigest/shared";
import { Card } from "@devdigest/ui";
import { s } from "./styles";

export interface HotFilesProps {
  files: readonly LeaderboardHotFile[];
}

export function HotFiles({ files }: HotFilesProps) {
  const t = useTranslations("leaderboardPanels.hotFiles");
  return (
    <Card style={s.card}>
      <div style={s.header}>
        <h3 style={s.title}>{t("title")}</h3>
        <span style={s.subtitle}>{t("subtitle")}</span>
      </div>
      {files.length === 0 ? (
        <div style={s.empty}>
          <strong style={s.emptyTitle}>{t("emptyTitle")}</strong>
          <span>{t("emptyBody")}</span>
        </div>
      ) : (
        <ul style={s.list} aria-label={t("title")}>
          {files.map((f) => (
            <li
              key={`${f.repo}:${f.path}`}
              data-series={f.path}
              data-critical={f.critical > 0 ? "true" : undefined}
              style={f.critical > 0 ? { ...s.row, ...s.rowCritical } : s.row}
            >
              <div style={s.info}>
                <span className="mono" style={s.path} title={f.path}>
                  {f.path}
                </span>
                <span style={s.location}>{t("location", { repo: f.repo, prs: f.prs.map((n) => `#${n}`).join(", ") })}</span>
              </div>
              <div style={s.counts}>
                <span className="tnum" style={s.findings}>
                  {t("findings", { count: f.findings })}
                </span>
                {f.critical > 0 && (
                  <span className="tnum" style={s.critical}>
                    {t("critical", { count: f.critical })}
                  </span>
                )}
              </div>
            </li>
          ))}
        </ul>
      )}
    </Card>
  );
}

/* HotFiles — the leaderboard's "Hot files" card (FR22, mockup): mono path, repo (with its host on GitLab) and #numbers (!numbers on GitLab) (links to the PR, FR26) on the
   left, "N findings" and a red "N critical" on the right; rows with criticals get a reddish background. */
"use client";

import React from "react";
import Link from "next/link";
import { useTranslations } from "next-intl";
import type { LeaderboardHotFile } from "@devdigest/shared";
import { Card } from "@devdigest/ui";
import { prDetailPath } from "@/lib/pr-urls";
import { prRef } from "@/lib/code-host";
import { NO_REPOS, repoNameLabel, type RepoLookup } from "../../helpers";
import { s } from "./styles";

export interface HotFilesProps {
  files: readonly LeaderboardHotFile[];
  /** The response's repositories by id (GitLab references and host labels). */
  repos?: RepoLookup;
}

export function HotFiles({ files, repos = NO_REPOS }: HotFilesProps) {
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
                <span style={s.location}>
                  {t("location", { repo: repoNameLabel(repos, f.repo_id, f.repo) })}{" "}
                  {f.prs.map((n, i) => (
                    <React.Fragment key={n}>
                      {i > 0 && ", "}
                      {t.rich("prLink", {
                        ref: prRef(repos.get(f.repo_id), n),
                        link: (chunks) => (
                          <Link href={prDetailPath(encodeURIComponent(f.repo_id), n)} style={s.prLink}>
                            {chunks}
                          </Link>
                        ),
                      })}
                    </React.Fragment>
                  ))}
                </span>
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

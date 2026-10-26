/* GithubActionCard — Settings › Integrations: where the workspace's agents run as a GitHub
   Action (FR2). Skeleton / empty / error + Retry / one row per repository. The status UI is
   gated on `data`, not on isError, so a failed background refetch keeps the rows. */
"use client";

import React from "react";
import { useTranslations } from "next-intl";
import { Button, Card, ErrorState, Skeleton } from "@devdigest/ui";
import { ApiError } from "@/lib/api";
import { useWorkspaceCiInstallations } from "@/lib/hooks";
import { InstallActionDialog } from "../InstallActionDialog";
import { RepoRow } from "./_components/RepoRow";
import { s } from "./styles";

export function GithubActionCard() {
  const t = useTranslations("integrationsGithubAction.card");
  const repos = useWorkspaceCiInstallations();
  const [installing, setInstalling] = React.useState(false);
  const list = repos.data;

  return (
    <Card style={s.card}>
      <div style={s.head}>
        <div style={s.headText}>
          <div style={s.titleRow}>
            <h3 id="gha-card-title" style={s.title}>
              {t("title")}
            </h3>
            {list && <span style={s.count}>{t("repoCount", { count: list.length })}</span>}
          </div>
          <div style={s.subtitle}>{t("subtitle")}</div>
        </div>
        <Button type="button" kind="secondary" size="sm" icon="Plus" onClick={() => setInstalling(true)}>
          {t("install")}
        </Button>
      </div>

      {list ? (
        list.length === 0 ? (
          <div style={s.empty}>{t("empty")}</div>
        ) : (
          <ul aria-labelledby="gha-card-title" style={s.list}>
            {list.map((entry) => (
              <RepoRow key={entry.repo} entry={entry} />
            ))}
          </ul>
        )
      ) : repos.isError ? (
        <ErrorState
          title={t("loadError")}
          body={repos.error instanceof ApiError ? repos.error.message : undefined}
          onRetry={() => void repos.refetch()} retryLabel={t("retry")}
        />
      ) : (
        <div role="status" aria-label={t("loading")}>
          {[0, 1].map((i) => (
            <div key={i} style={s.skeletonRow}>
              <Skeleton height={16} />
            </div>
          ))}
        </div>
      )}

      {installing && <InstallActionDialog onClose={() => setInstalling(false)} />}
    </Card>
  );
}

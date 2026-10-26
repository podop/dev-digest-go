"use client";

import { useTranslations } from "next-intl";
import { Button, Icon } from "@devdigest/ui";
import { isNotConnected } from "@/lib/code-host";
import { useRepoIntelStatus } from "@/lib/hooks";
import type { Repo } from "@/lib/types";
import { indexedAge, isNotIndexed } from "../../../SettingsGithub/helpers";
import { s } from "./styles";

/** One GitLab repository (FR37): full name, host, default branch, index state and, when no connection serves its host, "Not connected". */
export function GitLabRepoRow({ repo, onRemove }: { repo: Repo; onRemove: () => void }) {
  const t = useTranslations("gitlabIntegration.repos");
  const index = useRepoIntelStatus(repo.id);

  let indexText: string | null = null;
  if (index.data) {
    if (isNotIndexed(index.data)) indexText = t("notIndexed");
    else {
      const age = indexedAge(index.data.updatedAt);
      indexText = age.unit === "justNow" ? t("indexed.justNow") : t(`indexed.${age.unit}`, { count: age.count });
    }
  } else if (index.isError) {
    indexText = t("indexUnknown");
  }
  const notConnected = isNotConnected(repo);

  return (
    <li style={s.row}>
      <div style={s.main}>
        <div style={s.name}>{repo.full_name}</div>
        <div style={s.meta}>
          {repo.host && <span style={s.mono}>{repo.host}</span>}
          <span style={s.mono}>{repo.default_branch}</span>
          {indexText && <span>{indexText}</span>}
          {notConnected && (
            <span style={s.badge}>
              <Icon.AlertTriangle size={12} />
              {t("notConnected")}
            </span>
          )}
        </div>
        {notConnected && <div style={s.hint}>{t("notConnectedHint", { host: repo.host ?? "" })}</div>}
      </div>
      <Button type="button" kind="ghost" size="sm" onClick={onRemove} aria-label={t("removeLabel", { repo: repo.full_name })}>
        {t("remove")}
      </Button>
    </li>
  );
}

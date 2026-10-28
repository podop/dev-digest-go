"use client";

import { useTranslations } from "next-intl";
import { Icon } from "@devdigest/ui";
import { KitBadge, ListRow, MonoAction, RowMeta } from "@/components/settings-kit";
import { isNotConnected } from "@/lib/code-host";
import { hasNoSourceFiles, useRepoIntelStatus } from "@/lib/hooks";
import type { Repo } from "@/lib/types";
import { indexedAge, isNotIndexed } from "../../../RepoIndex";
import { s } from "./styles";

/** One GitLab repository (FR37), as the prototype's repository row: name, host, branch badge, index state, Remove and,
 *  when no connection serves its host, "Not connected" with its hint. */
export function GitLabRepoRow({ repo, onRemove }: { repo: Repo; onRemove: () => void }) {
  const t = useTranslations("gitlabIntegration.repos");
  const tc = useTranslations("common");
  const index = useRepoIntelStatus(repo.id);
  const noSourceFiles = hasNoSourceFiles(index.data);

  let indexText: string | null = null;
  if (index.data) {
    if (noSourceFiles) indexText = t("noSourceFiles");
    else if (isNotIndexed(index.data)) indexText = t("notIndexed");
    else {
      const age = indexedAge(index.data.updatedAt);
      indexText = age.unit === "justNow" ? t("indexed.justNow") : t(`indexed.${age.unit}`, { count: age.count });
    }
  } else if (index.isError) {
    indexText = t("indexUnknown");
  }
  const notConnected = isNotConnected(repo);

  return (
    <li style={s.item}>
      <ListRow name={repo.full_name}>
        {repo.host && (
          <span className="mono" style={s.host}>
            {repo.host}
          </span>
        )}
        <KitBadge>{repo.default_branch}</KitBadge>
        {indexText && <RowMeta>{indexText}</RowMeta>}
        {notConnected && (
          <span style={s.badge}>
            <Icon.AlertTriangle size={12} aria-hidden />
            {t("notConnected")}
          </span>
        )}
        <MonoAction onClick={onRemove} aria-label={t("removeLabel", { repo: repo.full_name })}>
          {t("remove")}
        </MonoAction>
      </ListRow>
      {noSourceFiles && <div style={s.hint}>{tc("repoIntel.supportedLanguages")}</div>}
      {notConnected && <div style={s.hint}>{t("notConnectedHint", { host: repo.host ?? "" })}</div>}
    </li>
  );
}

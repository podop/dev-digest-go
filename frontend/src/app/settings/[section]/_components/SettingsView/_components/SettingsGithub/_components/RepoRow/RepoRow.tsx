"use client";

import { useTranslations } from "next-intl";
import { KitBadge, ListRow, MonoAction, RowMeta } from "@/components/settings-kit";
import { hasNoSourceFiles, useRepoIntelStatus } from "@/lib/hooks";
import type { Repo } from "@/lib/types";
import { indexedAge, isNotIndexed } from "../../../RepoIndex";
import { s } from "./styles";

/** One repository (prototype row): branch icon, mono name, branch badge, index state and Remove (A4). */
export function RepoRow({ repo, onRemove }: { repo: Repo; onRemove: () => void }) {
  const t = useTranslations("githubIntegration.repos");
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

  return (
    <li style={s.item}>
      <ListRow name={repo.full_name}>
        <KitBadge>{repo.default_branch}</KitBadge>
        {indexText && <RowMeta>{indexText}</RowMeta>}
        <MonoAction onClick={onRemove} aria-label={t("removeLabel", { repo: repo.full_name })}>
          {t("remove")}
        </MonoAction>
      </ListRow>
      {noSourceFiles && <div style={s.hint}>{tc("repoIntel.supportedLanguages")}</div>}
    </li>
  );
}

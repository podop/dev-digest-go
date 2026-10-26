"use client";

import { useTranslations } from "next-intl";
import { Button } from "@devdigest/ui";
import { useRepoIntelStatus } from "@/lib/hooks";
import type { Repo } from "@/lib/types";
import { indexedAge, isNotIndexed } from "../../../RepoIndex";
import { s } from "./styles";

/** One repository: full name, default branch and its index state (A4). */
export function RepoRow({ repo, onRemove }: { repo: Repo; onRemove: () => void }) {
  const t = useTranslations("githubIntegration.repos");
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

  return (
    <li style={s.row}>
      <div style={s.main}>
        <div style={s.name}>{repo.full_name}</div>
        <div style={s.meta}>
          <span style={s.branch}>{repo.default_branch}</span>
          {indexText && <span>{indexText}</span>}
        </div>
      </div>
      <Button kind="ghost" size="sm" onClick={onRemove} aria-label={t("removeLabel", { repo: repo.full_name })}>
        {t("remove")}
      </Button>
    </li>
  );
}

"use client";

import { useTranslations } from "next-intl";
import { Icon } from "@devdigest/ui";
import { hasNoSourceFiles, useRepoIndexWait } from "@/lib/hooks";
import type { Repo } from "@/lib/types";
import { INDEX_POLL_MAX_MS, INDEX_POLL_MS } from "../../constants";
import { s } from "./styles";

/** FR25: the index status of a just-added repo, refreshed every 3 s until it has one. After 5 minutes
 *  without one the polling stops and the note says indexing continues in the background (EC16). */
export function IndexProgress({ repo }: { repo: Repo }) {
  const t = useTranslations("setup");
  const tc = useTranslations("common");
  const { state, indexed, timedOut } = useRepoIndexWait(repo.id, INDEX_POLL_MS, INDEX_POLL_MAX_MS);

  return (
    <div style={s.box(Boolean(indexed && state && !hasNoSourceFiles(state)))} aria-live="polite">
      {indexed && state && hasNoSourceFiles(state) ? (
        <>
          <div style={s.line}>
            <Icon.AlertTriangle size={15} style={s.icon(false)} aria-hidden />
            {t("repo.noSourceFiles", { name: repo.full_name })}
          </div>
          <div style={s.detail}>{tc("repoIntel.supportedLanguages")}</div>
        </>
      ) : indexed && state ? (
        <>
          <div style={s.line}>
            <Icon.CheckCircle size={15} style={s.icon(true)} aria-hidden />
            {t("repo.indexed", { name: repo.full_name })}
          </div>
          <div style={s.detail}>{t(`repo.indexStatus.${state.status}`)}</div>
        </>
      ) : (
        <>
          <div style={s.line}>
            <Icon.Clock size={15} style={s.icon(false)} aria-hidden />
            {t("repo.indexing", { name: repo.full_name })}
          </div>
          {timedOut && <div style={s.detail}>{t("repo.background")}</div>}
        </>
      )}
    </div>
  );
}

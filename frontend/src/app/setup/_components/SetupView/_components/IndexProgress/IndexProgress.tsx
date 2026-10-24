"use client";

import { useTranslations } from "next-intl";
import { Icon } from "@devdigest/ui";
import { useRepoIndexWait } from "@/lib/hooks";
import type { Repo } from "@/lib/types";
import { INDEX_POLL_MAX_MS, INDEX_POLL_MS } from "../../constants";
import { s } from "./styles";

/** FR25: the index status of a just-added repo, refreshed every 3 s until it has one. After 5 minutes
 *  without one the polling stops and the note says indexing continues in the background (EC16). */
export function IndexProgress({ repo }: { repo: Repo }) {
  const t = useTranslations("setup");
  const { state, indexed, timedOut } = useRepoIndexWait(repo.id, INDEX_POLL_MS, INDEX_POLL_MAX_MS);

  return (
    <div style={s.box} aria-live="polite">
      {indexed && state ? (
        <>
          <div style={{ ...s.line, ...s.ok }}>
            <Icon.CheckCircle size={14} />
            {t("repo.indexed", { name: repo.full_name })}
          </div>
          <div>{t(`repo.indexStatus.${state.status}`)}</div>
        </>
      ) : (
        <>
          <div style={s.line}>
            <Icon.Clock size={14} />
            {t("repo.indexing", { name: repo.full_name })}
          </div>
          {timedOut && <div>{t("repo.background")}</div>}
        </>
      )}
    </div>
  );
}

"use client";

import { useTranslations } from "next-intl";
import { Button, Skeleton } from "@devdigest/ui";
import { useWorkspaceStats } from "@/lib/hooks";
import { STAT_TILES } from "./constants";
import { s } from "./styles";

const COUNT_LOCALE = "en-US";

/**
 * The six workspace counts (FR12) from GET /workspace/stats. Loading shows skeleton bars
 * (no digits); a failed request shows "—" in every tile plus Retry (EC15) and leaves the
 * rest of the section alone.
 */
export function WorkspaceStats() {
  const t = useTranslations("workspaceStats");
  const { data, isPending, isError, isFetching, refetch } = useWorkspaceStats();

  return (
    <section aria-label={isPending ? t("loading") : t("groupLabel")} aria-busy={isPending} style={s.wrap}>
      <dl style={s.grid}>
        {STAT_TILES.map(({ key, field }) => (
          <div key={key} style={s.tile}>
            <dt style={s.label}>{t(`tiles.${key}`)}</dt>
            <dd style={s.value}>
              {data ? (
                data[field].toLocaleString(COUNT_LOCALE)
              ) : isError ? (
                t("unavailable")
              ) : (
                <Skeleton width={48} height={22} />
              )}
            </dd>
          </div>
        ))}
      </dl>
      {isError && !data && (
        <div role="alert" style={s.error}>
          <span>{t("loadError")}</span>
          <Button size="sm" disabled={isFetching} onClick={() => refetch()}>
            {t("retry")}
          </Button>
        </div>
      )}
    </section>
  );
}

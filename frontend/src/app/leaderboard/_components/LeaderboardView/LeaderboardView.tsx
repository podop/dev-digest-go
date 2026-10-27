/* LeaderboardView — /leaderboard: how pull requests fared in review for a period and a repo filter.
   The URL is the only view state: `period` and `repo` come from the page (already normalized) and
   change through router.replace. A repo id that is not in the repo list falls back to "all" before
   any request (EC14). While loading or failed no number is rendered (no placeholder data).
   This file holds the shell (header + loading / error / empty states) and the composition of the
   loaded page: KPI tiles, award cards, the ranked table beside the side panels, then health by size
   next to the activity chart (FR19-FR22). A period without rows shows "No PRs were reviewed in this
   period" in the table's place, with a button to the PR list of the filtered (else the active) repo. */
"use client";

import React from "react";
import Link from "next/link";
import { useRouter } from "next/navigation";
import { useTranslations } from "next-intl";
import type { Leaderboard, LeaderboardPeriodKey } from "@devdigest/shared";
import { EmptyState, ErrorState, Skeleton } from "@devdigest/ui";
import { AppShell } from "@/components/app-shell";
import { useLeaderboard, useRepos } from "@/lib/hooks";
import { useActiveRepo } from "@/lib/repo-context";
import { AwardCards } from "./_components/AwardCards";
import { DailyActivity } from "./_components/DailyActivity";
import { Header } from "./_components/Header";
import { HealthBySize } from "./_components/HealthBySize";
import { HotFiles } from "./_components/HotFiles";
import { KpiTiles } from "./_components/KpiTiles";
import { LeaderboardTable } from "./_components/LeaderboardTable";
import { NeedsAttention } from "./_components/NeedsAttention";
import { ALL_REPOS } from "./constants";
import { leaderboardHref, repoLookup, resolveRepoFilter } from "./helpers";
import { s } from "./styles";

export interface LeaderboardViewProps {
  period: LeaderboardPeriodKey;
  /** "all" or a UUID-shaped repository id (see resolveRepoParam); may name a repository that no longer exists. */
  repo: string;
}

export function LeaderboardView({ period, repo }: LeaderboardViewProps) {
  const t = useTranslations("leaderboard");
  const tc = useTranslations("common");
  const router = useRouter();
  const reposQuery = useRepos();
  const { repoId: activeRepoId } = useActiveRepo();
  // A failed repo list cannot confirm an id, so the page shows every repository instead.
  const repos = reposQuery.isError ? [] : reposQuery.data;
  const filter = resolveRepoFilter(repo, repos);
  const noRepos = reposQuery.data?.length === 0;
  const query = useLeaderboard({ period, repo: filter ?? "all", enabled: filter !== null && !noRepos });

  // The PR list the empty state links to: the filtered repository, else the active one, else the first.
  const pullsRepoId = filter !== null && filter !== ALL_REPOS ? filter : (activeRepoId ?? repos?.[0]?.id ?? null);

  const loading = !noRepos && !query.isError && !query.data;

  return (
    <AppShell crumb={[{ label: t("crumb") }]}>
      <div style={s.page}>
        <Header
          period={period}
          repo={filter ?? "all"}
          repos={repos}
          window={query.data?.period ?? null}
          compared={query.data?.previous_summary != null}
          syncRepos={query.data?.repos ?? null}
          onPeriodChange={(next) => router.replace(leaderboardHref(next, filter ?? "all"))}
          onRepoChange={(next) => router.replace(leaderboardHref(period, next))}
        />

        {noRepos && (
          <EmptyState
            icon="Trophy"
            title={t("empty.title")}
            body={
              <>
                {t("empty.body")} <Link href="/onboarding">{t("empty.cta")}</Link>
              </>
            }
          />
        )}
        {loading && <LoadingSkeleton label={t("loading")} />}
        {query.isError && <ErrorState title={t("loadError")} onRetry={() => query.refetch()} retryLabel={tc("actions.retry")} />}
        {query.data && !noRepos && <LoadedPage data={query.data} pullsRepoId={pullsRepoId} />}
      </div>
    </AppShell>
  );
}

/** The loaded page; every section is given its slice of the response. */
function LoadedPage({ data, pullsRepoId }: { data: Leaderboard; pullsRepoId: string | null }) {
  const t = useTranslations("leaderboard");
  const router = useRouter();
  const repos = React.useMemo(() => repoLookup(data.repos), [data.repos]);
  return (
    <div style={s.loaded}>
      <KpiTiles summary={data.summary} previous={data.previous_summary} />
      <AwardCards awards={data.awards} repos={repos} />
      <div style={s.main}>
        <div style={s.mainWide}>
          {data.rows.length > 0 ? (
            <LeaderboardTable rows={data.rows} repos={repos} />
          ) : (
            <section style={s.tableEmpty}>
              <EmptyState
                icon="Trophy"
                title={t("tableEmpty.title")}
                body={t("tableEmpty.body")}
                cta={pullsRepoId ? t("tableEmpty.cta") : undefined}
                onCta={pullsRepoId ? () => router.push(`/repos/${encodeURIComponent(pullsRepoId)}/pulls`) : undefined}
              />
            </section>
          )}
        </div>
        <div style={s.mainSide}>
          <NeedsAttention data={data.needs_attention} repos={repos} />
          <HotFiles files={data.hot_files} repos={repos} />
        </div>
      </div>
      <div style={s.main}>
        <div style={s.half}>
          <HealthBySize buckets={data.health_by_size} />
        </div>
        <div style={s.half}>
          <DailyActivity activity={data.activity} />
        </div>
      </div>
    </div>
  );
}

function LoadingSkeleton({ label }: { label: string }) {
  return (
    <div role="status" aria-busy="true" aria-label={label} style={s.section}>
      <div style={s.skeletonTiles}>
        {[0, 1, 2, 3, 4, 5, 6, 7].map((i) => (
          <div key={i} style={s.skeletonTile}>
            <Skeleton height={52} />
          </div>
        ))}
      </div>
      <Skeleton height={260} />
      <Skeleton height={160} />
    </div>
  );
}

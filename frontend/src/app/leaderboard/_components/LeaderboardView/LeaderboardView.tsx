/* LeaderboardView — /leaderboard: how pull requests fared in review for a period and a repo filter.
   The URL is the only view state: `period` and `repo` come from the page (already normalized) and
   change through router.replace. A repo id that is not in the repo list falls back to "all" before
   any request (EC14). While loading or failed no number is rendered (no placeholder data).
   This file holds the shell: header + loading / error / empty states. The sections of the loaded
   page (KPIs, awards, table, side panels) are composed at the marked place below. */
"use client";

import Link from "next/link";
import { useRouter } from "next/navigation";
import { useTranslations } from "next-intl";
import type { LeaderboardPeriodKey } from "@devdigest/shared";
import { EmptyState, ErrorState, Skeleton } from "@devdigest/ui";
import { AppShell } from "@/components/app-shell";
import { useLeaderboard, useRepos } from "@/lib/hooks";
import { Header } from "./_components/Header";
import { leaderboardHref, resolveRepoFilter } from "./helpers";
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
  // A failed repo list cannot confirm an id, so the page shows every repository instead.
  const repos = reposQuery.isError ? [] : reposQuery.data;
  const filter = resolveRepoFilter(repo, repos);
  const noRepos = reposQuery.data?.length === 0;
  const query = useLeaderboard({ period, repo: filter ?? "all", enabled: filter !== null && !noRepos });

  const loading = !noRepos && !query.isError && !query.data;

  return (
    <AppShell crumb={[{ label: t("crumb") }]}>
      <div style={s.page}>
        <Header
          period={period}
          repo={filter ?? "all"}
          repos={repos}
          window={query.data?.period ?? null}
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
        {query.data && (
          <div style={s.section}>
            {/* Loaded page — composed here in this order, each section given the response (`query.data`):
                KPI tiles (summary, previous_summary), award cards (awards), ranked table (rows),
                side panels (needs_attention, hot_files), health by size (health_by_size), daily activity (daily). */}
          </div>
        )}
      </div>
    </AppShell>
  );
}

function LoadingSkeleton({ label }: { label: string }) {
  return (
    <div role="status" aria-busy="true" aria-label={label} style={s.section}>
      <div style={s.skeletonTiles}>
        {[0, 1, 2, 3].map((i) => (
          <div key={i} style={s.skeletonTile}>
            <Skeleton height={96} />
          </div>
        ))}
      </div>
      <Skeleton height={260} />
      <Skeleton height={160} />
    </div>
  );
}

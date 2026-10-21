/* MultiAgentListView — /multi-agent: the active repo's recent multi-agent runs, newest first
   (specs/multi-agent-review.md FR14, §9). A row opens its results; "New multi-agent review"
   opens the Configure run screen. */
"use client";

import React from "react";
import Link from "next/link";
import { useRouter } from "next/navigation";
import { useTranslations } from "next-intl";
import { Button, EmptyState, ErrorState, Skeleton } from "@devdigest/ui";
import { AppShell } from "@/components/app-shell";
import { CostText } from "@/components/cost-text";
import { useActiveRepo } from "@/lib/repo-context";
import { useRepoMultiAgentRuns } from "@/lib/hooks";
import { formatRanAt } from "./helpers";
import { s } from "./styles";

export function MultiAgentListView() {
  const t = useTranslations("runs.multiAgent.list");
  const tc = useTranslations("common");
  const router = useRouter();
  const { repoId, reposLoaded } = useActiveRepo();
  const runs = useRepoMultiAgentRuns(repoId);
  const newReview = () => router.push("/multi-agent/new");

  return (
    <AppShell crumb={[{ label: t("crumb") }]}>
      <div style={s.page}>
        <div style={s.header}>
          <div style={s.headerText}>
            <h1 style={s.h1}>{t("title")}</h1>
            <p style={s.subtitle}>{t("subtitle")}</p>
          </div>
          {repoId && (
            <Button kind="primary" icon="Plus" onClick={newReview}>
              {t("newReview")}
            </Button>
          )}
        </div>

        {reposLoaded && !repoId && (
          <EmptyState
            icon="GitBranch"
            title={t("noRepoTitle")}
            body={t("noRepoBody")}
            cta={t("noRepoCta")}
            onCta={() => router.push("/onboarding")}
          />
        )}
        {repoId && runs.isLoading && (
          <div style={s.skeletons}>
            <Skeleton height={48} />
            <Skeleton height={48} />
            <Skeleton height={48} />
          </div>
        )}
        {repoId && runs.isError && (
          <ErrorState title={t("loadError")} onRetry={() => runs.refetch()} retryLabel={tc("actions.retry")} />
        )}
        {runs.data && runs.data.length === 0 && (
          <EmptyState
            icon="Layers"
            title={t("emptyTitle")}
            body={t("emptyBody")}
            cta={t("newReview")}
            onCta={newReview}
          />
        )}
        {runs.data && runs.data.length > 0 && (
          <ul style={s.list} aria-label={t("runsLabel")}>
            {runs.data.map((run) => (
              <li key={run.id}>
                <Link href={`/multi-agent/${encodeURIComponent(run.id)}`} style={s.row}>
                  <span style={s.rowTitle}>
                    {run.pr_number != null
                      ? t("prLabel", { number: run.pr_number, title: run.pr_title ?? "" })
                      : t("unknownPr")}
                  </span>
                  {run.running_count > 0 && (
                    <span style={s.running}>
                      <span style={s.dot} aria-hidden />
                      {t("running", { count: run.running_count })}
                    </span>
                  )}
                  <span style={s.rowMeta}>{t("agents", { count: run.agent_count })}</span>
                  <span style={s.rowMeta}>{formatRanAt(run.ran_at)}</span>
                  <CostText usd={run.total_cost_usd} style={s.rowMeta} />
                </Link>
              </li>
            ))}
          </ul>
        )}
      </div>
    </AppShell>
  );
}

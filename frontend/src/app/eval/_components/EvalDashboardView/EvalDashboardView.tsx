/* EvalDashboardView — /eval: the Eval Dashboard. Without ?agent= it lists every agent
   with eval data (cards with latest metrics, sparkline), the 20 most recent runs across agents and
   "Run all agents" (FR23); with ?agent=<id> it shows that agent's view (FR24). The URL
   is the only view state: the page resolves ?agent= and ?period= and passes them down. */
"use client";

import React from "react";
import Link from "next/link";
import { useTranslations } from "next-intl";
import type { EvalPeriodId } from "@devdigest/shared/constants/evals";
import { Button, EmptyState, ErrorState, Icon, Skeleton } from "@devdigest/ui";
import { AppShell } from "@/components/app-shell";
import { useEvalOverview, useRunAllEvals } from "@/lib/hooks";
import { AgentEvalView } from "../AgentEvalView";
import { AgentsOverview } from "../AgentsOverview";
import { RecentRunsTable } from "./_components/RecentRunsTable";
import { isOverviewEmpty } from "./helpers";
import { s } from "./styles";

export function EvalDashboardView({ agentId, period }: { agentId: string | null; period: EvalPeriodId }) {
  const t = useTranslations("eval");
  return (
    <AppShell crumb={[{ label: t("page.crumbSkillsLab") }, { label: t("page.crumbEvalDashboard") }]}>
      <div style={s.page}>{agentId ? <AgentEvalView key={agentId} agentId={agentId} period={period} /> : <AllAgents />}</div>
    </AppShell>
  );
}

function AllAgents() {
  const t = useTranslations("eval.overview");
  const tc = useTranslations("common");
  const td = useTranslations("eval.dashboard");
  const overview = useEvalOverview();
  const runAll = useRunAllEvals();
  const data = overview.data;

  return (
    <>
      <div style={s.header}>
        <div style={s.headerText}>
          <h1 style={s.h1}>{td("defaultTitle")}</h1>
          <p style={s.subtitle}>{t("subtitle")}</p>
        </div>
        {runAll.data && (
          <span style={s.result}>{t("runAllResult", { started: runAll.data.started.length, skipped: runAll.data.skipped.length })}</span>
        )}
        <Button kind="primary" icon="Play" loading={runAll.isPending} disabled={runAll.isPending} onClick={() => runAll.mutate()}>
          {t("runAll")}
        </Button>
      </div>

      {overview.isLoading && <Skeleton height={220} />}
      {overview.isError && <ErrorState title={t("loadError")} onRetry={() => overview.refetch()} retryLabel={tc("actions.retry")} />}
      {data && isOverviewEmpty(data) && (
        <EmptyState
          icon="BarChart"
          title={t("emptyTitle")}
          body={
            <>
              {t("emptyBody")} <Link href="/agents">{t("emptyCta")}</Link>
            </>
          }
        />
      )}
      {data && !isOverviewEmpty(data) && (
        <>
          <section style={s.section} aria-label={t("agentsHeading")}>
            <h2 style={s.h2}>
              <Icon.Cpu size={14} aria-hidden />
              {t("agentsHeading")}
            </h2>
            <AgentsOverview agents={data.agents} />
          </section>
          <section style={s.section} aria-label={t("recentRunsHeading")}>
            <h2 style={s.h2}>
              <Icon.History size={14} aria-hidden />
              {t("recentRunsHeading")}
            </h2>
            {data.recent_runs.length > 0 ? <RecentRunsTable runs={data.recent_runs} /> : <p style={s.caption}>{t("noRecentRuns")}</p>}
          </section>
        </>
      )}
    </>
  );
}

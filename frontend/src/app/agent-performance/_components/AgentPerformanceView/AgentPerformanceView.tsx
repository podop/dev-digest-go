/* AgentPerformanceView — /agent-performance: one row per agent for a period (summary cards, sortable
   table with expandable rows, cost breakdown). The URL is the only view state: period, sort and dir
   come from the page and change through router.replace; the open row is local state in the table.
   While loading or failed no number is rendered (no placeholder data, no zeros). */
"use client";

import React from "react";
import Link from "next/link";
import { useRouter } from "next/navigation";
import { useTranslations } from "next-intl";
import type { AgentPerf } from "@devdigest/shared";
import { EmptyState, ErrorState, Skeleton } from "@devdigest/ui";
import { AppShell } from "@/components/app-shell";
import { PeriodPicker } from "@/components/stats-period-picker";
import { useAgentPerformance } from "@/lib/hooks";
import { formatPeriodRange, type StatsPeriod } from "@/lib/stats-period";
import { AgentTable } from "./_components/AgentTable";
import { CostBreakdown } from "./_components/CostBreakdown";
import { SummaryCards } from "./_components/SummaryCards";
import type { SortState } from "./constants";
import { dashboardHref, nextSort, sortRows } from "./helpers";
import { s } from "./styles";

export function AgentPerformanceView({ period, sort }: { period: StatsPeriod; sort: SortState }) {
  const t = useTranslations("agentPerformance");
  const tc = useTranslations("common");
  const router = useRouter();
  const query = useAgentPerformance(period);
  const periodLabel = formatPeriodRange(period, { "1d": t("dashboard.periodPhrase.1d"), "30d": t("dashboard.periodPhrase.30d") });

  return (
    <AppShell crumb={[{ label: t("dashboard.crumb") }]}>
      <div style={s.page}>
        <div style={s.header}>
          <div style={s.headerText}>
            <h1 style={s.h1}>{t("title")}</h1>
            <p style={s.subtitle}>{t("dashboard.subtitle")}</p>
          </div>
          <PeriodPicker period={period} onChange={(next) => router.replace(dashboardHref(next, sort))} />
        </div>

        {query.isLoading && <LoadingSkeleton label={t("dashboard.loading")} />}
        {query.isError && <ErrorState title={t("loadError")} onRetry={() => query.refetch()} retryLabel={tc("actions.retry")} />}
        {query.data && (
          <Loaded
            data={query.data}
            period={period}
            periodLabel={periodLabel}
            sort={sort}
            onSort={(key) => router.replace(dashboardHref(period, nextSort(sort, key)))}
          />
        )}
      </div>
    </AppShell>
  );
}

interface LoadedProps {
  data: AgentPerf;
  period: StatsPeriod;
  periodLabel: string;
  sort: SortState;
  onSort: (key: SortState["key"]) => void;
}

function Loaded({ data, period, periodLabel, sort, onSort }: LoadedProps) {
  const t = useTranslations("agentPerformance.dashboard");
  const hasAgents = data.agents.some((a) => a.agent_id !== null);

  if (!hasAgents && data.summary.runs === 0) {
    return (
      <EmptyState
        icon="Cpu"
        title={t("noAgentsTitle")}
        body={
          <>
            {t("noAgentsBody")} <Link href="/agents">{t("noAgentsCta")}</Link>
          </>
        }
      />
    );
  }
  if (data.summary.runs === 0) {
    return <EmptyState icon="Activity" title={t("noRunsTitle", { period: periodLabel })} body={t("noRunsBody")} />;
  }
  return (
    <>
      <SummaryCards data={data} periodLabel={periodLabel} />
      <AgentTable
        rows={sortRows(data.agents, sort)}
        sort={sort}
        period={period}
        minDecisions={data.min_decisions}
        onSort={onSort}
      />
      <CostBreakdown byAgent={data.cost_by_agent} byModel={data.cost_by_model} />
    </>
  );
}

function LoadingSkeleton({ label }: { label: string }) {
  return (
    <div role="status" aria-busy="true" aria-label={label} style={s.section}>
      <div style={s.skeletonCards}>
        {[0, 1, 2, 3].map((i) => (
          <div key={i} style={s.skeletonCard}>
            <Skeleton height={96} />
          </div>
        ))}
      </div>
      <Skeleton height={260} />
      <Skeleton height={160} />
    </div>
  );
}

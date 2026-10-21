/* StatsTab — one agent's metrics for a period (FR18, mockup Agent Editor · Stats): the period control;
   four cards (runs with sparkline, average cost as an estimate with the change against the previous
   window, average duration, accept rate as a ring with its denominator); most-used skills and
   most-pulled memory bars; findings by severity (stacked bars) and by category (donut); cost by model,
   the run breakdown, the estimate note and the run history (newest runs, each opening its trace). The period lives in the URL (the picker uses
   router.replace); the same aggregation as the dashboard row. While loading or failed no number is
   rendered (no placeholder data, no zeros); a 404 reads like the editor's load error. */
"use client";

import React from "react";
import { useRouter } from "next/navigation";
import { useTranslations } from "next-intl";
import type { Agent, AgentStats } from "@devdigest/shared";
import { Card, EmptyState, ErrorState, SectionLabel, Skeleton } from "@devdigest/ui";
import {
  CostDonut,
  DeltaText,
  DonutChart,
  EstimateNote,
  RunBreakdown,
  StatCard,
  categoryColor,
  formatDurationS,
  formatPercent,
} from "@/components/agent-metrics";
import { CostText } from "@/components/cost-text";
import { formatUsd } from "@/lib/format-usage";
import { PeriodPicker } from "@/components/stats-period-picker";
import { ApiError } from "@/lib/api";
import { useAgentStats } from "@/lib/hooks";
import { agentStatsHref, formatPeriodRange, type StatsPeriod } from "@/lib/stats-period";
import { RunHistory } from "./_components/RunHistory";
import { SeverityBars } from "./_components/SeverityBars";
import { UsageBars } from "./_components/UsageBars";
import { MEMORY_BAR_COLOR, SKILL_BAR_COLOR } from "./constants";
import { isHourlyPeriod } from "./helpers";
import { s } from "./styles";

export function StatsTab({ agent, period }: { agent: Agent; period: StatsPeriod }) {
  const t = useTranslations("agentPerformance.statsTab");
  const ta = useTranslations("agents");
  const tc = useTranslations("common");
  const router = useRouter();
  const query = useAgentStats(agent.id, period);
  const periodLabel = formatPeriodRange(period, { "1d": t("periodPhrase.1d"), "30d": t("periodPhrase.30d") });
  const notFound = query.error instanceof ApiError && query.error.status === 404;

  return (
    <div style={s.wrap}>
      <div style={s.header}>
        <PeriodPicker period={period} onChange={(next) => router.replace(agentStatsHref(agent.id, next))} />
      </div>

      {query.isLoading && <LoadingSkeleton label={t("loading")} />}
      {query.isError &&
        (notFound ? (
          <ErrorState title={ta("editor.loadErrorTitle")} body={ta("editor.loadErrorBody")} onRetry={() => query.refetch()} retryLabel={tc("actions.retry")} />
        ) : (
          <ErrorState title={t("loadError")} onRetry={() => query.refetch()} retryLabel={tc("actions.retry")} />
        ))}
      {query.data &&
        (query.data.metrics.runs === 0 ? (
          <EmptyState icon="Activity" title={t("noRunsTitle", { period: periodLabel })} body={t("noRunsBody")} />
        ) : (
          <Loaded stats={query.data} />
        ))}
    </div>
  );
}

function Loaded({ stats }: { stats: AgentStats }) {
  const t = useTranslations("agentPerformance.statsTab");
  const tm = useTranslations("agentPerformance.metrics");
  const m = stats.metrics;
  const seconds = formatDurationS(m.avg_duration_ms);
  const percent = formatPercent(m.accept_rate);
  const categories = stats.findings_by_category.map((c) => ({
    key: c.category,
    label: c.category,
    value: c.count,
    color: categoryColor(c.category),
  }));

  return (
    <>
      <div role="group" aria-label={t("cardsLabel")} style={s.cards}>
        <StatCard label={tm("runs")} value={m.runs} trend={stats.runs_trend.map((p) => p.value)}>
          {m.runs_failed + m.runs_cancelled > 0 && <div>{tm("failedOrCancelled", { count: m.runs_failed + m.runs_cancelled })}</div>}
        </StatCard>
        <StatCard
          label={tm("avgCost")}
          value={m.avg_cost_usd == null ? null : <CostText usd={m.avg_cost_usd} />}
          delta={<DeltaText current={m.avg_cost_usd} previous={stats.previous.avg_cost_usd} format={formatUsd} lowerIsBetter />}
        >
          {m.total_cost_usd != null && <div>{tm("totalEstimate", { total: formatUsd(m.total_cost_usd) })}</div>}
          {m.unpriced_runs > 0 && <div>{tm("unpriced", { count: m.unpriced_runs })}</div>}
        </StatCard>
        <StatCard label={tm("avgDuration")} value={seconds} unit={tm("secondsUnit")} />
        <StatCard label={tm("acceptRate")} value={percent} unit={tm("percentUnit")} ring={percent}>
          <div>
            {m.decisions === 0 ? tm("noDecisions") : tm("acceptedOfDecisions", { accepted: m.accepted, decisions: m.decisions, pending: m.pending })}
            {m.small_sample && (
              <>
                {" · "}
                <span title={tm("smallSampleHint", { min: stats.min_decisions })} style={s.marker}>
                  {tm("smallSample")}
                </span>
              </>
            )}
          </div>
        </StatCard>
      </div>

      <div style={s.grid}>
        <Card>
          <SectionLabel icon="Sparkles">{t("mostUsedSkills")}</SectionLabel>
          <UsageBars items={stats.skills_used} totalRuns={m.runs} color={SKILL_BAR_COLOR} emptyText={t("noSkills")} />
        </Card>
        <Card>
          <SectionLabel icon="Database">{t("mostPulledMemory")}</SectionLabel>
          <UsageBars items={stats.memory_pulled} totalRuns={m.runs} color={MEMORY_BAR_COLOR} emptyText={t("noMemory")} />
        </Card>
        <Card>
          <SectionLabel icon="BarChart">{t("findingsBySeverity")}</SectionLabel>
          <SeverityBars buckets={stats.findings_by_severity} hourly={isHourlyPeriod(stats.period.from, stats.period.to)} />
        </Card>
        <Card>
          <SectionLabel icon="Tag">{t("findingsByCategory")}</SectionLabel>
          {categories.length === 0 ? (
            <p style={s.empty}>{t("noFindings")}</p>
          ) : (
            <DonutChart data={categories} label={t("findingsByCategory")} formatValue={(v) => v} />
          )}
        </Card>
        <Card>
          <SectionLabel icon="DollarSign">{t("costByModel")}</SectionLabel>
          <CostDonut title={t("costByModel")} segments={stats.cost_by_model} nullLabel="unknownModel" showTitle={false} />
        </Card>
        <Card>
          <SectionLabel icon="Activity">{t("runBreakdown")}</SectionLabel>
          <RunBreakdown metrics={m} />
        </Card>
      </div>
      <EstimateNote />

      <section aria-label={t("history.title")} style={s.section}>
        <SectionLabel icon="History">{t("history.title")}</SectionLabel>
        <RunHistory runs={stats.run_history} agentName={stats.agent_name} />
      </section>
    </>
  );
}

function LoadingSkeleton({ label }: { label: string }) {
  return (
    <div role="status" aria-busy="true" aria-label={label} style={s.section}>
      <div style={s.cards}>
        {[0, 1, 2, 3].map((i) => (
          <div key={i} style={s.skeletonCard}>
            <Skeleton height={96} />
          </div>
        ))}
      </div>
      <div style={s.grid}>
        {[0, 1, 2, 3].map((i) => (
          <Skeleton key={i} height={200} />
        ))}
      </div>
    </div>
  );
}

/* SummaryCards — the four dashboard cards (FR12, mockup N11): total runs with its sparkline, total
   cost (estimate) with the change against the previous window, pooled accept rate as a ring with its
   denominator, most-active agent. A null value is a muted "—", never 0. */
"use client";

import React from "react";
import { useTranslations } from "next-intl";
import type { AgentPerf } from "@devdigest/shared";
import { AgentGlyph, DeltaText, StatCard, formatPercent } from "@/components/agent-metrics";
import { CostText } from "@/components/cost-text";
import { formatUsd } from "@/lib/format-usage";
import { s } from "./styles";

export interface SummaryCardsProps {
  data: AgentPerf;
  periodLabel: string;
}

export function SummaryCards({ data, periodLabel }: SummaryCardsProps) {
  const t = useTranslations("agentPerformance.dashboard");
  const tm = useTranslations("agentPerformance.metrics");
  const { summary, previous_summary: previous, agents, min_decisions: minDecisions } = data;
  const failedOrCancelled = agents.reduce((sum, a) => sum + a.metrics.runs_failed + a.metrics.runs_cancelled, 0);
  const percent = formatPercent(summary.avg_accept_rate);
  const smallSample = summary.decisions > 0 && summary.decisions < minDecisions;
  const top = summary.most_active;
  const topPercent = formatPercent(agents.find((a) => a.agent_id === top?.agent_id)?.metrics.accept_rate);

  return (
    <div style={s.row}>
      <StatCard label={t("cards.totalRuns")} value={summary.runs} trend={data.runs_trend.map((p) => p.value)}>
        <div>{t("inPeriod", { period: periodLabel })}</div>
        {failedOrCancelled > 0 && <div>{tm("failedOrCancelled", { count: failedOrCancelled })}</div>}
      </StatCard>
      <StatCard
        label={t("cards.totalCost")}
        value={summary.total_cost_usd == null ? null : <CostText usd={summary.total_cost_usd} />}
        delta={<DeltaText current={summary.total_cost_usd} previous={previous.total_cost_usd} format={formatUsd} lowerIsBetter />}
      >
        <div>{tm("estimateLabel")}</div>
        {summary.unpriced_runs > 0 && <div>{tm("unpriced", { count: summary.unpriced_runs })}</div>}
      </StatCard>
      <StatCard label={t("cards.avgAccept")} value={percent} unit={tm("percentUnit")} ring={percent}>
        <div>
          {summary.decisions === 0
            ? tm("noDecisions")
            : tm("acceptedOfDecisions", { accepted: summary.accepted, decisions: summary.decisions, pending: summary.pending })}
          {smallSample && (
            <>
              {" · "}
              <span title={tm("smallSampleHint", { min: minDecisions })} style={s.marker}>
                {tm("smallSample")}
              </span>
            </>
          )}
        </div>
      </StatCard>
      <StatCard
        label={t("cards.mostActive")}
        value={
          top ? (
            <span style={s.agent}>
              <AgentGlyph />
              <span style={s.agentText}>
                <span style={s.name} title={top.agent_name}>
                  {top.agent_name}
                </span>
                <span style={s.agentSub}>
                  {t("cards.runsCount", { count: top.runs })}
                  {topPercent != null && <> · {t("cards.acceptPercent", { percent: topPercent })}</>}
                </span>
              </span>
            </span>
          ) : null
        }
      />
    </div>
  );
}

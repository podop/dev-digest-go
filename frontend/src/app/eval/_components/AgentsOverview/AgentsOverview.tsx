/* AgentsOverview — one card per agent with eval data: icon tile, name with model badge,
   the last finished run (version, date, passed / total), recall sparkline and the three
   metrics as large whole percents ("—" when undefined or never run). The whole card is
   one link to the agent's view (FR23). Presentational: loading / empty / error states
   belong to the caller. */
"use client";

import React from "react";
import Link from "next/link";
import { useTranslations } from "next-intl";
import { Icon, Sparkline } from "@devdigest/ui";
import type { EvalAgentOverview, EvalMetricName } from "@devdigest/shared";
import { toPercent } from "@/components/eval-metrics";
import { METRIC_COLOR } from "../../constants";
import { evalHref, formatRunStamp } from "../../helpers";
import { MIN_SPARK_POINTS, recallSeries } from "./helpers";
import { s } from "./styles";

export function AgentsOverview({ agents }: { agents: readonly EvalAgentOverview[] }) {
  const t = useTranslations("eval.agentsOverview");
  return (
    <ul style={s.list} aria-label={t("caption")}>
      {agents.map((agent) => (
        <li key={agent.agent_id} style={s.item}>
          <AgentCard agent={agent} />
        </li>
      ))}
    </ul>
  );
}

function AgentCard({ agent }: { agent: EvalAgentOverview }) {
  const t = useTranslations("eval.agentsOverview");
  const { latest } = agent;
  const spark = recallSeries(agent.sparkline);
  return (
    <Link href={evalHref(agent.agent_id)} style={s.card} data-testid="eval-agent-card">
      <span style={s.tile} aria-hidden="true">
        <Icon.Cpu size={18} />
      </span>
      <span style={s.body}>
        <span style={s.titleRow}>
          <span style={s.name} title={agent.name}>
            {agent.name}
          </span>
          {agent.model && (
            <span className="mono" style={s.model}>
              {agent.model}
            </span>
          )}
        </span>
        <span style={s.sub}>
          {latest
            ? t.rich("lastRun", {
                version: latest.agent_version,
                passed: latest.cases_passed,
                total: latest.cases_total,
                date: () => (
                  <span className="mono tnum">{formatRunStamp(latest.started_at)}</span>
                ),
              })
            : t("neverRun")}
        </span>
      </span>
      {spark.length >= MIN_SPARK_POINTS && (
        <span role="img" aria-label={t("trendLabel")} style={s.spark}>
          <Sparkline data={spark} color={METRIC_COLOR.recall} w={68} h={24} />
        </span>
      )}
      <MetricValue label={t("labels.recall")} metric="recall" ratio={latest?.recall} />
      <MetricValue label={t("labels.precision")} metric="precision" ratio={latest?.precision} />
      <MetricValue label={t("labels.citation")} metric="citation_accuracy" ratio={latest?.citation_accuracy} />
      <Icon.ChevronRight size={16} style={s.chevron} aria-hidden />
    </Link>
  );
}

function MetricValue({ label, metric, ratio }: { label: string; metric: EvalMetricName; ratio: number | null | undefined }) {
  const t = useTranslations("eval.agentsOverview");
  const percent = toPercent(ratio);
  return (
    <span style={s.metric}>
      <span style={s.metricLabel}>{label}</span>
      <span className="tnum" style={{ ...s.metricValue, color: percent == null ? "var(--text-muted)" : METRIC_COLOR[metric] }}>
        {percent == null ? t("noValue") : t("percent", { value: percent })}
      </span>
    </span>
  );
}

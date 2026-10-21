/* RunBreakdown — the read-only detail of one agent's period metrics (FR15): runs by status and
   by source, finding decisions, priced / unpriced runs. Shared by the dashboard's expanded row
   and the Stats tab; everything comes from the response, no request. Presentational. */
"use client";

import React from "react";
import { useTranslations } from "next-intl";
import type { AgentPeriodMetrics } from "@devdigest/shared";
import { s } from "./styles";

export interface RunBreakdownProps {
  metrics: AgentPeriodMetrics;
}

export function RunBreakdown({ metrics: m }: RunBreakdownProps) {
  const t = useTranslations("agentPerformance.breakdown");
  if (m.runs === 0) return <p style={s.empty}>{t("noRuns")}</p>;
  return (
    <div style={s.breakdown}>
      <Group title={t("byStatus")} rows={[[t("done"), m.runs_done], [t("failed"), m.runs_failed], [t("cancelled"), m.runs_cancelled]]} />
      <Group title={t("bySource")} rows={[[t("local"), m.runs_local], [t("ci"), m.runs_ci]]} />
      <Group title={t("decisions")} rows={[[t("accepted"), m.accepted], [t("dismissed"), m.dismissed], [t("pending"), m.pending]]} />
      <Group title={t("costEstimates")} rows={[[t("priced"), m.priced_runs], [t("unpriced"), m.unpriced_runs]]} />
    </div>
  );
}

function Group({ title, rows }: { title: string; rows: readonly (readonly [string, number])[] }) {
  return (
    <div role="group" aria-label={title} style={s.group}>
      <span style={s.groupTitle}>{title}</span>
      {rows.map(([label, value]) => (
        <div key={label} style={s.pair}>
          <span style={s.pairTerm}>{label}</span>
          <span className="tnum">{value}</span>
        </div>
      ))}
    </div>
  );
}

/* KpiTiles — the eight KPI tiles of the leaderboard (FR19): label, value and the change against the
   previous window. A null value is a muted "—"; no delta when either side is null or there is no
   previous summary (30d, or a previous week without rows). Open critical is red while it is above 0.
   Presentational: the numbers come from the GET /leaderboard response. */
"use client";

import React from "react";
import { useTranslations } from "next-intl";
import type { LeaderboardKpis } from "@devdigest/shared";
import { StatCard } from "@/components/agent-metrics";
import { CostText } from "@/components/cost-text";
import { formatUsd } from "@/lib/format-usage";
import { KpiDelta } from "./KpiDelta";
import { KPI_SPECS, formatDuration, formatScore, kpiDelta, type KpiSpec } from "./helpers";
import { s } from "./styles";

export interface KpiTilesProps {
  summary: LeaderboardKpis;
  /** The previous window's KPIs; null when it has none or no rows (no tile shows a delta then). */
  previous: LeaderboardKpis | null;
}

export function KpiTiles({ summary, previous }: KpiTilesProps) {
  const t = useTranslations("leaderboardHighlights");

  function display(spec: KpiSpec, value: number | null): React.ReactNode | null {
    if (value == null) return null;
    switch (spec.kind) {
      case "usd":
        return <CostText usd={value} />;
      case "duration":
        return formatDuration(t, value);
      case "health":
        return formatScore(value);
      case "count":
        return value;
    }
  }

  function amountText(spec: KpiSpec, amount: number): string {
    switch (spec.kind) {
      case "usd":
        return formatUsd(amount);
      case "duration":
        return formatDuration(t, amount);
      case "health":
        return formatScore(amount);
      case "count":
        return String(amount);
    }
  }

  return (
    <div style={s.grid}>
      {KPI_SPECS.map((spec) => {
        const current = summary[spec.key];
        const delta = previous ? kpiDelta(spec, current, previous[spec.key]) : null;
        const shown = display(spec, current);
        const alert = spec.key === "open_critical" && current != null && current > 0;
        return (
          <StatCard
            key={spec.key}
            compact
            label={t(`kpi.${spec.key}`)}
            value={alert ? <span style={s.alert}>{shown}</span> : shown}
            delta={delta && <KpiDelta delta={delta} amount={amountText(spec, delta.amount)} />}
          />
        );
      })}
    </div>
  );
}

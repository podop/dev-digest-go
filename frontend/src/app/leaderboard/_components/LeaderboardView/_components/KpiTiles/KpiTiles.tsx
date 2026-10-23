/* KpiTiles — the eight KPI tiles of the leaderboard (FR19): label, value and the change against the
   previous window. A null value is a muted "—"; no delta when either side is null or the previous
   window is empty. Presentational: the numbers come from the GET /leaderboard response. */
"use client";

import React from "react";
import { useTranslations } from "next-intl";
import type { LeaderboardKpis } from "@devdigest/shared";
import { StatCard } from "@/components/agent-metrics";
import { CostText } from "@/components/cost-text";
import { formatUsd } from "@/lib/format-usage";
import { KpiDelta } from "./KpiDelta";
import { KPI_SPECS, formatDuration, formatScore, kpiDelta, previousWindowIsEmpty, type KpiSpec } from "./helpers";
import { s } from "./styles";

export interface KpiTilesProps {
  summary: LeaderboardKpis;
  previous: LeaderboardKpis;
}

export function KpiTiles({ summary, previous }: KpiTilesProps) {
  const t = useTranslations("leaderboardHighlights");
  const comparable = !previousWindowIsEmpty(previous);

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
        const delta = comparable ? kpiDelta(spec, current, previous[spec.key]) : null;
        return (
          <StatCard
            key={spec.key}
            label={t(`kpi.${spec.key}`)}
            value={display(spec, current)}
            delta={delta && <KpiDelta delta={delta} amount={amountText(spec, delta.amount)} />}
          />
        );
      })}
    </div>
  );
}

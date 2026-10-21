/* StatCard — one number card of the Agent Performance screens (mockup PerfSummaryCard / StatBig):
   label, optional accept ring, big value with a muted unit and a delta, optional runs sparkline,
   optional hint lines. A null value renders a muted "—" (never 0). Presentational. */
"use client";

import React from "react";
import { useTranslations } from "next-intl";
import { CircularScore, Sparkline } from "@devdigest/ui";
import { s } from "./styles";

export interface StatCardProps {
  label: string;
  /** The formatted value; null/undefined = unknown, shown as "—". */
  value: React.ReactNode | null | undefined;
  /** Muted unit right after a known value ("%", "s"). */
  unit?: string;
  /** Change against the previous window, next to the value. */
  delta?: React.ReactNode;
  /** 0..100 score drawn as a ring at the top right. */
  ring?: number | null;
  /** Values of a sparkline under the value (needs ≥ 2 points). */
  trend?: readonly number[];
  /** Secondary lines under the value (denominators, estimate label, counts). */
  children?: React.ReactNode;
}

export function StatCard({ label, value, unit, delta, ring, trend, children }: StatCardProps) {
  const t = useTranslations("agentPerformance.metrics");
  const known = value != null;
  return (
    <div role="group" aria-label={label} style={s.card}>
      <div style={s.cardHead}>
        <span style={s.cardLabel}>{label}</span>
        {ring != null && <CircularScore score={ring} size={34} stroke={4} />}
      </div>
      <div style={s.cardValueRow}>
        <span className="tnum" style={{ ...s.cardValue, ...(known ? null : s.muted) }}>
          {known ? value : t("noValue")}
          {known && unit && <span style={s.cardUnit}>{unit}</span>}
        </span>
        {known && delta}
      </div>
      {trend && trend.length >= 2 && (
        <div data-testid="runs-trend" style={s.cardTrend}>
          <Sparkline data={[...trend]} color="var(--accent)" w={200} h={28} />
        </div>
      )}
      {children != null && <div style={s.cardHint}>{children}</div>}
    </div>
  );
}

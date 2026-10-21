/* DeltaText — the change against the equally long previous window ("-$1.20"), green when it moved
   the good way. Nothing when either side is unknown or there is no change. Presentational. */
"use client";

import React from "react";
import { useTranslations } from "next-intl";
import { s } from "./styles";

export interface DeltaTextProps {
  current: number | null;
  previous: number | null;
  /** Formats the absolute difference (the sign is added here). */
  format: (abs: number) => string;
  /** Cost: a decrease is good. */
  lowerIsBetter?: boolean;
}

export function DeltaText({ current, previous, format, lowerIsBetter = false }: DeltaTextProps) {
  const t = useTranslations("agentPerformance.metrics");
  if (current == null || previous == null) return null;
  const diff = current - previous;
  const text = format(Math.abs(diff));
  if (diff === 0 || text === format(0)) return null;
  const good = lowerIsBetter ? diff < 0 : diff > 0;
  return (
    <span className="tnum" title={t("vsPrevious")} style={{ ...s.delta, color: good ? "var(--ok)" : "var(--crit)" }}>
      {diff < 0 ? "-" : "+"}
      {text}
    </span>
  );
}

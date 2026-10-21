/* AcceptTrend — ↑ / ↓ against the previous window's accept rate (mockup N11). Nothing when either
   rate is unknown or they are equal at whole-percent precision. Presentational. */
"use client";

import React from "react";
import { useTranslations } from "next-intl";
import { Icon } from "@devdigest/ui";
import { formatPercent } from "./helpers";

export function AcceptTrend({ rate, previousRate }: { rate: number | null; previousRate: number | null }) {
  const t = useTranslations("agentPerformance.metrics");
  const now = formatPercent(rate);
  const before = formatPercent(previousRate);
  if (now == null || before == null || now === before) return null;
  const up = now > before;
  const Arrow = up ? Icon.ArrowUp : Icon.ArrowDown;
  const label = t(up ? "acceptUp" : "acceptDown", { previous: before });
  return (
    <span role="img" aria-label={label} title={label} style={{ display: "inline-flex", color: up ? "var(--ok)" : "var(--crit)" }}>
      <Arrow size={12} />
    </span>
  );
}

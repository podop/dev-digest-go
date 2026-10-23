/* KpiDelta — change of one KPI against the previous window: arrow + amount (or "no change"),
   green when it moved the good way, red otherwise; a screen-reader word backs the colour. */
"use client";

import React from "react";
import { useTranslations } from "next-intl";
import type { KpiDelta as Delta } from "./helpers";
import { s } from "./styles";

export interface KpiDeltaProps {
  delta: Delta;
  /** The amount already formatted in the KPI's unit ("3", "$0.40", "1h 5m"). */
  amount: string;
}

export function KpiDelta({ delta, amount }: KpiDeltaProps) {
  const t = useTranslations("leaderboardHighlights.delta");
  const color = delta.good == null ? "var(--text-muted)" : delta.good ? "var(--ok)" : "var(--crit)";
  return (
    <span className="tnum" title={t("hint")} style={{ ...s.delta, color }}>
      {delta.direction === "same" ? t("same") : t(delta.direction, { amount })}
      {delta.good != null && <span style={s.srOnly}> {t(delta.good ? "better" : "worse")}</span>}
    </span>
  );
}

/* AcceptRateText — the accept rate with its denominator: "60% of 12", "100% of 3 · small sample"
   (text marker + tooltip, so colour is never the only signal), "— 0 decisions". The percent is green
   from 60%, amber below (mockup N11), followed by the ↑/↓ against the previous window. Presentational. */
"use client";

import React from "react";
import { useTranslations } from "next-intl";
import { AcceptTrend } from "./AcceptTrend";
import { acceptRateColor, formatPercent } from "./helpers";
import { s } from "./styles";

export interface AcceptRateTextProps {
  rate: number | null;
  decisions: number;
  smallSample: boolean;
  minDecisions: number;
  /** The previous window's rate, for the trend arrow; omit for no arrow. */
  previousRate?: number | null;
}

export function AcceptRateText({ rate, decisions, smallSample, minDecisions, previousRate }: AcceptRateTextProps) {
  const t = useTranslations("agentPerformance.metrics");
  const percent = formatPercent(rate);
  if (percent == null) {
    return (
      <span className="tnum">
        <span style={s.muted}>{t("noValue")}</span> {t("noDecisions")}
      </span>
    );
  }
  return (
    <span className="tnum" style={s.acceptRate}>
      <span style={{ fontWeight: 600, color: acceptRateColor(rate) }}>
        {t("percent", { value: percent })}
      </span>
      {previousRate !== undefined && <AcceptTrend rate={rate} previousRate={previousRate} />}
      <span style={s.acceptOf}>{t("ofDecisions", { decisions })}</span>
      {smallSample && (
        <>
          {" · "}
          <span title={t("smallSampleHint", { min: minDecisions })} style={s.marker}>
            {t("smallSample")}
          </span>
        </>
      )}
    </span>
  );
}

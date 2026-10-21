/* RegressionBanner — the latest finished run dropped a metric by >= 0.02 vs the previous
   one (eval-pipeline FR15), as one line: the dipped metrics in bold with their drop in
   points, the version, and how the other metrics moved. The text carries the message,
   colour is only a hint (NFR8). */
"use client";

import React from "react";
import { useTranslations } from "next-intl";
import { Icon } from "@devdigest/ui";
import type { EvalRegressionAlert, EvalRunSummary } from "@devdigest/shared";
import { ratioToPoints } from "../../helpers";
import { otherMetricChanges, othersSentence } from "./helpers";
import { s } from "./styles";

export interface RegressionBannerProps {
  alert: EvalRegressionAlert;
  latest: EvalRunSummary;
  previous: EvalRunSummary | null;
}

export function RegressionBanner({ alert, latest, previous }: RegressionBannerProps) {
  const t = useTranslations("eval.regression");
  const dipped = alert.metrics
    .map(({ metric, delta }) => t("dipped", { metric: t(`metricCap.${metric}`), amount: Math.abs(ratioToPoints(delta) ?? 0) }))
    .join(` ${t("and")} `);
  const sentence = othersSentence(otherMetricChanges(alert, latest, previous));
  return (
    <div role="alert" style={s.banner}>
      <Icon.AlertTriangle size={16} aria-hidden style={s.icon} />
      <p style={s.text} className="tnum">
        <strong style={s.strong}>{dipped}</strong> {t("on", { version: latest.agent_version })}
        {sentence.kind === "one" && ` ${t("otherOne", { a: t(`metricCap.${sentence.a.metric}`), dir: t(`direction.${sentence.a.direction}`) })}`}
        {sentence.kind === "both" &&
          ` ${t("otherBoth", { a: t(`metricCap.${sentence.a.metric}`), b: t(`metric.${sentence.b.metric}`), dir: t(`direction.${sentence.a.direction}`) })}`}
        {sentence.kind === "mixed" &&
          ` ${t("otherMixed", {
            a: t(`metricCap.${sentence.a.metric}`),
            dirA: t(`direction.${sentence.a.direction}`),
            b: t(`metric.${sentence.b.metric}`),
            dirB: t(`direction.${sentence.b.direction}`),
          })}`}
      </p>
    </div>
  );
}

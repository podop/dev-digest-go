/* MetricTiles — the eval metric tiles of an agent's latest run: recall, precision,
   citation accuracy (+ cases passed on request). Values are whole percents ("—" when
   the metric is undefined), deltas vs the previous run are signed text with an arrow,
   so colour is never the only signal (NFR8). Presentational: loading / empty / error
   states belong to the caller. */
"use client";

import React from "react";
import { useTranslations } from "next-intl";
import { Sparkline } from "@devdigest/ui";
import type { EvalMetricName, EvalRunSummary } from "@devdigest/shared";
import { casesPassedDelta, deltaPoints, directionOf, EVAL_METRICS, metricOf, sparkSeries, toPercent, type DeltaDirection } from "./helpers";
import { s } from "./styles";

const METRIC_LABEL_KEY: Record<EvalMetricName, string> = {
  recall: "recall",
  precision: "precision",
  citation_accuracy: "citationAccuracy",
};

const METRIC_COLOR: Record<EvalMetricName, string> = {
  recall: "var(--accent)",
  precision: "var(--ok)",
  citation_accuracy: "var(--warn)",
};

const TONE: Record<DeltaDirection, React.CSSProperties> = { up: s.up, down: s.down, flat: s.flat };
const DELTA_KEY_SUFFIX: Record<DeltaDirection, string> = { up: "Up", down: "Down", flat: "Flat" };

/** Minimum points for a sparkline to draw a line (one point has no direction). */
const MIN_SPARK_POINTS = 2;

export interface MetricTilesProps {
  latest: EvalRunSummary | null;
  previous: EvalRunSummary | null;
  /** Finished runs, oldest first — draws a sparkline per metric tile. */
  history?: readonly EvalRunSummary[];
  /** Add the "cases passed n / m" tile (Evals tab). */
  showCasesPassed?: boolean;
}

export function MetricTiles({ latest, previous, history, showCasesPassed = false }: MetricTilesProps) {
  const t = useTranslations("eval.metricTiles");
  return (
    <div style={s.row}>
      {EVAL_METRICS.map((metric) => {
        const percent = toPercent(metricOf(latest, metric));
        const change = deltaPoints(metricOf(latest, metric), metricOf(previous, metric));
        const spark = history ? sparkSeries(history, metric) : [];
        return (
          <div key={metric} role="group" aria-label={t(METRIC_LABEL_KEY[metric])} style={s.tile}>
            <div style={s.head}>
              <span style={s.label}>{t(METRIC_LABEL_KEY[metric])}</span>
              {spark.length >= MIN_SPARK_POINTS && <Sparkline data={spark} color={METRIC_COLOR[metric]} w={56} h={20} />}
            </div>
            <div style={s.body}>
              <span className="tnum" style={{ ...s.value, ...(percent == null ? s.muted : null) }}>
                {percent == null ? t("noValue") : t("percent", { value: percent })}
              </span>
              {change != null && <Delta amount={change} unit="points" />}
            </div>
          </div>
        );
      })}
      {showCasesPassed && <CasesPassedTile latest={latest} previous={previous} />}
    </div>
  );
}

/** Signed change vs the previous run: arrow + sign + amount, coloured by direction. */
function Delta({ amount, unit }: { amount: number; unit: "points" | "cases" }) {
  const t = useTranslations("eval.metricTiles");
  const direction = directionOf(amount);
  const key = `${unit === "points" ? "delta" : "casesDelta"}${DELTA_KEY_SUFFIX[direction]}`;
  return (
    <span className="tnum" style={{ ...s.delta, ...TONE[direction] }} title={t("deltaHint")}>
      {t(key, { amount: Math.abs(amount) })}
    </span>
  );
}

function CasesPassedTile({ latest, previous }: { latest: EvalRunSummary | null; previous: EvalRunSummary | null }) {
  const t = useTranslations("eval.metricTiles");
  const change = casesPassedDelta(latest, previous);
  return (
    <div role="group" aria-label={t("casesPassed")} style={s.tile}>
      <div style={s.head}>
        <span style={s.label}>{t("casesPassed")}</span>
      </div>
      <div style={s.body}>
        <span className="tnum" style={{ ...s.value, ...(latest ? null : s.muted) }}>
          {latest ? t("casesFraction", { passed: latest.cases_passed, total: latest.cases_total }) : t("noValue")}
        </span>
        {change != null && <Delta amount={change} unit="cases" />}
      </div>
    </div>
  );
}

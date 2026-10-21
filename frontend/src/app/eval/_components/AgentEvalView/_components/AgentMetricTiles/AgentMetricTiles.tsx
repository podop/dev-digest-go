/* AgentMetricTiles — the agent view's three tiles: recall, precision, citation accuracy of
   the latest run. Value as a whole percent with a smaller muted "%", the change vs the
   previous finished run as arrow + two-decimal ratio (text, so colour is never the only
   signal, NFR8) and the period's sparkline in the series colour. Presentational: loading,
   empty and error states belong to the caller. */
"use client";

import React from "react";
import { useTranslations } from "next-intl";
import { Sparkline } from "@devdigest/ui";
import type { EvalRunSummary } from "@devdigest/shared";
import { EVAL_METRICS, metricOf, sparkSeries, toPercent, type DeltaDirection } from "@/components/eval-metrics";
import { METRIC_COLOR } from "@/app/eval/constants";
import { METRIC_LABEL_KEY } from "../../constants";
import { tileDelta } from "./helpers";
import { s } from "./styles";

const TONE: Record<DeltaDirection, React.CSSProperties> = { up: s.up, down: s.down, flat: s.flat };
const DELTA_KEY: Record<DeltaDirection, string> = { up: "deltaUp", down: "deltaDown", flat: "deltaFlat" };

/** A sparkline needs two points to have a direction. */
const MIN_SPARK_POINTS = 2;

export interface AgentMetricTilesProps {
  latest: EvalRunSummary;
  previous: EvalRunSummary | null;
  /** Finished runs of the period, oldest first. */
  history: readonly EvalRunSummary[];
}

export function AgentMetricTiles({ latest, previous, history }: AgentMetricTilesProps) {
  const t = useTranslations("eval.agentTiles");
  const tl = useTranslations("eval.dashboard.metrics");
  return (
    <div style={s.row}>
      {EVAL_METRICS.map((metric) => {
        const percent = toPercent(metricOf(latest, metric));
        const delta = tileDelta(latest, previous, metric);
        const spark = sparkSeries(history, metric);
        return (
          <div key={metric} role="group" aria-label={tl(METRIC_LABEL_KEY[metric])} style={s.tile}>
            <div style={s.head}>
              <span style={s.label}>{tl(METRIC_LABEL_KEY[metric])}</span>
              {spark.length >= MIN_SPARK_POINTS && <Sparkline data={spark} color={METRIC_COLOR[metric]} w={64} h={22} />}
            </div>
            <div style={s.body}>
              <span className="tnum" style={{ ...s.value, ...(percent == null ? s.muted : null) }}>
                {percent == null ? (
                  t("noValue")
                ) : (
                  <>
                    {percent}
                    <span style={s.unit}>{t("percentUnit")}</span>
                  </>
                )}
              </span>
              {delta && (
                <span className="tnum" style={{ ...s.delta, ...TONE[delta.direction] }} title={t("deltaHint")}>
                  {t(DELTA_KEY[delta.direction], { amount: delta.amount })}
                </span>
              )}
            </div>
          </div>
        );
      })}
    </div>
  );
}

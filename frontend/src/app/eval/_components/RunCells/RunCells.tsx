/* RunCells — the two result cells shared by the agent runs table and the overview's recent
   runs table: a metric as a horizontal bar plus whole percent, and a run's pass count.
   Both render a <td>, so they belong inside a table row. */
"use client";

import React from "react";
import { useTranslations } from "next-intl";
import type { EvalMetricName, EvalRunSummary } from "@devdigest/shared";
import { toPercent } from "@/components/eval-metrics";
import { METRIC_COLOR } from "../../constants";
import { s } from "./styles";

/** Bar (fill = value, series colour) and whole percent; a metric the run has no value for
 *  shows an empty bar and "—". */
export function MetricBarCell({ ratio, metric }: { ratio: number | null | undefined; metric: EvalMetricName }) {
  const t = useTranslations("eval.runCells");
  const percent = toPercent(ratio);
  return (
    <td style={s.cell}>
      <div style={s.metric}>
        <div style={s.track} aria-hidden="true">
          {percent != null && <div style={s.fill(Math.min(100, Math.max(0, percent)), METRIC_COLOR[metric])} />}
        </div>
        <span className="mono tnum" style={{ ...s.percent, ...(percent == null ? s.muted : null) }}>
          {percent == null ? t("noValue") : t("percent", { value: percent })}
        </span>
      </div>
    </td>
  );
}

export type PassCellRun = Pick<EvalRunSummary, "status" | "cases_passed" | "cases_total" | "cases_finished" | "error">;

/** "17/20" in bold; "Running x / N" while it executes; "Failed" (error as tooltip) when it failed. */
export function PassCell({ run }: { run: PassCellRun }) {
  const t = useTranslations("eval.runCells");
  if (run.status === "running") {
    return <td style={{ ...s.cell, ...s.progress }}>{t("running", { finished: run.cases_finished, total: run.cases_total })}</td>;
  }
  if (run.status === "failed") {
    return (
      <td style={{ ...s.cell, ...s.crit }} title={run.error ?? undefined}>
        {t("failed")}
      </td>
    );
  }
  return (
    <td className="tnum" style={{ ...s.cell, ...s.pass }}>
      {t("passFraction", { passed: run.cases_passed, total: run.cases_total })}
    </td>
  );
}

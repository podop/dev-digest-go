/* TrendChart — recall, precision and citation accuracy of every finished run of the period
   (oldest → newest, equally spaced). Inline SVG, no chart library. At rest only each
   series' newest point is a dot; hovering or keyboard-focusing a run shows a guide, a dot
   per series and one tooltip (version, date, metrics, cost). Every run is a native
   <button> laid over the plot (roving tabindex, ←/→/Home/End, Escape hides) whose label
   is the tooltip text, so the chart is reachable without a pointer; series are told apart
   by legend and tooltip text, not by colour alone (NFR8). */
"use client";

import React from "react";
import { useTranslations } from "next-intl";
import type { EvalMetricName, EvalRunSummary } from "@devdigest/shared";
import type { EvalPeriodId } from "@devdigest/shared/constants/evals";
import { Icon } from "@devdigest/ui";
import { EVAL_METRICS, toPercent } from "@/components/eval-metrics";
import { METRIC_COLOR } from "@/app/eval/constants";
import { formatRunStamp } from "@/app/eval/helpers";
import { formatUsd } from "@/lib/format-usage";
import { CHART, EMPTY_KEY, LEGEND_KEY } from "./constants";
import { axisBottom, axisTicks, columnOf, lineSegments, metricPoints, newestPoint, toPolyline, tooltipOnLeft, xOf, yOf } from "./helpers";
import { s } from "./styles";
import { useChartCursor } from "./useChartCursor";

export interface TrendChartProps {
  /** Finished runs of the period, oldest first. */
  runs: readonly EvalRunSummary[];
  period: EvalPeriodId;
}

const pct = (x: number) => `${(x / CHART.width) * 100}%`;

export function TrendChart({ runs, period }: TrendChartProps) {
  const t = useTranslations("eval.trend");
  const tl = useTranslations("eval.dashboard.legend");
  const tooltipId = React.useId();
  const cursor = useChartCursor(runs.length);
  const label = (metric: EvalMetricName) => tl(LEGEND_KEY[metric]);

  const header = (
    <div style={s.head}>
      <h2 style={s.title}>
        <Icon.TrendingUp size={14} aria-hidden />
        {t("title")}
      </h2>
      <div style={s.legend}>
        {EVAL_METRICS.map((metric) => (
          <span key={metric} style={s.legendItem}>
            <span style={{ ...s.swatch, background: METRIC_COLOR[metric] }} aria-hidden />
            {label(metric)}
          </span>
        ))}
      </div>
    </div>
  );
  if (runs.length === 0) {
    return (
      <div style={s.wrap}>
        {header}
        <p style={s.empty}>{t(EMPTY_KEY[period])}</p>
      </div>
    );
  }

  const bottom = axisBottom(runs);
  const series = EVAL_METRICS.map((metric) => ({ metric, points: metricPoints(runs, metric, bottom), lines: lineSegments(runs, metric, bottom) }));
  const activeRun = cursor.active == null ? null : runs[cursor.active]!;
  const activeX = cursor.active == null ? 0 : xOf(cursor.active, runs.length);
  // At rest the newest point of each series; the hovered or focused run adds its own points.
  const dots = series.flatMap(({ metric, points }) => {
    const newest = newestPoint(points);
    const atCursor = activeRun ? points.find((p) => p.run.id === activeRun.id) : undefined;
    return [newest, atCursor !== newest ? atCursor : undefined].flatMap((point) => (point ? [{ metric, point }] : []));
  });

  return (
    <div style={s.wrap}>
      {header}
      <div style={s.plot}>
        <svg viewBox={`0 0 ${CHART.width} ${CHART.height}`} aria-hidden="true" style={s.svg}>
          {axisTicks(bottom).map((value) => (
            <g key={value}>
              <line x1={CHART.left} x2={CHART.width - CHART.right} y1={yOf(value, bottom)} y2={yOf(value, bottom)} stroke="var(--border)" strokeWidth={1} />
              <text x={CHART.left - 10} y={yOf(value, bottom) + 4} textAnchor="end" className="mono" style={s.axis}>
                {t("axis", { value: value.toFixed(1) })}
              </text>
            </g>
          ))}
          {activeRun && <line x1={activeX} x2={activeX} y1={CHART.top} y2={CHART.height - CHART.bottom} stroke="var(--text-muted)" strokeWidth={1} strokeDasharray="3 3" />}
          {series.map(({ metric, lines }) =>
            lines.map((segment) => (
              <polyline
                key={`${metric}-${segment[0]!.run.id}`}
                data-series={metric}
                points={toPolyline(segment)}
                fill="none"
                stroke={METRIC_COLOR[metric]}
                strokeWidth={2}
                strokeLinejoin="round"
                strokeLinecap="round"
              />
            )),
          )}
          {dots.map(({ metric, point }) => (
            <circle key={`${metric}-${point.run.id}`} data-testid="trend-dot" data-series={metric} cx={point.x} cy={point.y} r={3.5} fill={METRIC_COLOR[metric]} />
          ))}
        </svg>
        <div role="group" aria-label={t("title")} style={s.columns}>
          {runs.map((run, i) => {
            const column = columnOf(i, runs.length);
            return (
              <button
                key={run.id}
                type="button"
                ref={cursor.setRef(i)}
                tabIndex={i === cursor.tabStop ? 0 : -1}
                aria-label={runLabel(t, run)}
                aria-describedby={cursor.active === i ? tooltipId : undefined}
                style={{ ...s.column, left: pct(column.left), width: pct(column.width) }}
                {...cursor.bind(i)}
              />
            );
          })}
        </div>
        {activeRun && cursor.active != null && (
          <RunTooltip
            id={tooltipId}
            run={activeRun}
            label={label}
            style={{ left: pct(activeX), transform: tooltipOnLeft(cursor.active, runs.length) ? "translateX(calc(-100% - 12px))" : "translateX(12px)" }}
          />
        )}
      </div>
    </div>
  );
}

type TrendT = ReturnType<typeof useTranslations<"eval.trend">>;

/** The tooltip as one sentence — the accessible name of the run's button. */
function runLabel(t: TrendT, run: EvalRunSummary): string {
  const value = (ratio: number | null) => {
    const percent = toPercent(ratio);
    return percent == null ? t("noValue") : t("percent", { value: percent });
  };
  return t("runLabel", {
    version: run.agent_version,
    date: formatRunStamp(run.started_at),
    recall: value(run.recall),
    precision: value(run.precision),
    citation: value(run.citation_accuracy),
    cost: formatUsd(run.cost_usd),
    status: run.status,
  });
}

function RunTooltip({ id, run, label, style }: { id: string; run: EvalRunSummary; label: (m: EvalMetricName) => string; style: React.CSSProperties }) {
  const t = useTranslations("eval.trend");
  return (
    <div id={id} role="tooltip" style={{ ...s.tooltip, ...style }}>
      <div style={s.tipHead}>
        <span className="mono" style={s.version}>
          {t("version", { version: run.agent_version })}
        </span>
        {run.status === "failed" && <span style={s.failed}>{t("failed")}</span>}
      </div>
      <div className="mono tnum" style={s.stamp}>
        {formatRunStamp(run.started_at)}
      </div>
      {EVAL_METRICS.map((metric) => {
        const percent = toPercent(run[metric]);
        return (
          <div key={metric} style={s.tipRow}>
            <span style={{ ...s.dot, background: METRIC_COLOR[metric] }} aria-hidden />
            <span style={s.tipLabel}>{label(metric)}</span>
            <span className="mono tnum">{percent == null ? t("noValue") : t("percent", { value: percent })}</span>
          </div>
        );
      })}
      <div style={s.tipRow}>
        <span style={s.tipLabel}>{t("cost")}</span>
        <span className="mono tnum">{formatUsd(run.cost_usd)}</span>
      </div>
    </div>
  );
}

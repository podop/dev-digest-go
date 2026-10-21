/* DonutChart — ring chart with a legend (inline SVG; the vendored Donut hard-codes "$" + 2 decimals).
   Each legend row shows the formatted value; the share is in its tooltip. Presentational. */
"use client";

import React from "react";
import { s } from "./styles";

export interface DonutDatum {
  key: string;
  label: string;
  value: number;
  color: string;
}

export interface DonutChartProps {
  data: readonly DonutDatum[];
  /** How a value is shown in the legend (cost text, a count). */
  formatValue: (value: number) => React.ReactNode;
  /** Accessible name of the chart. */
  label: string;
  size?: number;
  stroke?: number;
}

export function DonutChart({ data, formatValue, label, size = 120, stroke = 22 }: DonutChartProps) {
  const total = data.reduce((sum, d) => sum + d.value, 0);
  const r = (size - stroke) / 2;
  const circ = 2 * Math.PI * r;
  let offset = 0;
  return (
    <div style={s.donut}>
      <svg width={size} height={size} role="img" aria-label={label} style={s.donutSvg}>
        <circle cx={size / 2} cy={size / 2} r={r} fill="none" stroke="var(--bg-hover)" strokeWidth={stroke} />
        {total > 0 &&
          data.map((d) => {
            const len = (d.value / total) * circ;
            const seg = (
              <circle
                key={d.key}
                data-series={d.key}
                cx={size / 2}
                cy={size / 2}
                r={r}
                fill="none"
                stroke={d.color}
                strokeWidth={stroke}
                strokeDasharray={`${len} ${circ - len}`}
                strokeDashoffset={-offset}
              />
            );
            offset += len;
            return seg;
          })}
      </svg>
      <ul style={s.legend}>
        {data.map((d) => (
          <li key={d.key} style={s.legendItem} title={`${d.label} · ${total > 0 ? Math.round((d.value / total) * 100) : 0}%`}>
            <span aria-hidden style={{ ...s.swatch, background: d.color }} />
            <span style={s.legendLabel}>{d.label}</span>
            <span className="mono tnum" style={s.legendValue}>
              {formatValue(d.value)}
            </span>
          </li>
        ))}
      </ul>
    </div>
  );
}

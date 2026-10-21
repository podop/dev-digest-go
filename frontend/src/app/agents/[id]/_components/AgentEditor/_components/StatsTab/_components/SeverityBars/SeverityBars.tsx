/* SeverityBars — "Findings by severity" (mockup Agent Editor · Stats): one stacked bar per equal
   slice of the period (critical at the bottom, then warning, then suggestion), heights relative to
   the busiest slice, the slice start under each bar and a legend. Each bar's tooltip has the counts. */
"use client";

import React from "react";
import { useTranslations } from "next-intl";
import type { SeverityBucket } from "@devdigest/shared";
import { SEV } from "@devdigest/ui";
import { formatSliceStart } from "../../helpers";
import { s } from "./styles";

const STACK = [
  ["suggestion", SEV.SUGGESTION.c],
  ["warning", SEV.WARNING.c],
  ["critical", SEV.CRITICAL.c],
] as const;

const total = (b: SeverityBucket) => b.critical + b.warning + b.suggestion;

export function SeverityBars({ buckets, hourly }: { buckets: readonly SeverityBucket[]; hourly: boolean }) {
  const t = useTranslations("agentPerformance.statsTab");
  const max = Math.max(0, ...buckets.map(total));
  if (max === 0) return <p style={s.empty}>{t("noFindings")}</p>;
  return (
    <div>
      <div style={s.chart} role="img" aria-label={t("findingsBySeverity")}>
        {buckets.map((b) => {
          const label = formatSliceStart(b.from, hourly);
          return (
            <div
              key={b.from}
              data-series={b.from}
              style={s.column}
              title={t("severityTooltip", { label, critical: b.critical, warning: b.warning, suggestion: b.suggestion })}
            >
              <div style={s.stack}>
                {STACK.map(([key, color]) =>
                  b[key] > 0 ? <div key={key} style={{ ...s.segment, height: `${(b[key] / max) * 100}%`, background: color }} /> : null,
                )}
              </div>
              <span style={s.tick}>{label}</span>
            </div>
          );
        })}
      </div>
      <div style={s.legend}>
        {[...STACK].reverse().map(([key, color]) => (
          <span key={key} style={s.legendItem}>
            <span aria-hidden style={{ ...s.swatch, background: color }} />
            {t(`severity.${key}`)}
          </span>
        ))}
      </div>
    </div>
  );
}

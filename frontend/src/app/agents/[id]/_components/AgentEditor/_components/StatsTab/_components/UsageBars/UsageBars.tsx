/* UsageBars — "Most-used skills" / "Most-pulled memory" (mockup Agent Editor · Stats): one bar per
   item, its share of the agent's counted runs in the period; the tooltip has the run counts. */
"use client";

import React from "react";
import { useTranslations } from "next-intl";
import type { UsageCount } from "@devdigest/shared";
import { BarRow } from "@devdigest/ui";
import { usageShare } from "../../helpers";
import { s } from "./styles";

export interface UsageBarsProps {
  items: readonly UsageCount[];
  /** The agent's counted runs in the period (the share's denominator). */
  totalRuns: number;
  color: string;
  emptyText: string;
}

export function UsageBars({ items, totalRuns, color, emptyText }: UsageBarsProps) {
  const t = useTranslations("agentPerformance.statsTab");
  if (items.length === 0) return <p style={s.empty}>{emptyText}</p>;
  return (
    <ul style={s.list}>
      {items.map((item) => {
        const share = usageShare(item.runs, totalRuns);
        return (
          <li key={item.key} title={`${item.label} · ${t("runsOf", { runs: item.runs, total: totalRuns })}`}>
            <BarRow label={item.label} value={share} max={100} color={color} suffix={t("share", { percent: share })} />
          </li>
        );
      })}
    </ul>
  );
}

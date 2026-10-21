/* CostDonut — one cost-breakdown panel ("By agent" / "By model") as a donut of estimated cost.
   Null keys read "Deleted agents" / "Unknown model". The empty copy is shown when no run was priced. */
"use client";

import React from "react";
import { useTranslations } from "next-intl";
import type { CostSegment } from "@devdigest/shared";
import { CostText } from "@/components/cost-text";
import { seriesColor } from "./constants";
import { DonutChart } from "./DonutChart";
import { s } from "./styles";

export interface CostDonutProps {
  title: string;
  segments: readonly CostSegment[];
  /** What a null segment key stands for: deleted agents (by agent) or an unknown model (by model). */
  nullLabel: "deletedAgents" | "unknownModel";
  /** false when a section label above already names the panel (the title stays its accessible name). */
  showTitle?: boolean;
}

export function CostDonut({ title, segments, nullLabel, showTitle = true }: CostDonutProps) {
  const t = useTranslations("agentPerformance.breakdown");
  const data = segments.map((seg, i) => ({
    key: seg.key ?? "∅",
    label: seg.label ?? t(nullLabel),
    value: seg.value,
    color: seriesColor(i),
  }));
  return (
    <section aria-label={title} style={s.panel}>
      {showTitle && <h3 style={s.panelTitle}>{title}</h3>}
      {data.length === 0 ? (
        <p style={s.empty}>{t("noCost")}</p>
      ) : (
        <DonutChart data={data} label={title} formatValue={(v) => <CostText usd={v} />} />
      )}
    </section>
  );
}

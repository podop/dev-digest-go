/* CostBreakdown — "Cost breakdown" (mockup N11): estimated cost by agent and by model as two
   donuts, plus the one estimate note that is always visible (FR17, NFR5). */
"use client";

import React from "react";
import { useTranslations } from "next-intl";
import type { CostSegment } from "@devdigest/shared";
import { Card, SectionLabel } from "@devdigest/ui";
import { CostDonut, EstimateNote } from "@/components/agent-metrics";
import { s } from "./styles";

export interface CostBreakdownProps {
  byAgent: readonly CostSegment[];
  byModel: readonly CostSegment[];
}

export function CostBreakdown({ byAgent, byModel }: CostBreakdownProps) {
  const t = useTranslations("agentPerformance");
  return (
    <section style={s.section} aria-label={t("dashboard.costBreakdown")}>
      <SectionLabel icon="DollarSign">{t("dashboard.costBreakdown")}</SectionLabel>
      <div style={s.panels}>
        <Card>
          <CostDonut title={t("breakdown.byAgent")} segments={byAgent} nullLabel="deletedAgents" />
        </Card>
        <Card>
          <CostDonut title={t("breakdown.byModel")} segments={byModel} nullLabel="unknownModel" />
        </Card>
      </div>
      <EstimateNote />
    </section>
  );
}

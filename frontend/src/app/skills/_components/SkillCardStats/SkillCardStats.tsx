/* SkillCardStats — the stats footer of a skill card (grid card and editor sidebar item; mockup
   "3 agents  71% pull  74% accept"): linked agents, last-30-days pull rate and accept rate (green
   from 60%, amber below). An unknown rate reads "—". */
"use client";

import React from "react";
import { useTranslations } from "next-intl";
import type { Skill, SkillStatsSummary } from "@devdigest/shared";
import { acceptRateColor } from "@/components/agent-metrics";
import { CardStats } from "@/components/card-stats";
import { formatRate } from "../../helpers";

export function SkillCardStats({ skill, stats }: { skill: Skill; stats?: SkillStatsSummary }) {
  const t = useTranslations("skills");
  const pull = formatRate(stats?.pull_rate) ?? t("card.noStat");
  const accept = formatRate(stats?.accept_rate) ?? t("card.noStat");
  return (
    <CardStats
      title={t("card.statsTitle")}
      items={[
        { key: "agents", text: t("card.agents", { count: skill.used_by ?? 0 }) },
        { key: "pull", text: t("card.pull", { value: pull }) },
        { key: "accept", text: t("card.accept", { value: accept }), color: acceptRateColor(stats?.accept_rate) },
      ]}
    />
  );
}

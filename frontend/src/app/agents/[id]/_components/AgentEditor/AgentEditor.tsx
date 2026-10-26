/* AgentEditor — the agent's tabs: Config (model + system prompt), Skills
   (linked skills + their order), Context (attached repo documents) and Evals
   (eval cases + run metrics), Stats (period metrics + cost estimates) and CI (export to GitHub Actions
   + CI runs). The tab lives in ?tab= (resolved by the route); `period` is the Stats tab's URL period. */
"use client";

import React from "react";
import { useTranslations } from "next-intl";
import { Tabs } from "@devdigest/ui";
import type { Agent } from "@devdigest/shared";
import { PluginBadge } from "@/components/plugin-badge";
import { DEFAULT_STATS_PRESET, type StatsPeriod } from "@/lib/stats-period";
import { ConfigTab } from "./_components/ConfigTab";
import { CiTab } from "./_components/CiTab";
import { ContextTab } from "./_components/ContextTab";
import { EvalsTab } from "./_components/EvalsTab";
import { SkillsTab } from "./_components/SkillsTab";
import { StatsTab } from "./_components/StatsTab";
import { TABS } from "./constants";
import { s } from "./styles";

const DEFAULT_PERIOD: StatsPeriod = { key: DEFAULT_STATS_PRESET };

interface AgentEditorProps {
  agent: Agent;
  tab: string;
  onTab: (t: string) => void;
  period?: StatsPeriod;
}

export function AgentEditor({ agent, tab, onTab, period = DEFAULT_PERIOD }: AgentEditorProps) {
  const t = useTranslations("agents");
  const tabs = TABS.map((tb) => ({ key: tb.key, label: t(tb.labelKey), icon: tb.icon }));
  return (
    <div style={s.wrap}>
      {agent.plugin && (
        <div style={s.pluginBar}>
          <PluginBadge plugin={agent.plugin} />
        </div>
      )}
      <div style={s.tabsBar}>
        <Tabs tabs={tabs} value={tab} onChange={onTab} pad="0 24px" />
      </div>
      <div style={s.body}>
        {tab === "skills" && <SkillsTab key={agent.id} agent={agent} />}
        {tab === "context" && <ContextTab key={agent.id} agent={agent} />}
        {tab === "evals" && <EvalsTab key={agent.id} agent={agent} />}
        {tab === "stats" && <StatsTab key={agent.id} agent={agent} period={period} />}
        {tab === "ci" && <CiTab key={agent.id} agent={agent} />}
        {tab !== "skills" && tab !== "context" && tab !== "evals" && tab !== "stats" && tab !== "ci" && <ConfigTab key={agent.id} agent={agent} />}
      </div>
    </div>
  );
}

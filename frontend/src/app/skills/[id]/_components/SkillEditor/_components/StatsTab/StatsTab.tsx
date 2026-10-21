/* StatsTab — last-30-days usage of one skill (GET /skills/:id/stats), mockup Skill Editor · Stats:
   four cards (agents using it, pull frequency, accept rate as a ring, findings), the agents using
   the skill (each opens its Skills tab) and findings by category as a donut. Until the skill was
   part of a run: an empty state, but still the agents that link it (the only place the editor
   lists them). */
"use client";

import Link from "next/link";
import { useTranslations } from "next-intl";
import { Card, EmptyState, ErrorState, SectionLabel, Skeleton } from "@devdigest/ui";
import type { SkillAgentRef } from "@devdigest/shared";
import { AgentGlyph, DonutChart, StatCard, categoryColor, formatPercent } from "@/components/agent-metrics";
import { useSkillStats } from "@/lib/hooks";
import { neverAttached } from "./helpers";
import { s } from "./styles";

/** The agents that link this skill (each opens its Skills tab). */
function UsedBy({ agents }: { agents: SkillAgentRef[] }) {
  const t = useTranslations("skills");
  return (
    <Card>
      <SectionLabel icon="Cpu">{t("stats.usedBy")}</SectionLabel>
      {agents.length === 0 ? (
        <p style={s.muted}>{t("stats.notUsed")}</p>
      ) : (
        <ul style={s.agents}>
          {agents.map((a) => (
            <li key={a.id}>
              <Link href={`/agents/${encodeURIComponent(a.id)}?tab=skills`} aria-label={a.name} style={s.agent(a.enabled)}>
                <AgentGlyph muted={!a.enabled} />
                <span style={s.agentName}>{a.name}</span>
                <span className="mono" style={s.open}>
                  {t("stats.open")}
                </span>
              </Link>
            </li>
          ))}
        </ul>
      )}
    </Card>
  );
}

export function StatsTab({ skillId }: { skillId: string }) {
  const t = useTranslations("skills");
  const tm = useTranslations("agentPerformance.metrics");
  const { data: stats, isLoading, isError, refetch } = useSkillStats(skillId);

  if (isLoading) return <Skeleton height={200} />;
  if (isError || !stats) return <ErrorState title={t("stats.loadError")} onRetry={() => refetch()} />;
  if (neverAttached(stats.runs_attached)) {
    return (
      <div style={s.wrap}>
        <EmptyState icon="BarChart" title={t("stats.empty.title")} body={t("stats.empty.body")} />
        <UsedBy agents={stats.used_by} />
      </div>
    );
  }

  const pull = formatPercent(stats.pull_rate);
  const accept = formatPercent(stats.accept_rate);
  const categories = stats.by_category.map((c) => ({ key: c.key, label: c.key, value: c.count, color: categoryColor(c.key) }));

  return (
    <div style={s.wrap}>
      <div role="group" aria-label={t("stats.title", { days: stats.window_days })} style={s.cards}>
        <StatCard
          label={t("stats.usedByCard")}
          value={stats.used_by.length}
          unit={t("stats.agentsUnit", { count: stats.used_by.length })}
        />
        <StatCard label={t("stats.pullRate")} value={pull} unit={tm("percentUnit")}>
          {t("stats.pullRateHint", { cited: stats.runs_cited, attached: stats.runs_attached })}
        </StatCard>
        <StatCard label={t("stats.acceptRate")} value={accept} unit={tm("percentUnit")} ring={accept}>
          {t("stats.acceptRateHint", { accepted: stats.accepted, dismissed: stats.dismissed })}
        </StatCard>
        <StatCard label={t("stats.findingsDays", { days: stats.window_days })} value={stats.findings} />
      </div>

      <div style={s.grid}>
        <UsedBy agents={stats.used_by} />
        <Card>
          <SectionLabel icon="Tag">{t("stats.byCategory")}</SectionLabel>
          {categories.length === 0 ? (
            <p style={s.muted}>{t("stats.none")}</p>
          ) : (
            <div style={s.donut}>
              <DonutChart data={categories} label={t("stats.byCategory")} formatValue={(v) => v} />
            </div>
          )}
        </Card>
      </div>
    </div>
  );
}

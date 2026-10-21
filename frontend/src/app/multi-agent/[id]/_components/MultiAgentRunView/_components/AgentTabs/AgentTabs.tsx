/* AgentTabs — Tabs view: one tab per agent with its score, a summary card and expandable
   finding cards with Accept / Dismiss / Learn / Turn into eval case (specs/multi-agent-review.md FR18, FR21). */
"use client";

import React from "react";
import { useTranslations } from "next-intl";
import { Button, CircularScore, Markdown, Tabs } from "@devdigest/ui";
import type { FindingActionKind, MultiAgentRun } from "@devdigest/shared";
import { useMultiAgentFindingAction } from "@/lib/hooks";
import { RunMeta } from "../RunMeta";
import { StatusLabel } from "../StatusLabel";
import { TabFinding } from "./_components/TabFinding";
import { s } from "./styles";

export function AgentTabs({ run, onViewTrace }: { run: MultiAgentRun; onViewTrace: (runId: string) => void }) {
  const t = useTranslations("runs");
  const [selected, setSelected] = React.useState<string | null>(null);
  const act = useMultiAgentFindingAction(run.id);

  const deleted = t("multiAgent.results.deletedAgent");
  const tabs = run.columns.map((c) => ({
    key: c.run_id,
    label: c.agent_name || deleted,
    ...(c.score != null ? { count: c.score } : {}),
  }));
  // A selection that is no longer in the run falls back to the first agent.
  const active = run.columns.find((c) => c.run_id === selected) ?? run.columns[0];
  if (!active) return null;
  const name = active.agent_name || deleted;

  return (
    <div>
      <div style={s.tabs}>
        <Tabs tabs={tabs} value={active.run_id} onChange={setSelected} pad="0" />
      </div>
      <div style={s.panel}>
        <section style={s.summary} aria-label={t("multiAgent.results.summaryOf", { agent: name })}>
          {active.score != null && <CircularScore score={active.score} size={56} stroke={5} />}
          <div style={s.summaryMain}>
            <div style={s.summaryHead}>
              <h3 style={s.name}>{name}</h3>
              <StatusLabel status={active.status} />
              <RunMeta durationMs={active.duration_ms} costUsd={active.cost_usd} style={s.meta} />
            </div>
            {active.status === "failed" && <p style={s.error}>{active.error || t("multiAgent.results.failedNoError")}</p>}
            {active.summary ? (
              <div style={s.prose}>
                <Markdown>{active.summary}</Markdown>
              </div>
            ) : (
              <p style={s.muted}>{t("tabs.noSummary")}</p>
            )}
          </div>
          <Button kind="secondary" size="sm" icon="Activity" onClick={() => onViewTrace(active.run_id)}>
            {t("viewTrace")}
          </Button>
        </section>

        {active.findings.length === 0 && active.status === "done" && <p style={s.muted}>{t("column.noFindings")}</p>}
        {active.findings.length > 0 && (
          <ul style={s.list} aria-label={t("multiAgent.results.findingsOf", { agent: name })}>
            {active.findings.map((f) => (
              <TabFinding
                key={f.id}
                finding={f}
                agentId={active.agent_id || null}
                prId={run.pr_id}
                pendingAction={act.isPending && act.variables?.findingId === f.id ? act.variables.action : null}
                onAction={(action: FindingActionKind) => act.mutate({ findingId: f.id, action })}
              />
            ))}
          </ul>
        )}
      </div>
    </div>
  );
}

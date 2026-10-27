/* AgentTabs — Tabs view: one tab per agent (icon, name, score), a summary card in the agent's
   colour and expandable finding cards with Accept / Dismiss / Learn / Turn into eval case
   (specs/multi-agent-review.md FR18, FR21; Claude Design screen_multiagent.jsx TabsView). */
"use client";

import React from "react";
import { useTranslations } from "next-intl";
import { CircularScore, Icon, Markdown } from "@devdigest/ui";
import type { FindingActionKind, MultiAgentRun } from "@devdigest/shared";
import { agentPersona } from "@/app/multi-agent/_components/AgentAvatar";
import { useMultiAgentFindingAction } from "@/lib/hooks";
import { RunMeta } from "../RunMeta";
import { StatusLabel } from "../StatusLabel";
import { TraceLink } from "../TraceLink";
import { TabFinding } from "./_components/TabFinding";
import { s } from "./styles";

export function AgentTabs({ run, onViewTrace }: { run: MultiAgentRun; onViewTrace: (runId: string) => void }) {
  const t = useTranslations("runs");
  const [selected, setSelected] = React.useState<string | null>(null);
  const act = useMultiAgentFindingAction(run.id);

  const deleted = t("multiAgent.results.deletedAgent");
  // A selection that is no longer in the run falls back to the first agent.
  const active = run.columns.find((c) => c.run_id === selected) ?? run.columns[0];
  if (!active) return null;
  const name = active.agent_name || deleted;
  const persona = agentPersona(name, active.agent_id);

  return (
    <div>
      <div role="tablist" style={s.tabs}>
        {run.columns.map((c) => {
          const on = c.run_id === active.run_id;
          const tabName = c.agent_name || deleted;
          const p = agentPersona(tabName, c.agent_id);
          const I = Icon[p.icon];
          return (
            <button key={c.run_id} type="button" role="tab" aria-selected={on} onClick={() => setSelected(c.run_id)} style={s.tab(on, p.color)}>
              <I size={15} aria-hidden style={s.tabIcon(on, p.color)} />
              <span style={s.tabName(on)}>{tabName}</span>
              {c.score != null && (
                <span className="tnum" style={s.tabScore(c.score)}>
                  {c.score}
                </span>
              )}
            </button>
          );
        })}
      </div>
      <div role="tabpanel" style={s.panel}>
        <section style={s.summary(persona.color)} aria-label={t("multiAgent.results.summaryOf", { agent: name })}>
          {active.score != null && <CircularScore score={active.score} size={44} />}
          <div style={s.summaryMain}>
            <div style={s.summaryHead}>
              <h3 style={s.name(persona.color)}>{name}</h3>
              {active.status !== "done" && <StatusLabel status={active.status} />}
            </div>
            {active.status === "failed" && <p style={s.error}>{active.error || t("multiAgent.results.failedNoError")}</p>}
            <div style={s.prose}>{active.summary ? <Markdown>{active.summary}</Markdown> : <p style={s.muted}>{t("tabs.noSummary")}</p>}</div>
          </div>
          <div style={s.summaryAside}>
            <TraceLink onClick={() => onViewTrace(active.run_id)} />
            <RunMeta durationMs={active.duration_ms} costUsd={active.cost_usd} style={s.meta} />
          </div>
        </section>

        {active.findings.length === 0 && active.status === "done" && <p style={s.muted}>{t("column.noFindings")}</p>}
        {active.findings.length > 0 && (
          <ul key={active.run_id} style={s.list} aria-label={t("multiAgent.results.findingsOf", { agent: name })}>
            {active.findings.map((f, i) => (
              <TabFinding
                key={f.id}
                finding={f}
                defaultExpanded={i === 0}
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

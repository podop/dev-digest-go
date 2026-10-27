/* AgentColumns — Columns view: one card per agent (icon tile, name, "time · cost", score ring),
   its findings, and a footer with "View trace" and the findings count (specs/multi-agent-review.md
   FR17; Claude Design screen_multiagent.jsx ColumnsView). Up to five columns per row. */
"use client";

import { useTranslations } from "next-intl";
import { CircularScore } from "@devdigest/ui";
import type { AgentColumn } from "@devdigest/shared";
import { AgentAvatar, agentPersona } from "@/app/multi-agent/_components/AgentAvatar";
import { RunMeta } from "../RunMeta";
import { StatusLabel } from "../StatusLabel";
import { TraceLink } from "../TraceLink";
import { FindingRow } from "./_components/FindingRow";
import { s } from "./styles";

export function AgentColumns({
  columns,
  onViewTrace,
}: {
  columns: readonly AgentColumn[];
  onViewTrace: (runId: string) => void;
}) {
  const t = useTranslations("runs");
  return (
    <div style={s.scroller}>
      <div style={s.grid(columns.length)}>
        {columns.map((c) => {
          const name = c.agent_name || t("multiAgent.results.deletedAgent");
          const persona = agentPersona(name, c.agent_id);
          return (
            <section key={c.run_id} style={s.column} aria-label={name}>
              <div style={s.head(persona.color)}>
                <AgentAvatar persona={persona} />
                <div style={s.headText}>
                  <h3 style={s.name} title={name}>
                    {name}
                  </h3>
                  <RunMeta durationMs={c.duration_ms} costUsd={c.cost_usd} style={s.meta} />
                </div>
                {c.score != null && <CircularScore score={c.score} size={32} stroke={3.5} />}
              </div>

              <div style={s.body}>
                {c.status !== "done" && <StatusLabel status={c.status} />}
                {c.status === "failed" && <p style={s.error}>{c.error || t("multiAgent.results.failedNoError")}</p>}
                {c.status === "done" && c.findings.length === 0 && <p style={s.empty}>{t("column.noFindings")}</p>}
                {c.findings.length > 0 && (
                  <ul style={s.list} aria-label={t("multiAgent.results.findingsOf", { agent: name })}>
                    {c.findings.map((f) => (
                      <FindingRow key={f.id} finding={f} />
                    ))}
                  </ul>
                )}
              </div>

              <div style={s.footer}>
                <TraceLink onClick={() => onViewTrace(c.run_id)} />
                <span style={s.count}>{t("column.findingsCount", { count: c.findings.length })}</span>
              </div>
            </section>
          );
        })}
      </div>
    </div>
  );
}

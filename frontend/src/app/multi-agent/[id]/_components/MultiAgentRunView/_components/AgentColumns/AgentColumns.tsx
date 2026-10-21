/* AgentColumns — Columns view: one column per agent with status, score ring, "time · cost",
   its findings and "View trace" (specs/multi-agent-review.md FR17). More than four columns scroll horizontally. */
"use client";

import React from "react";
import { useTranslations } from "next-intl";
import { Button, CircularScore } from "@devdigest/ui";
import type { AgentColumn } from "@devdigest/shared";
import { RunMeta } from "../RunMeta";
import { StatusLabel } from "../StatusLabel";
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
    <div style={s.row}>
      {columns.map((c) => {
        const name = c.agent_name || t("multiAgent.results.deletedAgent");
        return (
          <section key={c.run_id} style={s.column} aria-label={name}>
            <div style={s.head}>
              <div style={s.headText}>
                <h3 style={s.name}>{name}</h3>
                <StatusLabel status={c.status} />
                <RunMeta durationMs={c.duration_ms} costUsd={c.cost_usd} style={s.meta} />
              </div>
              {c.score != null && <CircularScore score={c.score} />}
            </div>

            {c.status === "failed" && <p style={s.error}>{c.error || t("multiAgent.results.failedNoError")}</p>}
            {c.status === "done" && (
              <p style={s.count}>
                {c.findings.length === 0
                  ? t("column.noFindings")
                  : t("column.findingsCount", { count: c.findings.length })}
              </p>
            )}
            {c.findings.length > 0 && (
              <ul style={s.list} aria-label={t("multiAgent.results.findingsOf", { agent: name })}>
                {c.findings.map((f) => (
                  <FindingRow key={f.id} finding={f} />
                ))}
              </ul>
            )}

            <div style={s.footer}>
              <Button kind="secondary" size="sm" icon="Activity" onClick={() => onViewTrace(c.run_id)}>
                {t("viewTrace")}
              </Button>
            </div>
          </section>
        );
      })}
    </div>
  );
}

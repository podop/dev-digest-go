/* AgentRow — one agent of the table plus its expandable detail row (FR13, FR15, FR16). The agent
   name is a real <button aria-expanded> (the keyboard toggle); a click anywhere else on the row
   toggles too, except on links and buttons. The accept rate carries ↑/↓ against the previous
   window; the detail row adds the agent's runs sparkline (mockup N11). Null values render "—". */
"use client";

import React from "react";
import Link from "next/link";
import { useTranslations } from "next-intl";
import type { AgentPerfRow } from "@devdigest/shared";
import { Sparkline } from "@devdigest/ui";
import { AcceptRateText, AgentGlyph, RunBreakdown, formatDurationS } from "@/components/agent-metrics";
import { CostText } from "@/components/cost-text";
import { agentStatsHref, type StatsPeriod } from "@/lib/stats-period";
import { formatLastRun } from "../../../../helpers";
import { s } from "./styles";

/** Number of columns incl. View — the detail row spans them all. */
const COLUMNS = 7;

export interface AgentRowProps {
  row: AgentPerfRow;
  period: StatsPeriod;
  minDecisions: number;
  open: boolean;
  onToggle: () => void;
}

export function AgentRow({ row, period, minDecisions, open, onToggle }: AgentRowProps) {
  const t = useTranslations("agentPerformance");
  const m = row.metrics;
  const deleted = row.agent_id === null;
  const name = deleted ? t("breakdown.deletedAgents") : row.agent_name;
  const seconds = formatDurationS(m.avg_duration_ms);
  const lastRun = formatLastRun(m.last_run_at);
  const dash = <span style={s.muted}>{t("metrics.noValue")}</span>;
  const cell = deleted ? { ...s.td, color: "var(--text-muted)" } : s.td;

  const onRowClick = (e: React.MouseEvent<HTMLTableRowElement>) => {
    if ((e.target as HTMLElement).closest("a, button")) return;
    onToggle();
  };

  return (
    <>
      <tr style={s.row} onClick={onRowClick}>
        <td style={cell}>
          <button type="button" style={s.toggle} aria-expanded={open} onClick={onToggle}>
            <span aria-hidden style={s.chevron}>
              {open ? "▾" : "▸"}
            </span>
            <AgentGlyph muted={deleted} />
            <span style={s.name} title={name}>
              {name}
            </span>
          </button>
        </td>
        <td className="tnum" style={cell}>
          {m.runs}
        </td>
        <td style={cell}>
          <CostText usd={m.avg_cost_usd} />
        </td>
        <td className="tnum" style={cell}>
          {seconds == null ? dash : t("metrics.seconds", { value: seconds })}
        </td>
        <td style={cell}>
          <AcceptRateText
            rate={m.accept_rate}
            decisions={m.decisions}
            smallSample={m.small_sample}
            minDecisions={minDecisions}
            previousRate={row.previous.accept_rate}
          />
        </td>
        <td className="tnum" style={cell}>
          {lastRun ?? dash}
        </td>
        <td style={cell}>
          {row.agent_id === null ? (
            dash
          ) : (
            <Link href={agentStatsHref(row.agent_id, period)} style={s.link} aria-label={t("dashboard.table.viewAgent", { name })}>
              {t("dashboard.table.view")}
            </Link>
          )}
        </td>
      </tr>
      {open && (
        <tr>
          <td colSpan={COLUMNS} style={s.detail}>
            {m.runs > 0 && (
              <div style={s.trend} data-testid="row-runs-trend">
                <Sparkline data={row.runs_trend.map((p) => p.value)} color="var(--accent)" w={160} h={34} />
                <span style={s.trendCaption}>{t("dashboard.table.runsOverPeriod")}</span>
              </div>
            )}
            <RunBreakdown metrics={m} />
          </td>
        </tr>
      )}
    </>
  );
}

/* RunHistory — "Run history" (mockup Agent Editor · Stats): the agent's newest counted runs of the
   period (≤ 20, from the stats response): start time, PR (link to the PR page), tokens, estimated
   cost, findings, source (local / CI, plus failed / cancelled) and "View trace", which opens the
   run trace drawer. CI runs have no trace, so they get no button. Unknown values read "—". */
"use client";

import React from "react";
import Link from "next/link";
import { useTranslations } from "next-intl";
import type { RunHistoryItem } from "@devdigest/shared";
import { RunTraceDrawer } from "@/components/run-trace-drawer";
import { CostText } from "@/components/cost-text";
import { formatRunTime, formatTokenCount } from "../../helpers";
import { s } from "./styles";

export function RunHistory({ runs, agentName }: { runs: readonly RunHistoryItem[]; agentName: string }) {
  const t = useTranslations("agentPerformance.statsTab.history");
  const [open, setOpen] = React.useState<RunHistoryItem | null>(null);
  const dash = <span style={s.muted}>{t("noValue")}</span>;

  return (
    <>
      <div style={s.scroll}>
        <table style={s.table} aria-label={t("title")}>
          <thead>
            <tr>
              {(["time", "pr", "tokens", "cost", "findings", "source"] as const).map((col) => (
                <th key={col} scope="col" style={s.th}>
                  {t(`columns.${col}`)}
                </th>
              ))}
              <th scope="col" style={s.th}>
                <span style={s.srOnly}>{t("columns.trace")}</span>
              </th>
            </tr>
          </thead>
          <tbody>
            {runs.map((run) => (
              <tr key={run.run_id}>
                <td className="mono tnum" style={s.td}>
                  {formatRunTime(run.ran_at)}
                </td>
                <td className="mono tnum" style={s.td}>
                  {run.pr ? (
                    <Link href={`/repos/${encodeURIComponent(run.pr.repo_id)}/pulls/${run.pr.number}`} style={s.link}>
                      #{run.pr.number}
                    </Link>
                  ) : (
                    dash
                  )}
                </td>
                <td className="tnum" style={s.td}>
                  {run.tokens == null ? dash : formatTokenCount(run.tokens)}
                </td>
                <td style={s.td}>
                  <CostText usd={run.cost_usd} />
                </td>
                <td className="tnum" style={s.td}>
                  {run.findings_count ?? dash}
                </td>
                <td style={s.td}>
                  <span style={s.source(run.source)}>{t(`source.${run.source}`)}</span>
                  {run.status !== "done" && <span style={s.status}>{t(`status.${run.status}`)}</span>}
                </td>
                <td style={s.td}>
                  {run.has_trace && (
                    <button type="button" className="mono" style={s.traceButton} onClick={() => setOpen(run)}>
                      {t("viewTrace")}
                    </button>
                  )}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      {open && (
        <RunTraceDrawer
          key={open.run_id}
          runId={open.run_id}
          agentName={agentName}
          prNumber={open.pr?.number ?? null}
          onClose={() => setOpen(null)}
        />
      )}
    </>
  );
}

/* AgentRunsTable — the agent view's runs of the period, newest first: a heading row
   ("RECENT RUNS", the compare hint and the Compare button) over a table of checkbox, RAN AT,
   VERSION, one bar cell per metric, PASS and COST. Every finished run has a labelled native
   checkbox; a running run cannot be selected. Loading, error and empty are told here so the
   heading stays; Compare is enabled by the caller passing `onCompare`. */
"use client";

import React from "react";
import { useTranslations } from "next-intl";
import { Button, ErrorState, Icon, Skeleton } from "@devdigest/ui";
import type { EvalRunSummary } from "@devdigest/shared";
import { CostText } from "@/components/cost-text";
import type { RunsTableSelection } from "@/components/eval-run-history";
import { MetricBarCell, PassCell } from "@/app/eval/_components/RunCells";
import { formatRunStamp } from "@/app/eval/helpers";
import { s } from "./styles";

export interface AgentRunsTableProps {
  /** Runs of the period, newest first; undefined until loaded. */
  runs: readonly EvalRunSummary[] | undefined;
  state: "loading" | "error" | "ready";
  onRetry: () => void;
  selection: RunsTableSelection;
  /** Set when exactly two finished runs are selected; Compare is disabled without it. */
  onCompare?: () => void;
}

export function AgentRunsTable({ runs, state, onRetry, selection, onCompare }: AgentRunsTableProps) {
  const t = useTranslations("eval.agentRuns");
  const tc = useTranslations("common");
  return (
    <section style={s.section} aria-label={t("heading")}>
      <div style={s.head}>
        <h2 style={s.title}>
          <Icon.History size={14} aria-hidden />
          {t("heading")}
        </h2>
        <p style={s.caption}>{t("caption")}</p>
        <span title={onCompare ? undefined : t("compareHint")}>
          <Button kind="secondary" icon="GitMerge" disabled={!onCompare} onClick={onCompare}>
            {t("compare")}
          </Button>
        </span>
      </div>
      {state === "error" ? (
        <ErrorState title={t("loadError")} onRetry={onRetry} retryLabel={tc("actions.retry")} />
      ) : state === "loading" || !runs ? (
        <Skeleton height={80} />
      ) : runs.length === 0 ? (
        <p style={s.empty}>{t("empty")}</p>
      ) : (
        <div style={s.scroll}>
          <table style={s.table} aria-label={t("tableLabel")}>
            <thead>
              <tr>
                <th style={s.th} scope="col">
                  <span style={s.srOnly}>{t("columns.select")}</span>
                </th>
                <th style={s.th} scope="col">{t("columns.ranAt")}</th>
                <th style={s.th} scope="col">{t("columns.version")}</th>
                <th style={s.th} scope="col">{t("columns.recall")}</th>
                <th style={s.th} scope="col">{t("columns.precision")}</th>
                <th style={s.th} scope="col">{t("columns.citation")}</th>
                <th style={s.th} scope="col">{t("columns.pass")}</th>
                <th style={{ ...s.th, ...s.num }} scope="col">{t("columns.cost")}</th>
              </tr>
            </thead>
            <tbody>
              {runs.map((run) => (
                <RunRow key={run.id} run={run} selection={selection} />
              ))}
            </tbody>
          </table>
        </div>
      )}
    </section>
  );
}

function RunRow({ run, selection }: { run: EvalRunSummary; selection: RunsTableSelection }) {
  const t = useTranslations("eval.agentRuns");
  const stamp = formatRunStamp(run.started_at);
  const checked = selection.selectedIds.includes(run.id);
  const selectable = run.status !== "running";
  return (
    <tr style={checked ? s.selected : undefined}>
      <td style={s.td}>
        <input
          type="checkbox"
          style={s.checkbox}
          checked={checked}
          disabled={!selectable}
          title={selectable ? undefined : t("runningNotSelectable")}
          aria-label={t("selectRun", { version: run.agent_version, date: stamp })}
          onChange={() => selection.onToggle(run.id)}
        />
      </td>
      <td className="mono tnum" style={s.td}>{stamp}</td>
      <td className="mono" style={{ ...s.td, ...s.version }}>{t("version", { version: run.agent_version })}</td>
      <MetricBarCell ratio={run.recall} metric="recall" />
      <MetricBarCell ratio={run.precision} metric="precision" />
      <MetricBarCell ratio={run.citation_accuracy} metric="citation_accuracy" />
      <PassCell run={run} />
      <td style={{ ...s.td, ...s.num }}>
        <CostText usd={run.cost_usd} />
      </td>
    </tr>
  );
}

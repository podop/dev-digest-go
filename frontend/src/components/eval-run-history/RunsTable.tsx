/* RunsTable — eval runs, newest first: date, version, model, pass count, the three
   metrics (whole %, "—" when undefined) and cost. With `selection` every finished run
   gets a labelled native checkbox (keyboard-operable, NFR8); a running run cannot be
   selected. Shared by the per-agent dashboard, the cross-agent recent runs and (variant `skill`:
   skill version + host-agent column) the skill's Evals tab. */
"use client";

import React from "react";
import { useTranslations } from "next-intl";
import { CostText } from "@/components/cost-text";
import { toPercent } from "@/components/eval-metrics";
import { formatRunDate, isSelectable, runVersion, type RunsTableRow } from "./helpers";
import { s } from "./styles";

export interface RunsTableSelection {
  selectedIds: readonly string[];
  onToggle: (runId: string) => void;
}

export interface RunsTableProps {
  runs: readonly RunsTableRow[];
  /** `skill` adds the host-agent column; the default `agent` table lists one agent's runs. */
  variant?: "agent" | "skill";
  /** Adds a checkbox column; omit for a read-only table. */
  selection?: RunsTableSelection;
}

export function RunsTable({ runs, variant = "agent", selection }: RunsTableProps) {
  const t = useTranslations("eval.runsTable");
  const showAgent = variant === "skill";
  return (
    <div style={s.scroll}>
      <table style={s.table} aria-label={t("caption")}>
        <thead>
          <tr>
            {selection && (
              <th style={{ ...s.th, position: "relative" }} scope="col">
                <span style={s.srOnly}>{t("columns.select")}</span>
              </th>
            )}
            <th style={s.th} scope="col">{t("columns.ranAt")}</th>
            {showAgent && <th style={s.th} scope="col">{t("columns.agent")}</th>}
            <th style={s.th} scope="col">{t("columns.version")}</th>
            <th style={s.th} scope="col">{t("columns.model")}</th>
            <th style={s.th} scope="col">{t("columns.result")}</th>
            <th style={{ ...s.th, ...s.num }} scope="col">{t("columns.recall")}</th>
            <th style={{ ...s.th, ...s.num }} scope="col">{t("columns.precision")}</th>
            <th style={{ ...s.th, ...s.num }} scope="col">{t("columns.citation")}</th>
            <th style={{ ...s.th, ...s.num }} scope="col">{t("columns.cost")}</th>
          </tr>
        </thead>
        <tbody>
          {runs.map((run) => (
            <RunRow key={run.id} run={run} variant={variant} selection={selection} />
          ))}
        </tbody>
      </table>
    </div>
  );
}

function RunRow({ run, variant, selection }: { run: RunsTableRow; variant: "agent" | "skill"; selection?: RunsTableSelection }) {
  const t = useTranslations("eval.runsTable");
  const date = formatRunDate(run.started_at);
  const version = runVersion(run, variant);
  const checked = selection?.selectedIds.includes(run.id) ?? false;
  const selectable = isSelectable(run);
  return (
    <tr style={checked ? s.selected : undefined}>
      {selection && (
        <td style={s.td}>
          <input
            type="checkbox"
            style={s.checkbox}
            checked={checked}
            disabled={!selectable}
            title={selectable ? undefined : t("runningNotSelectable")}
            aria-label={t("selectRun", { version, date })}
            onChange={() => selection.onToggle(run.id)}
          />
        </td>
      )}
      <td style={s.td}>{date}</td>
      {variant === "skill" && <td style={{ ...s.td, ...s.agent }}>{run.agent_name}</td>}
      <td style={s.td}>{t("version", { version })}</td>
      <td style={s.td} className="mono">{run.model}</td>
      <td style={s.td}>
        <RunResult run={run} />
      </td>
      <Metric ratio={run.recall} />
      <Metric ratio={run.precision} />
      <Metric ratio={run.citation_accuracy} />
      <td style={{ ...s.td, ...s.num }}>
        <CostText usd={run.cost_usd} />
      </td>
    </tr>
  );
}

function Metric({ ratio }: { ratio: number | null }) {
  const t = useTranslations("eval.runsTable");
  const percent = toPercent(ratio);
  return (
    <td className="tnum" style={{ ...s.td, ...s.num, ...(percent == null ? s.muted : null) }}>
      {percent == null ? t("noValue") : t("percent", { value: percent })}
    </td>
  );
}

function RunResult({ run }: { run: RunsTableRow }) {
  const t = useTranslations("eval.runsTable");
  if (run.status === "running") return <span>{t("running", { finished: run.cases_finished, total: run.cases_total })}</span>;
  if (run.status === "failed") return <span style={s.crit} title={run.error ?? undefined}>{t("failed")}</span>;
  return (
    <span className="tnum">
      {t("passed", { passed: run.cases_passed, total: run.cases_total })}
      {run.cases_errored > 0 && <span style={s.muted}>{t("erroredSuffix", { count: run.cases_errored })}</span>}
    </span>
  );
}

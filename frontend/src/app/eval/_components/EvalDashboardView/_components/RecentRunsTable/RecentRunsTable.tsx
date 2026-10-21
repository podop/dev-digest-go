/* RecentRunsTable — the latest runs across all agents on the overview: agent name, ran at,
   version, one bar cell per metric and PASS. No model or cost column. The column headers
   are for assistive technology only and are visually hidden. */
"use client";

import React from "react";
import { useTranslations } from "next-intl";
import type { EvalRecentRun } from "@devdigest/shared";
import { MetricBarCell, PassCell } from "@/app/eval/_components/RunCells";
import { formatRunStamp } from "@/app/eval/helpers";
import { s } from "./styles";

export function RecentRunsTable({ runs }: { runs: readonly EvalRecentRun[] }) {
  const t = useTranslations("eval.recentRuns");
  return (
    <div style={s.scroll}>
      <table style={s.table} aria-label={t("caption")}>
        <thead>
          <tr>
            <th style={s.th} scope="col"><span style={s.srOnly}>{t("columns.agent")}</span></th>
            <th style={s.th} scope="col"><span style={s.srOnly}>{t("columns.ranAt")}</span></th>
            <th style={s.th} scope="col"><span style={s.srOnly}>{t("columns.version")}</span></th>
            <th style={s.th} scope="col"><span style={s.srOnly}>{t("columns.recall")}</span></th>
            <th style={s.th} scope="col"><span style={s.srOnly}>{t("columns.precision")}</span></th>
            <th style={s.th} scope="col"><span style={s.srOnly}>{t("columns.citation")}</span></th>
            <th style={s.th} scope="col"><span style={s.srOnly}>{t("columns.pass")}</span></th>
          </tr>
        </thead>
        <tbody>
          {runs.map((run) => (
            <tr key={run.id} data-testid="eval-recent-run">
              <td style={{ ...s.td, ...s.agent }}>{run.agent_name}</td>
              <td className="mono tnum" style={{ ...s.td, ...s.stamp }}>{formatRunStamp(run.started_at)}</td>
              <td className="mono" style={{ ...s.td, ...s.version }}>{t("version", { version: run.agent_version })}</td>
              <MetricBarCell ratio={run.recall} metric="recall" />
              <MetricBarCell ratio={run.precision} metric="precision" />
              <MetricBarCell ratio={run.citation_accuracy} metric="citation_accuracy" />
              <PassCell run={run} />
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

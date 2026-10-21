/* AgentTable — the sortable agent table (FR13–FR15). Headers are <button aria-sort> controls that
   only change the URL sort (no request); one row's detail panel is open at a time (local state). */
"use client";

import React from "react";
import { useTranslations } from "next-intl";
import type { AgentPerfRow } from "@devdigest/shared";
import type { StatsPeriod } from "@/lib/stats-period";
import { SORT_KEYS, type SortKey, type SortState } from "../../constants";
import { AgentRow } from "./_components/AgentRow";
import { s } from "./styles";

export interface AgentTableProps {
  rows: readonly AgentPerfRow[];
  sort: SortState;
  period: StatsPeriod;
  minDecisions: number;
  onSort: (key: SortKey) => void;
}

const rowKey = (row: AgentPerfRow) => row.agent_id ?? "deleted-agents";

export function AgentTable({ rows, sort, period, minDecisions, onSort }: AgentTableProps) {
  const t = useTranslations("agentPerformance.dashboard.table");
  const [openKey, setOpenKey] = React.useState<string | null>(null);

  return (
    <div style={s.scroll}>
      <table style={s.table} aria-label={t("label")}>
        <thead>
          <tr>
            {SORT_KEYS.map((key) => {
              const active = sort.key === key;
              return (
                <th
                  key={key}
                  scope="col"
                  style={s.th}
                  aria-sort={active ? (sort.dir === "asc" ? "ascending" : "descending") : "none"}
                >
                  <button type="button" style={s.sortButton} onClick={() => onSort(key)}>
                    {t(`columns.${key}`)}
                    <span aria-hidden style={s.arrow}>
                      {active ? (sort.dir === "asc" ? "↑" : "↓") : ""}
                    </span>
                  </button>
                </th>
              );
            })}
            <th scope="col" style={s.th}>
              {t("view")}
            </th>
          </tr>
        </thead>
        <tbody>
          {rows.map((row) => {
            const key = rowKey(row);
            return (
              <AgentRow
                key={key}
                row={row}
                period={period}
                minDecisions={minDecisions}
                open={openKey === key}
                onToggle={() => setOpenKey(openKey === key ? null : key)}
              />
            );
          })}
        </tbody>
      </table>
    </div>
  );
}

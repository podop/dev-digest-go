/* LeaderboardTable — the ranked PR table of the leaderboard (FR21). Pure view of `rows` (server
   order = rank by health); sort and "show all" are local state. Headers of the sortable columns are
   <button aria-sort> controls (desc → asc → server rank); the "#" column keeps the server rank while
   sorting. Top 10 are shown until "Show all N". One row's reviews panel is open at a time. */
"use client";

import React from "react";
import { useTranslations } from "next-intl";
import type { LeaderboardRow } from "@devdigest/shared";
import { SORT_KEYS, TOP_N, type SortKey, type SortState } from "./constants";
import { ariaSort, nextSort, rankRows, sortRows } from "./helpers";
import { LeaderboardTableRow } from "./_components/LeaderboardTableRow";
import { s } from "./styles";

export interface LeaderboardTableProps {
  /** Rows in server rank order (health desc; ties: fewer CRITICAL, repo, number). */
  rows: readonly LeaderboardRow[];
}

const isSortKey = (key: string): key is SortKey => (SORT_KEYS as readonly string[]).includes(key);

/** Column keys in display order. */
const COLUMNS = ["rank", "pr", "status", "size", "risk", "health", "findings", "rounds", "time", "cost"] as const;

export function LeaderboardTable({ rows }: LeaderboardTableProps) {
  const t = useTranslations("leaderboardTable");
  const headingId = React.useId();
  const [sort, setSort] = React.useState<SortState | null>(null);
  const [showAll, setShowAll] = React.useState(false);
  const [openId, setOpenId] = React.useState<string | null>(null);

  const sorted = sortRows(rankRows(rows), sort);
  const canCollapse = sorted.length > TOP_N;
  const visible = canCollapse && !showAll ? sorted.slice(0, TOP_N) : sorted;

  return (
    <section style={s.card} aria-labelledby={headingId}>
      <h2 id={headingId} style={s.header}>
        {t("title", { count: rows.length })}
      </h2>
      <div style={s.scroll}>
        <table style={s.table} aria-label={t("tableLabel")}>
          <thead>
            <tr>
              {COLUMNS.map((key) =>
                isSortKey(key) ? (
                  <th key={key} scope="col" style={s.th} aria-sort={ariaSort(sort, key)}>
                    <button type="button" style={s.sortButton} onClick={() => setSort(nextSort(sort, key))}>
                      {t(`columns.${key}`)}
                      <span aria-hidden style={s.arrow}>
                        {sort?.key === key ? (sort.dir === "asc" ? "↑" : "↓") : ""}
                      </span>
                    </button>
                  </th>
                ) : (
                  <th key={key} scope="col" style={s.th}>
                    {t(`columns.${key}`)}
                  </th>
                ),
              )}
              <th scope="col" style={s.th}>
                <span style={s.hidden}>{t("columns.expand")}</span>
              </th>
            </tr>
          </thead>
          <tbody>
            {visible.map(({ row, rank }) => (
              <LeaderboardTableRow
                key={row.pr_id}
                row={row}
                rank={rank}
                open={openId === row.pr_id}
                onToggle={() => setOpenId(openId === row.pr_id ? null : row.pr_id)}
              />
            ))}
          </tbody>
        </table>
      </div>
      {canCollapse && (
        <div style={s.footer}>
          <button type="button" style={s.more} onClick={() => setShowAll(!showAll)}>
            {showAll ? t("showTop", { count: TOP_N }) : t("showAll", { count: sorted.length })}
          </button>
        </div>
      )}
    </section>
  );
}

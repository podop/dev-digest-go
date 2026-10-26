/* LeaderboardTable — the ranked PR table of the leaderboard (FR21, D23). View of `rows` (server order =
   rank by health, ties by PR number); sort and "show all" are local state. Headers of the sortable
   columns are <button aria-sort> controls (desc → asc → server rank; Time starts ascending) and
   "Reset to rank" drops the sort at once; the "#" column keeps the server rank while sorting and
   missing values sort last. Top 10 are shown until "Show all N". A click on a row opens the PR; one
   row's top findings are expanded at a time. */
"use client";

import React from "react";
import { useRouter } from "next/navigation";
import { useTranslations } from "next-intl";
import type { LeaderboardRow } from "@devdigest/shared";
import { prDetailPath } from "@/lib/pr-urls";
import type { RepoLookup } from "../../helpers";
import { SORT_KEYS, TOP_N, type SortKey, type SortState } from "./constants";
import { ariaSort, nextSort, rankRows, sortRows } from "./helpers";
import { HealthInfo } from "./_components/HealthInfo";
import { LeaderboardTableRow } from "./_components/LeaderboardTableRow";
import { s } from "./styles";

export interface LeaderboardTableProps {
  /** Rows in server rank order (health desc; ties: PR number ascending, then repo). */
  rows: readonly LeaderboardRow[];
  /** The response's repositories by id (GitLab references and host labels). */
  repos?: RepoLookup;
}

const isSortKey = (key: string): key is SortKey => (SORT_KEYS as readonly string[]).includes(key);

/** Column keys in display order. */
const COLUMNS = ["rank", "pr", "status", "size", "risk", "health", "findings", "rounds", "time", "cost"] as const;

export function LeaderboardTable({ rows, repos }: LeaderboardTableProps) {
  const t = useTranslations("leaderboardTable");
  const router = useRouter();
  const headingId = React.useId();
  const [sort, setSort] = React.useState<SortState | null>(null);
  const [showAll, setShowAll] = React.useState(false);
  const [openId, setOpenId] = React.useState<string | null>(null);

  const sorted = sortRows(rankRows(rows), sort);
  const canCollapse = sorted.length > TOP_N;
  const visible = canCollapse && !showAll ? sorted.slice(0, TOP_N) : sorted;

  return (
    <section style={s.card} aria-labelledby={headingId}>
      <div style={s.header}>
        <h2 id={headingId} style={s.heading}>
          {t("title", { count: rows.length })}
        </h2>
        {sort && (
          <button type="button" style={s.reset} onClick={() => setSort(null)}>
            {t("resetSort")}
          </button>
        )}
      </div>
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
                    {key === "health" && <HealthInfo />}
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
                repos={repos}
                rank={rank}
                open={openId === row.pr_id}
                onToggle={() => setOpenId(openId === row.pr_id ? null : row.pr_id)}
                onOpen={() => router.push(prDetailPath(encodeURIComponent(row.repo_id), row.number))}
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

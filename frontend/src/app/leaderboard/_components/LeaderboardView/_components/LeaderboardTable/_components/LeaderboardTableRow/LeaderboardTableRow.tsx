/* LeaderboardTableRow — one ranked PR plus its expandable reviews row (FR21, D12). The chevron is a
   real <button aria-expanded> (the keyboard toggle); a click anywhere else on the row toggles too,
   except on links and buttons. Severity and risk always carry text, never colour only (NFR4).
   Long titles are truncated with a native tooltip (EC15). */
"use client";

import type React from "react";
import { useTranslations } from "next-intl";
import type { LeaderboardRow } from "@devdigest/shared";
import { Avatar, Badge, CircularScore } from "@devdigest/ui";
import { STATUS_META } from "@/app/repos/[repoId]/pulls/constants";
import { CostText } from "@/components/cost-text";
import { COLUMN_COUNT, RISK_COLOR, SIZE_COLOR } from "../../constants";
import { durationParts, openAgeParts, type DurationParts } from "../../helpers";
import { FindingChips } from "../FindingChips";
import { RowReviews } from "../RowReviews";
import { s } from "./styles";

export interface LeaderboardTableRowProps {
  row: LeaderboardRow;
  /** Position in the server rank (kept while the table is sorted). */
  rank: number;
  open: boolean;
  onToggle: () => void;
}

export function LeaderboardTableRow({ row, rank, open, onToggle }: LeaderboardTableRowProps) {
  const t = useTranslations("leaderboardTable");
  const status = STATUS_META[row.status] ?? STATUS_META.needs_review!;
  const pr = `${row.repo}#${row.number}`;
  const dash = <span style={s.muted}>{t("noValue")}</span>;

  const duration = (p: DurationParts) => t(`duration.${p.unit}`, { value: p.value });
  const timeLines: string[] = [];
  if (row.time_to_first_review_s !== null) timeLines.push(t("time.review", { value: duration(durationParts(row.time_to_first_review_s)) }));
  if (row.time_to_merge_s !== null) {
    timeLines.push(t("time.merge", { value: duration(durationParts(row.time_to_merge_s)) }));
  } else if (row.open_age_s !== null) {
    const age = openAgeParts(row.open_age_s);
    timeLines.push(t("time.open", { value: t(`openAge.${age.unit}`, { value: age.value }) }));
  }

  const onRowClick = (e: React.MouseEvent<HTMLTableRowElement>) => {
    if ((e.target as HTMLElement).closest("a, button")) return;
    onToggle();
  };

  return (
    <>
      <tr style={s.row} onClick={onRowClick}>
        <td className="tnum" style={{ ...s.td, ...s.rank }}>
          {rank}
        </td>
        <td style={{ ...s.td, ...s.prCell }}>
          <div style={s.prMeta}>
            <Avatar name={row.author} size={18} />
            <span className="mono" style={s.repoNumber}>
              {pr}
            </span>
            <span>{row.author}</span>
            {row.draft && <span>{t("draft")}</span>}
          </div>
          <div style={s.title} title={row.title}>
            {row.title}
          </div>
        </td>
        <td style={s.td}>
          <Badge dot color={status.c} bg="transparent">
            {t(`status.${status.labelKey}`)}
          </Badge>
        </td>
        <td style={s.td}>
          {row.size === null ? (
            dash
          ) : (
            <div style={s.stack}>
              <Badge color={SIZE_COLOR[row.size]} bg="transparent" style={s.sizeBadge(SIZE_COLOR[row.size])}>
                {row.size}
              </Badge>
              <span className="mono tnum" style={s.sizeStats}>
                {t.rich("sizeStats", {
                  additions: row.additions,
                  deletions: row.deletions,
                  files: row.files_count,
                  add: (chunks) => <span style={s.add}>{chunks}</span>,
                  del: (chunks) => <span style={s.del}>{chunks}</span>,
                })}
              </span>
            </div>
          )}
        </td>
        <td style={s.td}>
          <span style={s.risk}>
            <span aria-hidden style={s.riskDot(RISK_COLOR[row.risk])} />
            {t(`risk.${row.risk}`)}
          </span>
        </td>
        <td style={s.td}>{row.health === null ? dash : <CircularScore score={row.health} size={34} stroke={3} />}</td>
        <td style={s.td}>
          <FindingChips counts={row.findings} />
        </td>
        <td className="tnum" style={s.td}>
          {row.rounds}
        </td>
        <td style={s.td}>
          {timeLines.length === 0 ? (
            dash
          ) : (
            <div style={s.time}>
              {timeLines.map((line) => (
                <span key={line}>{line}</span>
              ))}
            </div>
          )}
        </td>
        <td style={s.td}>
          <CostText usd={row.cost_usd} />
        </td>
        <td style={s.td}>
          <button type="button" style={s.toggle} aria-expanded={open} aria-label={t("row.toggle", { pr })} onClick={onToggle}>
            <span aria-hidden>{open ? "▾" : "▸"}</span>
          </button>
        </td>
      </tr>
      {open && (
        <tr>
          <td colSpan={COLUMN_COUNT} style={s.detail}>
            <RowReviews row={row} />
          </td>
        </tr>
      )}
    </>
  );
}

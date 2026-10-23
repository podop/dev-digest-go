/* RowReviews — the expanded part of a leaderboard row (D12): the per-agent reviews of the row's
   window snapshot (agent, score, C/W/S) and a link to the PR. An unscored review shows "—". */
"use client";

import Link from "next/link";
import { useTranslations } from "next-intl";
import type { LeaderboardRow } from "@devdigest/shared";
import { prDetailPath } from "@/lib/pr-urls";
import { FindingChips } from "../FindingChips";
import { s } from "./styles";

export interface RowReviewsProps {
  row: LeaderboardRow;
}

export function RowReviews({ row }: RowReviewsProps) {
  const t = useTranslations("leaderboardTable");
  const pr = `${row.repo}#${row.number}`;
  return (
    <div>
      <h3 style={s.heading}>{t("detail.heading")}</h3>
      {row.reviews.length === 0 ? (
        <p style={s.empty}>{t("detail.empty")}</p>
      ) : (
        <ul style={s.list}>
          {row.reviews.map((review) => (
            <li key={review.review_id} style={s.item}>
              <span style={s.agent}>{review.agent_name}</span>
              <span className="tnum" style={s.score}>
                {review.score === null ? t("noValue") : t("detail.score", { score: review.score })}
              </span>
              <FindingChips counts={review.findings} />
            </li>
          ))}
        </ul>
      )}
      <Link href={prDetailPath(row.repo_id, row.number)} style={s.link}>
        {t("detail.openPr", { pr })}
      </Link>
    </div>
  );
}

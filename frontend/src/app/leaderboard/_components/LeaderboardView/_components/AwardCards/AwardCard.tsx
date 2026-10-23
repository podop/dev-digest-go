/* AwardCard — one award: icon chip, title, the big coloured metric with its unit word, the PR as a mono
   `repo#number` link to its detail page, and the PR title (one line, full text in the tooltip).
   No winner = a muted "—". Presentational. */
"use client";

import React from "react";
import Link from "next/link";
import { useTranslations } from "next-intl";
import type { LeaderboardAward } from "@devdigest/shared";
import { Icon } from "@devdigest/ui";
import { CostText } from "@/components/cost-text";
import { formatDuration, formatScore } from "../KpiTiles/helpers";
import { formatCount, type AwardSpec } from "./helpers";
import { s } from "./styles";

export interface AwardCardProps {
  spec: AwardSpec;
  award: LeaderboardAward | null;
  /** PR detail page of the winner; null when the repository is unknown (the reference is plain text). */
  href: string | null;
}

export function AwardCard({ spec, award, href }: AwardCardProps) {
  const t = useTranslations("leaderboardHighlights");
  const AwardIcon = Icon[spec.icon];
  const title = t(`awards.${spec.key}.title`);

  function metric(a: LeaderboardAward): React.ReactNode {
    switch (spec.kind) {
      case "usd":
        return <CostText usd={a.value} />;
      case "duration":
        return formatDuration(t, a.value);
      case "score":
        return formatScore(a.value);
      case "lines":
        return formatCount(a.value);
      case "count":
        return a.value;
    }
  }

  const reference = award ? `${award.repo}#${award.number}` : null;
  return (
    <div role="group" aria-label={title} style={s.card}>
      <div style={s.head}>
        <span style={{ ...s.chip, background: spec.bg, color: spec.color }}>
          <AwardIcon size={16} />
        </span>
        <span style={s.title}>{title}</span>
      </div>
      {award ? (
        <>
          <div style={s.metricRow}>
            <span className="tnum" style={{ ...s.metric, color: spec.color }}>
              {metric(award)}
            </span>
            <span style={s.unit}>{t(`awards.${spec.key}.unit`, { count: award.value })}</span>
          </div>
          {href ? (
            <Link href={href} className="mono" title={reference ?? undefined} style={s.link}>
              {reference}
            </Link>
          ) : (
            <span className="mono" title={reference ?? undefined} style={s.ref}>
              {reference}
            </span>
          )}
          <span title={award.title} style={s.prTitle}>
            {award.title}
          </span>
        </>
      ) : (
        <span style={s.none}>{t("awards.none")}</span>
      )}
    </div>
  );
}

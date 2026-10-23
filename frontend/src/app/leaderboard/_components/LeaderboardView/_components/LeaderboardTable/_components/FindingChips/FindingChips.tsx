/* FindingChips — "C n / W n / S n" counts of a PR; a severity with no findings is muted. The CRITICAL
   chip is outlined red while some of its findings are still open (FR21). The letters carry the
   severity, colour and outline are only a hint (NFR4); the group has one spoken label. */
"use client";

import { useTranslations } from "next-intl";
import type { LeaderboardSeverityCounts } from "@devdigest/shared";
import { s } from "./styles";

export interface FindingChipsProps {
  counts: LeaderboardSeverityCounts;
  /** CRITICAL findings still open; the CRITICAL chip is outlined red when above 0. */
  openCritical?: number;
}

const SEVERITIES = [
  { key: "critical", field: "CRITICAL", color: "var(--crit)" },
  { key: "warning", field: "WARNING", color: "var(--warn)" },
  { key: "suggestion", field: "SUGGESTION", color: "var(--text-secondary)" },
] as const;

export function FindingChips({ counts, openCritical = 0 }: FindingChipsProps) {
  const t = useTranslations("leaderboardTable");
  return (
    <span
      role="img"
      style={s.chips}
      aria-label={t(openCritical > 0 ? "findings.labelOpen" : "findings.label", {
        critical: counts.CRITICAL,
        open: openCritical,
        warning: counts.WARNING,
        suggestion: counts.SUGGESTION,
      })}
    >
      {SEVERITIES.map(({ key, field, color }) => (
        <span key={key} className="tnum" style={s.chip(counts[field] > 0 ? color : null, field === "CRITICAL" && openCritical > 0)}>
          {t(`findings.${key}`, { count: counts[field] })}
        </span>
      ))}
    </span>
  );
}

/* FindingChips — "C n / W n / S n" counts of one review or PR; a severity with no findings is muted.
   The letters carry the severity, colour is only a hint (NFR4); the group has one spoken label. */
"use client";

import { useTranslations } from "next-intl";
import type { LeaderboardSeverityCounts } from "@devdigest/shared";
import { s } from "./styles";

export interface FindingChipsProps {
  counts: LeaderboardSeverityCounts;
}

const SEVERITIES = [
  { key: "critical", field: "CRITICAL", color: "var(--crit)" },
  { key: "warning", field: "WARNING", color: "var(--warn)" },
  { key: "suggestion", field: "SUGGESTION", color: "var(--text-secondary)" },
] as const;

export function FindingChips({ counts }: FindingChipsProps) {
  const t = useTranslations("leaderboardTable");
  return (
    <span
      role="img"
      style={s.chips}
      aria-label={t("findings.label", { critical: counts.CRITICAL, warning: counts.WARNING, suggestion: counts.SUGGESTION })}
    >
      {SEVERITIES.map(({ key, field, color }) => (
        <span key={key} className="tnum" style={s.chip(counts[field] > 0 ? color : null)}>
          {t(`findings.${key}`, { count: counts[field] })}
        </span>
      ))}
    </span>
  );
}

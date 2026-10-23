/** Pure parts of the award cards (specs/2026-10-23-pr-leaderboard.md FR12, FR20). */
import type { LeaderboardAwards, LeaderboardRepo } from "@devdigest/shared";
import type { IconName } from "@devdigest/ui";
import { prDetailPath } from "@/lib/pr-urls";

/** What an award's `value` counts. */
export type AwardKind = "score" | "count" | "duration" | "lines" | "usd";

export interface AwardSpec {
  key: keyof LeaderboardAwards;
  kind: AwardKind;
  icon: IconName;
  /** Colour of the icon chip and the metric. */
  color: string;
  bg: string;
}

/** The six cards in display order. */
export const AWARD_SPECS: readonly AwardSpec[] = [
  { key: "cleanest", kind: "score", icon: "CheckCircle", color: "var(--ok)", bg: "var(--ok-bg)" },
  { key: "hottest", kind: "count", icon: "AlertTriangle", color: "var(--crit)", bg: "var(--crit-bg)" },
  { key: "fastest_merge", kind: "duration", icon: "Zap", color: "var(--accent)", bg: "var(--accent-bg)" },
  { key: "most_rounds", kind: "count", icon: "RefreshCw", color: "var(--warn)", bg: "var(--warn-bg)" },
  { key: "monster", kind: "lines", icon: "Layers", color: "var(--text-secondary)", bg: "var(--info-bg)" },
  { key: "priciest", kind: "usd", icon: "DollarSign", color: "var(--warn)", bg: "var(--warn-bg)" },
];

/** Link to the PR detail page; the award carries the repository name only, so the id comes from the filter's repos. */
export function awardHref(repos: readonly LeaderboardRepo[], repo: string, number: number): string | null {
  const found = repos.find((r) => r.full_name === repo);
  return found ? prDetailPath(encodeURIComponent(found.id), number) : null;
}

/** Whole number with thousands separators ("12,400"). */
export function formatCount(value: number): string {
  return Math.round(value).toLocaleString("en-US");
}

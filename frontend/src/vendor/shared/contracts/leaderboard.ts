import { z } from 'zod';
import { PrStatus } from './platform.js';

/**
 * PR Leaderboard contract (specs/2026-10-23-pr-leaderboard.md §7, D17).
 *
 * GET /leaderboard?period=week|last_week|30d&tz=<IANA>&repo=all|<uuid>
 *
 * Wire rules:
 *  - `period.from/to/previous_from/previous_to` are LOCAL times of `tz` with an
 *    offset and milliseconds (`2026-10-19T00:00:00.000+03:00`), `to` exclusive.
 *    Every other timestamp is UTC with milliseconds (`…Z`).
 *  - `health` and `size` of a row are nullable (no scored review / diff stats
 *    still 0); medians and means are null when there is nothing to average.
 *  - Durations are whole seconds; money is a number rounded to 6 decimals.
 *
 * The backend (backend/internal/leaderboard) changes together with this file.
 */

export const LEADERBOARD_PERIODS = ['week', 'last_week', '30d'] as const;
export const LeaderboardPeriodKey = z.enum(LEADERBOARD_PERIODS);
export type LeaderboardPeriodKey = z.infer<typeof LeaderboardPeriodKey>;

/** The resolved window: local times of `tz` with offset, `to` exclusive. */
export const LeaderboardPeriod = z.object({
  key: LeaderboardPeriodKey,
  tz: z.string(),
  from: z.string(),
  to: z.string(),
  previous_from: z.string(),
  previous_to: z.string(),
});
export type LeaderboardPeriod = z.infer<typeof LeaderboardPeriod>;

/** A repository inside the filter; `last_polled_at` is null until first polled. */
export const LeaderboardRepo = z.object({
  id: z.string(),
  full_name: z.string(),
  last_polled_at: z.string().nullable(),
});
export type LeaderboardRepo = z.infer<typeof LeaderboardRepo>;

/** Window KPIs; the same shape is returned for the previous window. */
export const LeaderboardKpis = z.object({
  prs_reviewed: z.number().int(),
  merged: z.number().int(),
  avg_health: z.number().nullable(),
  time_to_first_review_s: z.number().int().nullable(),
  time_to_merge_s: z.number().int().nullable(),
  critical_found: z.number().int(),
  open_critical: z.number().int(),
  review_cost_usd: z.number(),
});
export type LeaderboardKpis = z.infer<typeof LeaderboardKpis>;

/** Finding counts by severity. */
export const LeaderboardSeverityCounts = z.object({
  CRITICAL: z.number().int(),
  WARNING: z.number().int(),
  SUGGESTION: z.number().int(),
});
export type LeaderboardSeverityCounts = z.infer<typeof LeaderboardSeverityCounts>;

export const LeaderboardSize = z.enum(['S', 'M', 'L', 'XL']);
export type LeaderboardSize = z.infer<typeof LeaderboardSize>;

export const LeaderboardRisk = z.enum(['low', 'medium', 'high']);
export type LeaderboardRisk = z.infer<typeof LeaderboardRisk>;

/** One agent's review in the row's window snapshot (shown when the row expands). */
export const LeaderboardRowReview = z.object({
  review_id: z.string(),
  agent_id: z.string().nullable(),
  /** Stored name; "Deleted agent" when the agent no longer exists. */
  agent_name: z.string(),
  score: z.number().int().nullable(),
  findings: LeaderboardSeverityCounts,
});
export type LeaderboardRowReview = z.infer<typeof LeaderboardRowReview>;

/** One ranked PR of the table. */
export const LeaderboardRow = z.object({
  pr_id: z.string(),
  repo_id: z.string(),
  repo: z.string(),
  number: z.number().int(),
  title: z.string(),
  author: z.string(),
  status: PrStatus,
  draft: z.boolean(),
  additions: z.number().int(),
  deletions: z.number().int(),
  files_count: z.number().int(),
  size: LeaderboardSize.nullable(),
  health: z.number().int().nullable(),
  risk: LeaderboardRisk,
  findings: LeaderboardSeverityCounts,
  open_critical: z.number().int(),
  rounds: z.number().int(),
  time_to_first_review_s: z.number().int().nullable(),
  time_to_merge_s: z.number().int().nullable(),
  open_age_s: z.number().int().nullable(),
  cost_usd: z.number(),
  new_commits_since_review: z.boolean(),
  opened_at: z.string().nullable(),
  merged_at: z.string().nullable(),
  reviews: z.array(LeaderboardRowReview),
});
export type LeaderboardRow = z.infer<typeof LeaderboardRow>;

/** An award winner; `value` is a score, count, seconds, lines or USD by award. */
export const LeaderboardAward = z.object({
  pr_id: z.string(),
  repo: z.string(),
  number: z.number().int(),
  title: z.string(),
  value: z.number(),
});
export type LeaderboardAward = z.infer<typeof LeaderboardAward>;

export const LeaderboardAwards = z.object({
  cleanest: LeaderboardAward.nullable(),
  hottest: LeaderboardAward.nullable(),
  fastest_merge: LeaderboardAward.nullable(),
  most_rounds: LeaderboardAward.nullable(),
  monster: LeaderboardAward.nullable(),
  priciest: LeaderboardAward.nullable(),
});
export type LeaderboardAwards = z.infer<typeof LeaderboardAwards>;

export const LeaderboardAttentionKind = z.enum([
  'merged_open_critical',
  'open_critical',
  'no_review',
  'new_commits',
  'open_long',
]);
export type LeaderboardAttentionKind = z.infer<typeof LeaderboardAttentionKind>;

/** One reason a PR needs attention; `count` / `days` carry the kind's number when it has one. */
export const LeaderboardAttentionReason = z.object({
  kind: LeaderboardAttentionKind,
  count: z.number().int().optional(),
  days: z.number().int().optional(),
});
export type LeaderboardAttentionReason = z.infer<typeof LeaderboardAttentionReason>;

export const LeaderboardAttention = z.object({
  pr_id: z.string(),
  repo: z.string(),
  number: z.number().int(),
  title: z.string(),
  author: z.string(),
  opened_at: z.string().nullable(),
  reasons: z.array(LeaderboardAttentionReason),
});
export type LeaderboardAttention = z.infer<typeof LeaderboardAttention>;

/** `items` holds at most 20 entries; `total` counts them all. */
export const LeaderboardNeedsAttention = z.object({
  total: z.number().int(),
  items: z.array(LeaderboardAttention),
});
export type LeaderboardNeedsAttention = z.infer<typeof LeaderboardNeedsAttention>;

export const LeaderboardHotFile = z.object({
  path: z.string(),
  repo: z.string(),
  findings: z.number().int(),
  critical: z.number().int(),
  prs: z.array(z.number().int()),
});
export type LeaderboardHotFile = z.infer<typeof LeaderboardHotFile>;

/** One of the four size buckets; averages are null for an empty bucket. */
export const LeaderboardSizeBucket = z.object({
  size: LeaderboardSize,
  prs: z.number().int(),
  avg_health: z.number().nullable(),
  avg_findings: z.number().nullable(),
});
export type LeaderboardSizeBucket = z.infer<typeof LeaderboardSizeBucket>;

/** One calendar day of the window in `tz` (`YYYY-MM-DD`). */
export const LeaderboardDay = z.object({
  date: z.string(),
  opened: z.number().int(),
  merged: z.number().int(),
  critical: z.number().int(),
});
export type LeaderboardDay = z.infer<typeof LeaderboardDay>;

/** Response of GET /leaderboard. */
export const Leaderboard = z.object({
  period: LeaderboardPeriod,
  repos: z.array(LeaderboardRepo),
  summary: LeaderboardKpis,
  previous_summary: LeaderboardKpis,
  awards: LeaderboardAwards,
  rows: z.array(LeaderboardRow),
  needs_attention: LeaderboardNeedsAttention,
  hot_files: z.array(LeaderboardHotFile),
  health_by_size: z.array(LeaderboardSizeBucket),
  daily: z.array(LeaderboardDay),
});
export type Leaderboard = z.infer<typeof Leaderboard>;

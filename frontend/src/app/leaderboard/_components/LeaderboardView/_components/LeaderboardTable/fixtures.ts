/* Test fixtures for the leaderboard table folder. */
import type { LeaderboardRow, LeaderboardRowReview } from "@devdigest/shared";

export function review(o: Partial<LeaderboardRowReview> = {}): LeaderboardRowReview {
  return {
    review_id: "rv1",
    agent_id: "ag1",
    agent_name: "Security agent",
    score: 82,
    findings: { CRITICAL: 1, WARNING: 0, SUGGESTION: 2 },
    ...o,
  };
}

export function row(o: Partial<LeaderboardRow> = {}): LeaderboardRow {
  return {
    pr_id: "pr1",
    repo_id: "r1",
    repo: "acme/api",
    number: 12,
    title: "Add rate limiting",
    author: "ivan.petrov",
    status: "merged",
    draft: false,
    additions: 120,
    deletions: 30,
    files_count: 7,
    size: "M",
    health: 90,
    risk: "low",
    findings: { CRITICAL: 0, WARNING: 2, SUGGESTION: 0 },
    open_critical: 0,
    rounds: 2,
    time_to_first_review_s: 2880,
    time_to_merge_s: 6840,
    open_age_s: null,
    cost_usd: 0.0039,
    new_commits_since_review: false,
    opened_at: "2026-10-19T08:00:00.000Z",
    merged_at: "2026-10-19T10:00:00.000Z",
    reviews: [review()],
    ...o,
  };
}

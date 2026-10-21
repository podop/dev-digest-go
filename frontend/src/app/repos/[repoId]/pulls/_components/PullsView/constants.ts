/** Open PRs carry a derived review status; everything else is merged/closed. */
export const OPEN_STATUSES: ReadonlySet<string> = new Set(["needs_review", "reviewed", "stale"]);

/** Status filter applied when ?status= is absent — the most actionable one. */
export const DEFAULT_STATUS = "needs_review";

/** Sort orders for the list: by last update, review risk, finding count or diff size. */
export const SORT_ORDERS = ["newest", "oldest", "risk", "findings", "largest"] as const;
export type PullsSort = (typeof SORT_ORDERS)[number];

/** Per-finding weights behind "Highest risk" — the score penalties of reviewer-core's
 *  scoreFromFindings (src/review/reduce.ts), applied to every agent's latest findings. */
export const RISK_WEIGHTS = { CRITICAL: 35, WARNING: 12, SUGGESTION: 3 } as const;

/** Sort applied when ?sort= is absent or unknown (kept out of the URL). */
export const DEFAULT_SORT: PullsSort = "newest";

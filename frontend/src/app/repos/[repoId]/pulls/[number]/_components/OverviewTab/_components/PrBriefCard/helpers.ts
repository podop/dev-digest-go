import type { ReviewRecord, Verdict } from "@devdigest/shared";
import { VERDICT_RANK } from "./constants";

/** Review state shown in the banner (null/false = nothing to show). */
export interface BannerStats {
  hasReviews: boolean;
  /** Worst verdict over the current reviews (null verdicts ignored). */
  verdict: Verdict | null;
  /** Score of the newest current review. */
  score: number | null;
  findings: number;
  blockers: number;
}

function time(iso: string): number {
  const t = Date.parse(iso);
  return Number.isNaN(t) ? 0 : t;
}

/** A PR's current reviews: the newest `kind='review'` row per agent (client/INSIGHTS.md — never reviews[0]). Newest first. */
export function currentReviews(reviews: readonly ReviewRecord[] | undefined): ReviewRecord[] {
  const newestFirst = (reviews ?? [])
    .filter((r) => r.kind === "review")
    .sort((a, b) => time(b.created_at) - time(a.created_at));
  const seen = new Set<string | null>();
  return newestFirst.filter((r) => {
    if (seen.has(r.agent_id)) return false;
    seen.add(r.agent_id);
    return true;
  });
}

/** Banner numbers over the current review set, counted the way the Findings tab counts them
 *  (every kept finding; blockers = CRITICAL and not dismissed). */
export function bannerStats(reviews: readonly ReviewRecord[] | undefined): BannerStats {
  const current = currentReviews(reviews);
  const newest = current[0];
  if (!newest) return { hasReviews: false, verdict: null, score: null, findings: 0, blockers: 0 };
  let verdict: Verdict | null = null;
  for (const r of current) {
    if (r.verdict && (!verdict || VERDICT_RANK[r.verdict] > VERDICT_RANK[verdict])) verdict = r.verdict;
  }
  return {
    hasReviews: true,
    verdict,
    score: newest.score,
    findings: current.reduce((n, r) => n + r.findings.length, 0),
    blockers: current.reduce((n, r) => n + r.findings.filter((f) => f.severity === "CRITICAL" && !f.dismissed_at).length, 0),
  };
}

/** Stale when the server says so, or the page shows a different head SHA than the brief was generated for. */
export function isBriefStale(serverStale: boolean, briefHeadSha: string, pageHeadSha: string | null): boolean {
  return serverStale || (pageHeadSha != null && pageHeadSha !== briefHeadSha);
}

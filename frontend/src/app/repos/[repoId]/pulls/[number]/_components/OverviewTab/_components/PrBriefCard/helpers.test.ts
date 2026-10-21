import { describe, it, expect } from "vitest";
import type { FindingRecord, ReviewRecord } from "@devdigest/shared";
import { bannerStats, currentReviews, isBriefStale } from "./helpers";

const finding = (severity: FindingRecord["severity"], dismissed = false) =>
  ({ id: Math.random().toString(), severity, dismissed_at: dismissed ? "2026-01-01T00:00:00Z" : null }) as unknown as FindingRecord;

const review = (over: Partial<ReviewRecord>): ReviewRecord =>
  ({ id: "r", agent_id: "a1", kind: "review", verdict: "approve", score: 50, created_at: "2026-02-01T10:00:00Z", findings: [], ...over }) as ReviewRecord;

describe("currentReviews", () => {
  it("keeps the newest kind=review row per agent, newest first, whatever the input order", () => {
    const rows = [
      review({ id: "old-a1", agent_id: "a1", created_at: "2026-02-01T09:00:00Z" }),
      review({ id: "sum", agent_id: "a3", kind: "summary", created_at: "2026-02-03T00:00:00Z" }),
      review({ id: "new-a2", agent_id: "a2", created_at: "2026-02-01T11:00:00Z" }),
      review({ id: "new-a1", agent_id: "a1", created_at: "2026-02-01T10:00:00Z" }),
    ];
    expect(currentReviews(rows).map((r) => r.id)).toEqual(["new-a2", "new-a1"]);
    expect(currentReviews(undefined)).toEqual([]);
  });
});

describe("bannerStats", () => {
  it("takes the worst verdict, the newest score and the summed counts (AC28)", () => {
    const stats = bannerStats([
      review({ agent_id: "a1", verdict: "approve", score: 80, created_at: "2026-02-02T00:00:00Z", findings: [finding("WARNING")] }),
      review({
        agent_id: "a2",
        verdict: "request_changes",
        score: 40,
        created_at: "2026-02-01T00:00:00Z",
        findings: [finding("CRITICAL"), finding("CRITICAL", true), finding("SUGGESTION")],
      }),
    ]);
    expect(stats).toEqual({ hasReviews: true, verdict: "request_changes", score: 80, findings: 4, blockers: 1 });
  });

  it("ignores null verdicts and reports no reviews as empty", () => {
    expect(bannerStats([review({ verdict: null, score: null })]).verdict).toBeNull();
    expect(bannerStats([review({ kind: "summary" })])).toEqual({ hasReviews: false, verdict: null, score: null, findings: 0, blockers: 0 });
  });
});

describe("isBriefStale", () => {
  it("is stale on the server flag or a differing page head SHA, never on an unknown page SHA", () => {
    expect(isBriefStale(true, "a", "a")).toBe(true);
    expect(isBriefStale(false, "a", "b")).toBe(true);
    expect(isBriefStale(false, "a", "a")).toBe(false);
    expect(isBriefStale(false, "a", null)).toBe(false);
  });
});

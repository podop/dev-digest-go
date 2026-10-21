import { describe, it, expect } from "vitest";
import type { FindingRecord, ReviewRecord } from "@devdigest/shared";
import type { PrMeta } from "./constants";
import { currentReviewFindings, shortDate } from "./helpers";

describe("shortDate", () => {
  const now = new Date("2026-09-25T12:00:00Z");

  it("shows month and day without the year for this year", () => {
    const out = shortDate("2026-09-24T12:00:00Z", now);
    expect(out).toMatch(/24/);
    expect(out).not.toMatch(/2026/);
  });

  it("adds the year for another year", () => {
    expect(shortDate("2025-03-10T12:00:00Z", now)).toMatch(/2025/);
  });

  it("shows a dash for a missing or invalid date", () => {
    expect(shortDate(null, now)).toBe("—");
    expect(shortDate(undefined, now)).toBe("—");
    expect(shortDate("not a date", now)).toBe("—");
  });
});

describe("currentReviewFindings", () => {
  const finding = (id: string, severity: FindingRecord["severity"], review_id: string) =>
    ({ id, severity, review_id }) as FindingRecord;
  const review = (id: string, findings: FindingRecord[]) => ({ id, findings }) as ReviewRecord;
  const pr = (fields: Partial<PrMeta>) => ({ id: "p1", ...fields }) as PrMeta;

  const REVIEWS = [
    review("rv-new", [finding("s", "SUGGESTION", "rv-new"), finding("w", "WARNING", "rv-new")]),
    review("rv-other", [finding("c", "CRITICAL", "rv-other")]),
    review("rv-old", [finding("stale", "CRITICAL", "rv-old")]),
  ];

  it("takes the findings of every review in latest_review_ids, most severe first", () => {
    const out = currentReviewFindings(REVIEWS, pr({ latest_review_ids: ["rv-new", "rv-other"], latest_review_id: "rv-new" }));
    expect(out.map((f) => f.id)).toEqual(["c", "w", "s"]);
  });

  it("falls back to latest_review_id when the row has no id list", () => {
    const out = currentReviewFindings(REVIEWS, pr({ latest_review_ids: null, latest_review_id: "rv-new" }));
    expect(out.map((f) => f.id)).toEqual(["w", "s"]);
  });

  it("returns nothing for a PR without a review", () => {
    expect(currentReviewFindings(REVIEWS, pr({ latest_review_ids: null, latest_review_id: null }))).toEqual([]);
  });
});

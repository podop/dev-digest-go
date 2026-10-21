import { describe, it, expect } from "vitest";
import type { PrMeta } from "@/lib/types";
import { countPulls, filterPulls, parsePullsSearch, pullsHref, type PullsSearch } from "./helpers";

function pr(o: Partial<PrMeta>): PrMeta {
  return {
    number: 1,
    title: "PR",
    author: "a",
    branch: "b",
    base: "main",
    head_sha: "x",
    additions: 1,
    deletions: 1,
    files_count: 1,
    status: "needs_review",
    updated_at: "2026-06-01T00:00:00Z",
    score: null,
    ...o,
  };
}

const PULLS = [
  pr({ number: 10, title: "Add rate limiting", status: "needs_review", updated_at: "2026-06-02T00:00:00Z" }),
  pr({ number: 11, title: "Fix login", status: "reviewed", updated_at: "2026-06-03T00:00:00Z" }),
  pr({ number: 12, title: "Rate limit docs", status: "needs_review", updated_at: "2026-06-01T00:00:00Z" }),
  pr({ number: 13, title: "Old merge", status: "merged", updated_at: null }),
];

describe("parsePullsSearch", () => {
  it("defaults to needs_review + newest", () => {
    expect(parsePullsSearch({})).toEqual({ status: "needs_review", sort: "newest" });
  });

  it("keeps explicit values, takes the first of repeated params and rejects unknown sorts", () => {
    expect(parsePullsSearch({ status: ["all", "stale"], sort: "oldest" })).toEqual({ status: "all", sort: "oldest" });
    expect(parsePullsSearch({ status: "reviewed", sort: "random" })).toEqual({ status: "reviewed", sort: "newest" });
    expect(parsePullsSearch({ sort: "risk" })).toEqual({ status: "needs_review", sort: "risk" });
  });
});

describe("pullsHref", () => {
  it("always writes status and omits the default sort", () => {
    expect(pullsHref("r1", { status: "all", sort: "newest" })).toBe("/repos/r1/pulls?status=all");
    expect(pullsHref("r1", { status: "stale", sort: "oldest" })).toBe("/repos/r1/pulls?status=stale&sort=oldest");
  });
});

describe("filterPulls", () => {
  it("filters by status and sorts newest first", () => {
    expect(filterPulls(PULLS, { status: "needs_review", sort: "newest" }, "").map((p) => p.number)).toEqual([10, 12]);
  });

  it("'all' keeps every status; oldest puts missing dates first", () => {
    expect(filterPulls(PULLS, { status: "all", sort: "oldest" }, "").map((p) => p.number)).toEqual([13, 12, 10, 11]);
  });

  it("matches the query against title (case-insensitive) or number", () => {
    expect(filterPulls(PULLS, { status: "all", sort: "newest" }, "  RATE ").map((p) => p.number)).toEqual([10, 12]);
    expect(filterPulls(PULLS, { status: "all", sort: "newest" }, "11").map((p) => p.number)).toEqual([11]);
  });

  describe("risk / findings / largest", () => {
    const counts = (CRITICAL: number, WARNING: number, SUGGESTION: number) => ({ CRITICAL, WARNING, SUGGESTION });
    const MIXED = [
      pr({ number: 1, score: 90, findings_counts: counts(0, 1, 0), additions: 10, deletions: 0 }),
      pr({ number: 2, score: null, findings_counts: null, additions: 500, deletions: 100 }),
      pr({ number: 3, score: 40, findings_counts: counts(2, 0, 0), additions: 50, deletions: 50 }),
      pr({ number: 4, score: 100, findings_counts: counts(0, 1, 5), additions: 1, deletions: 1 }),
      pr({ number: 5, score: 40, findings_counts: counts(1, 1, 0), additions: 100, deletions: 0, updated_at: "2026-06-05T00:00:00Z" }),
    ];
    const order = (sort: PullsSearch["sort"]) => filterPulls(MIXED, { status: "all", sort }, "").map((p) => p.number);

    it("highest risk = severity-weighted findings first (not score), ties newest first, unreviewed last", () => {
      // 3: 2×35=70 · 5: 35+12=47 · 4: 12+5×3=27 · 1: 12 — #1's score 90 vs #4's 75 is ignored on purpose
      expect(order("risk")).toEqual([3, 5, 4, 1, 2]);
    });

    it("most findings = largest total across severities first, unreviewed last", () => {
      expect(order("findings")).toEqual([4, 5, 3, 1, 2]);
    });

    it("largest = most changed lines first, ties newest first", () => {
      expect(order("largest")).toEqual([2, 5, 3, 1, 4]);
    });
  });

  it("does not mutate the input", () => {
    const input = [...PULLS];
    filterPulls(input, { status: "all", sort: "oldest" }, "");
    expect(input).toEqual(PULLS);
  });
});

describe("countPulls", () => {
  it("counts open (derived review statuses), needs-review and merged PRs", () => {
    expect(countPulls(PULLS)).toEqual({ open: 3, needsReview: 2, merged: 1 });
  });
});

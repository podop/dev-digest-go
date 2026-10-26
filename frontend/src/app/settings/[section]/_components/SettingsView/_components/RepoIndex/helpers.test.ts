import { describe, it, expect } from "vitest";
import { indexedAge, isNotIndexed } from "./helpers";

const NOW = Date.parse("2026-10-24T12:00:00Z");
const ago = (ms: number) => new Date(NOW - ms).toISOString();

describe("indexedAge", () => {
  it("picks the coarsest unit and never goes negative", () => {
    expect(indexedAge(ago(20_000), NOW)).toEqual({ unit: "justNow" });
    expect(indexedAge(ago(2 * 60_000), NOW)).toEqual({ unit: "minutes", count: 2 });
    expect(indexedAge(ago(3 * 3_600_000), NOW)).toEqual({ unit: "hours", count: 3 });
    expect(indexedAge(ago(50 * 3_600_000), NOW)).toEqual({ unit: "days", count: 2 });
    expect(indexedAge(ago(-5 * 60_000), NOW)).toEqual({ unit: "justNow" });
  });
});

describe("index rules", () => {
  it("not indexed exactly when lastIndexedSha is empty (A4)", () => {
    expect(isNotIndexed({ lastIndexedSha: "" })).toBe(true);
    expect(isNotIndexed({ lastIndexedSha: "abc123" })).toBe(false);
  });
});

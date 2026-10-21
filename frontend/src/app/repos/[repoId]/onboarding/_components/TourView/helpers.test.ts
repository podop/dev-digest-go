import { describe, it, expect } from "vitest";
import { ApiError } from "@/lib/api";
import { describeGenerateError, firstVisible, readyTour, relativeTime, sectionFromHash, shareUrl } from "./helpers";

const NOW = Date.parse("2026-10-01T12:00:00.000Z");
const ago = (ms: number) => new Date(NOW - ms).toISOString();

describe("relativeTime", () => {
  it("words the age in the largest whole unit", () => {
    expect(relativeTime(ago(10_000), NOW)).toBe("now");
    expect(relativeTime(ago(5 * 60_000), NOW)).toBe("5 minutes ago");
    expect(relativeTime(ago(2 * 3_600_000), NOW)).toBe("2 hours ago");
    expect(relativeTime(ago(36 * 3_600_000), NOW)).toBe("yesterday");
    expect(relativeTime(ago(3 * 86_400_000), NOW)).toBe("3 days ago");
  });

  it("does not claim a future time and tolerates garbage", () => {
    expect(relativeTime(ago(-60_000), NOW)).toBe("now");
    expect(relativeTime("not a date", NOW)).toBe("—");
  });
});

describe("shareUrl / sectionFromHash / firstVisible", () => {
  it("appends the section as a hash only when there is one", () => {
    expect(shareUrl("http://x", "/repos/r1/onboarding", "first-tasks")).toBe("http://x/repos/r1/onboarding#first-tasks");
    expect(shareUrl("http://x", "/repos/r1/onboarding", null)).toBe("http://x/repos/r1/onboarding");
  });

  it("accepts only known section ids from the hash", () => {
    const ids = ["architecture", "first-tasks"] as const;
    expect(sectionFromHash(ids, "#first-tasks")).toBe("first-tasks");
    expect(sectionFromHash(ids, "#nope")).toBeNull();
    expect(sectionFromHash(ids, "")).toBeNull();
  });

  it("picks the first visible id in display order", () => {
    const ids = ["a", "b", "c"] as const;
    expect(firstVisible(ids, new Set(["c", "b"]))).toBe("b");
    expect(firstVisible(ids, new Set())).toBeNull();
  });
});

describe("describeGenerateError", () => {
  it("maps the known codes and falls back for anything else", () => {
    expect(describeGenerateError(new ApiError("x", 409, "generation_in_progress"))).toEqual({ messageKey: "inProgress", code: "generation_in_progress" });
    expect(describeGenerateError(new ApiError("x", 422, "index_not_ready")).messageKey).toBe("indexNotReady");
    expect(describeGenerateError(new ApiError("x", 422, "provider_not_configured")).messageKey).toBe("providerNotConfigured");
    expect(describeGenerateError(new ApiError("x", 502, "generation_failed"))).toEqual({ messageKey: "generationFailed", code: "generation_failed" });
    expect(describeGenerateError(new Error("boom"))).toEqual({ messageKey: "generationFailed", code: undefined });
  });
});

describe("readyTour", () => {
  it("returns the ready state, null for none or no data", () => {
    expect(readyTour(undefined)).toBeNull();
    expect(readyTour({ status: "none" })).toBeNull();
  });
});

import { describe, it, expect } from "vitest";
import type { LeaderboardPeriod } from "@devdigest/shared";
import { leaderboardHref, resolvePeriod, resolveRepoFilter, resolveRepoParam, syncedAgo, windowRanges } from "./helpers";

const REPO = "3f2b8c1e-5d4a-4e6f-9a7b-1c2d3e4f5a6b";

const window = (over: Partial<LeaderboardPeriod>): LeaderboardPeriod => ({
  key: "week",
  tz: "Europe/Kyiv",
  from: "2026-09-28T00:00:00.000+03:00",
  to: "2026-10-05T00:00:00.000+03:00",
  previous_from: "2026-09-21T00:00:00.000+03:00",
  previous_to: "2026-09-28T00:00:00.000+03:00",
  ...over,
});

describe("URL state", () => {
  it("falls back to week / all for garbage and keeps valid values (EC13)", () => {
    expect(resolvePeriod(undefined)).toBe("week");
    expect(resolvePeriod("yesterday")).toBe("week");
    expect(resolvePeriod(["30d", "week"])).toBe("30d");
    expect(resolvePeriod("last_week")).toBe("last_week");

    expect(resolveRepoParam(undefined)).toBe("all");
    expect(resolveRepoParam("not-a-uuid")).toBe("all");
    expect(resolveRepoParam("' OR 1=1 --")).toBe("all");
    expect(resolveRepoParam(REPO)).toBe(REPO);
    expect(resolveRepoParam([REPO, "x"])).toBe(REPO);
  });

  it("checks a repo id against the repo list: unknown → all, not loaded → null (EC14)", () => {
    expect(resolveRepoFilter("all", undefined)).toBe("all");
    expect(resolveRepoFilter(REPO, undefined)).toBeNull();
    expect(resolveRepoFilter(REPO, [{ id: REPO }])).toBe(REPO);
    expect(resolveRepoFilter(REPO, [{ id: "other" }])).toBe("all");
    expect(resolveRepoFilter(REPO, [])).toBe("all");
  });

  it("builds the page URL", () => {
    expect(leaderboardHref("30d", "all")).toBe("/leaderboard?period=30d&repo=all");
    expect(leaderboardHref("week", REPO)).toBe(`/leaderboard?period=week&repo=${REPO}`);
  });
});

describe("windowRanges (FR18)", () => {
  it("formats a week and its predecessor within one month and across months", () => {
    expect(windowRanges(window({}), true)).toEqual({ range: "Sep 28 – Oct 4", previous: "Sep 21 – 27" });
  });

  it("formats a 30-day window, which has no previous window", () => {
    const w = window({
      key: "30d",
      from: "2026-09-05T00:00:00.000+03:00",
      to: "2026-10-05T00:00:00.000+03:00",
      previous_from: null,
      previous_to: null,
    });
    expect(windowRanges(w, true)).toEqual({ range: "Sep 5 – Oct 4", previous: null });
  });

  it("leaves out the previous range when nothing was compared, even if the window has a previous one (FR25)", () => {
    expect(windowRanges(window({}), false)).toEqual({ range: "Sep 28 – Oct 4", previous: null });
  });

  it("uses the local calendar days even when the offset changes inside the window, and shows years across a year", () => {
    const w = window({
      from: "2026-12-28T00:00:00.000+02:00",
      to: "2027-01-04T00:00:00.000+02:00",
      previous_from: "2026-12-21T00:00:00.000+02:00",
      previous_to: "2026-12-28T00:00:00.000+02:00",
    });
    expect(windowRanges(w, true)).toEqual({ range: "Dec 28, 2026 – Jan 3, 2027", previous: "Dec 21 – 27" });
  });

  it("returns null for a time that does not parse", () => {
    expect(windowRanges(window({ from: "garbage" }), true)).toBeNull();
    expect(windowRanges(window({ previous_from: "garbage" }), true)).toBeNull();
  });
});

describe("syncedAgo (FR23)", () => {
  const now = Date.parse("2026-10-22T12:00:00.000Z");

  it("describes the last poll relative to now", () => {
    expect(syncedAgo("2026-10-22T11:55:00.000Z", now)).toBe("5 minutes ago");
    expect(syncedAgo("2026-10-22T09:00:00.000Z", now)).toBe("3 hours ago");
    expect(syncedAgo("2026-10-20T12:00:00.000Z", now)).toBe("2 days ago");
    expect(syncedAgo("2026-10-22T11:59:59.000Z", now)).toBe("now");
    expect(syncedAgo("2026-10-22T12:05:00.000Z", now)).toBe("now");
  });

  it("is null when the repository was never polled or the time is not a date", () => {
    expect(syncedAgo(null, now)).toBeNull();
    expect(syncedAgo("garbage", now)).toBeNull();
  });
});

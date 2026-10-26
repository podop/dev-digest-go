import { describe, it, expect } from "vitest";
import { fixSectionsOf, pollAge, toneOf } from "./helpers";

const NOW = Date.parse("2026-10-25T12:00:00Z");
const ago = (ms: number) => new Date(NOW - ms).toISOString();

describe("pollAge", () => {
  it("is never without a poll, else the coarsest unit, and never negative (A13)", () => {
    expect(pollAge(null, NOW)).toEqual({ unit: "never" });
    expect(pollAge(ago(20_000), NOW)).toEqual({ unit: "justNow" });
    expect(pollAge(ago(2 * 60_000), NOW)).toEqual({ unit: "minutes", count: 2 });
    expect(pollAge(ago(59 * 60_000), NOW)).toEqual({ unit: "minutes", count: 59 });
    expect(pollAge(ago(3 * 3_600_000), NOW)).toEqual({ unit: "hours", count: 3 });
    expect(pollAge(ago(50 * 3_600_000), NOW)).toEqual({ unit: "days", count: 2 });
    expect(pollAge(ago(-5 * 60_000), NOW)).toEqual({ unit: "justNow" });
  });
});

describe("state presentation", () => {
  it("only active is green, off is muted, the paused states warn; three states link to a fix", () => {
    expect(toneOf("active")).toBe("ok");
    expect(toneOf("off")).toBe("muted");
    for (const state of ["poll_disabled", "no_github_token", "no_code_host_token", "no_agents", "no_triggers", "paused_spend_cap"] as const) {
      expect(toneOf(state)).toBe("warn");
    }
    expect(fixSectionsOf("no_github_token")).toEqual(["github"]);
    expect(fixSectionsOf("no_code_host_token")).toEqual(["github", "gitlab"]);
    expect(fixSectionsOf("paused_spend_cap")).toEqual(["api-keys"]);
    expect(fixSectionsOf("active")).toEqual([]);
    expect(fixSectionsOf("no_agents")).toEqual([]);
  });
});

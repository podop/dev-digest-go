import { describe, it, expect } from "vitest";
import { agentEvalsHref, evalHref, formatRunStamp, resolveAgentId, resolvePeriod } from "./helpers";

describe("eval route helpers", () => {
  it("resolves ?agent= (missing, empty, repeated)", () => {
    expect(resolveAgentId(undefined)).toBeNull();
    expect(resolveAgentId("")).toBeNull();
    expect(resolveAgentId("ag1")).toBe("ag1");
    expect(resolveAgentId(["ag1", "ag2"])).toBe("ag1");
  });

  it("resolves ?period= to a preset, anything else to 30d (EC6)", () => {
    expect(resolvePeriod("7d")).toBe("7d");
    expect(resolvePeriod("all")).toBe("all");
    expect(resolvePeriod(["90d", "7d"])).toBe("90d");
    expect(resolvePeriod(undefined)).toBe("30d");
    expect(resolvePeriod("")).toBe("30d");
    expect(resolvePeriod("bogus")).toBe("30d");
    expect(resolvePeriod("14d")).toBe("30d");
  });

  it("builds the dashboard and Evals-tab URLs", () => {
    expect(evalHref()).toBe("/eval");
    expect(evalHref(null, "7d")).toBe("/eval");
    expect(evalHref("ag 1")).toBe("/eval?agent=ag+1");
    expect(evalHref("ag1", "7d")).toBe("/eval?agent=ag1&period=7d");
    expect(agentEvalsHref("ag1")).toBe("/agents/ag1?tab=evals");
  });

  it("formats a run start as local YYYY-MM-DD HH:mm", () => {
    expect(formatRunStamp(new Date(2026, 4, 29, 9, 14).toISOString())).toBe("2026-05-29 09:14");
    expect(formatRunStamp(new Date(2026, 11, 1, 23, 5).toISOString())).toBe("2026-12-01 23:05");
    expect(formatRunStamp("not a date")).toBe("—");
  });
});

import { describe, it, expect } from "vitest";
import { makeRun } from "@/test/eval-fixtures";
import { comparePair, followedRunId, ratioToPoints, runningRun } from "./helpers";

const OLD = makeRun({ id: "old", started_at: "2026-10-01T10:00:00.000Z" });
const NEW = makeRun({ id: "new", started_at: "2026-10-02T10:00:00.000Z" });
const LIVE = makeRun({ id: "live", status: "running", finished_at: null, started_at: "2026-10-03T10:00:00.000Z" });

describe("comparePair", () => {
  it("needs exactly two finished runs and orders them older → newer", () => {
    const runs = [LIVE, NEW, OLD];
    expect(comparePair(runs, ["new", "old"])).toEqual([OLD, NEW]);
    expect(comparePair(runs, ["old"])).toBeNull();
    expect(comparePair(runs, ["old", "new", "live"])).toBeNull();
    expect(comparePair(runs, ["old", "live"])).toBeNull();
    expect(comparePair(undefined, ["old", "new"])).toBeNull();
  });
});

describe("followed / running run", () => {
  it("prefers the running run of the list, then the one just started", () => {
    expect(followedRunId([NEW, LIVE], "started")).toBe("live");
    expect(followedRunId([NEW], "started")).toBe("started");
    expect(followedRunId(undefined, undefined)).toBeUndefined();
  });

  it("takes the freshest data and ignores finished runs", () => {
    expect(runningRun(undefined, [LIVE])?.id).toBe("live");
    expect(runningRun({ run: NEW, results: [] }, [LIVE])).toBeNull();
    expect(runningRun(undefined, [NEW])).toBeNull();
  });
});

describe("ratioToPoints", () => {
  it("rounds to whole points and keeps null", () => {
    expect(ratioToPoints(-0.05)).toBe(-5);
    expect(ratioToPoints(0.0149)).toBe(1);
    expect(ratioToPoints(null)).toBeNull();
  });
});

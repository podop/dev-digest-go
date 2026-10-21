import { describe, it, expect } from "vitest";
import { makeRun } from "@/test/eval-fixtures";
import { casesPassedDelta, deltaPoints, directionOf, metricOf, sparkSeries, toPercent } from "./helpers";

describe("eval metric helpers", () => {
  it("rounds ratios to whole percents and keeps null", () => {
    expect(toPercent(0.755)).toBe(76);
    expect(toPercent(0)).toBe(0);
    expect(toPercent(null)).toBeNull();
  });

  it("deltaPoints is the signed difference of the shown whole percents", () => {
    expect(deltaPoints(0.75, 0.8)).toBe(-5);
    expect(deltaPoints(0.8, 0.75)).toBe(5);
    expect(deltaPoints(0.754, 0.746)).toBe(0); // 75% vs 75%: no arrow on screen
    expect(deltaPoints(null, 0.5)).toBeNull();
    expect(deltaPoints(0.5, null)).toBeNull();
  });

  it("directionOf maps the sign", () => {
    expect([directionOf(3), directionOf(-1), directionOf(0)]).toEqual(["up", "down", "flat"]);
  });

  it("casesPassedDelta needs two runs and a finished latest", () => {
    const prev = makeRun({ cases_passed: 4 });
    expect(casesPassedDelta(makeRun({ cases_passed: 6 }), prev)).toBe(2);
    expect(casesPassedDelta(makeRun({ status: "running", cases_passed: 6 }), prev)).toBeNull();
    expect(casesPassedDelta(makeRun(), null)).toBeNull();
    expect(casesPassedDelta(null, prev)).toBeNull();
  });

  it("metricOf and sparkSeries skip runs without the metric", () => {
    const runs = [makeRun({ recall: 0.5 }), makeRun({ recall: null }), makeRun({ recall: 1 })];
    expect(metricOf(null, "recall")).toBeNull();
    expect(metricOf(runs[0]!, "recall")).toBe(0.5);
    expect(sparkSeries(runs, "recall")).toEqual([0.5, 1]);
  });
});

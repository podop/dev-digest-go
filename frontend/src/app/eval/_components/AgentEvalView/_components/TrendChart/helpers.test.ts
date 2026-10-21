import { describe, it, expect } from "vitest";
import { makeRun } from "@/test/eval-fixtures";
import { axisBottom, axisTicks, columnOf, lineSegments, metricPoints, newestPoint, toPolyline, tooltipOnLeft, xOf, yOf } from "./helpers";

describe("trend chart axis", () => {
  it("bottoms at 0.6 when every value is at or above it, else at the tenth below the lowest, never under 0", () => {
    expect(axisBottom([makeRun({ recall: 0.6, precision: 0.9, citation_accuracy: 1 })])).toBeCloseTo(0.6);
    expect(axisBottom([makeRun({ recall: 0.55 })])).toBeCloseTo(0.5);
    expect(axisBottom([makeRun({ recall: 0.3, precision: 0.9 })])).toBeCloseTo(0.3);
    expect(axisBottom([makeRun({ recall: 0, precision: 0.9 })])).toBe(0);
    expect(axisBottom([makeRun({ recall: null, precision: null, citation_accuracy: null })])).toBeCloseTo(0.6);
    expect(axisBottom([])).toBeCloseTo(0.6);
  });

  it("ticks run from 1.0 down to the bottom every 0.1", () => {
    expect(axisTicks(0.6).map((v) => v.toFixed(1))).toEqual(["1.0", "0.9", "0.8", "0.7", "0.6"]);
    expect(axisTicks(0.5).map((v) => v.toFixed(1))).toEqual(["1.0", "0.9", "0.8", "0.7", "0.6", "0.5"]);
    expect(axisTicks(0)).toHaveLength(11);
  });
});

describe("trend chart geometry", () => {
  it("puts 1.0 at the top and the bottom value at the foot, and spaces runs equally with a lone run in the middle", () => {
    expect(yOf(1, 0.6)).toBeLessThan(yOf(0.6, 0.6));
    expect(yOf(0.8, 0.6)).toBeCloseTo((yOf(1, 0.6) + yOf(0.6, 0.6)) / 2);
    expect(xOf(0, 1)).toBeGreaterThan(xOf(0, 2));
    expect(xOf(1, 3) - xOf(0, 3)).toBeCloseTo(xOf(2, 3) - xOf(1, 3));
  });

  it("leaves a gap for an undefined metric, draws no line for a single point and keeps the newest point", () => {
    const runs = [makeRun({ id: "a", recall: 0.6 }), makeRun({ id: "b", recall: null }), makeRun({ id: "c", recall: 0.75 }), makeRun({ id: "d", recall: 1 })];
    const points = metricPoints(runs, "recall", 0.6);
    expect(points.map((p) => p.run.id)).toEqual(["a", "c", "d"]);
    expect(newestPoint(points)?.run.id).toBe("d");
    expect(newestPoint([])).toBeNull();
    const lines = lineSegments(runs, "recall", 0.6);
    expect(lines).toHaveLength(1);
    expect(lines[0]!.map((p) => p.run.id)).toEqual(["c", "d"]);
    expect(toPolyline(lines[0]!).split(" ")).toHaveLength(2);
  });

  it("hover columns tile the plot and the tooltip flips to the left in the right half", () => {
    const first = columnOf(0, 5);
    const second = columnOf(1, 5);
    expect(first.left + first.width).toBeCloseTo(second.left);
    expect(columnOf(0, 1).width).toBeGreaterThan(0);
    expect([tooltipOnLeft(0, 5), tooltipOnLeft(4, 5)]).toEqual([false, true]);
  });
});

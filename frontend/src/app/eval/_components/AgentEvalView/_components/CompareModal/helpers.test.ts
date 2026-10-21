import { describe, it, expect } from "vitest";
import { makeCompare } from "@/test/eval-fixtures";
import { flippedCount, isConfigUnchanged, isPromptUnchanged, metricChanges } from "./helpers";

describe("compare helpers", () => {
  it("derives signed points from the shown percentages, null when a side is undefined", () => {
    const compare = makeCompare();
    compare.a = { ...compare.a, recall: 0.5, precision: null };
    compare.b = { ...compare.b, recall: 0.75, precision: 0.8 };
    const [recall, precision] = metricChanges(compare, ["recall", "precision"]);
    expect(recall).toMatchObject({ from: 0.5, to: 0.75, points: 25 });
    expect(precision).toMatchObject({ from: null, to: 0.8, points: null });
  });

  it("detects an unchanged prompt / config and counts flips", () => {
    const same = makeCompare({ prompt: [{ op: "same", line: "x" }] });
    expect(isPromptUnchanged(same)).toBe(true);
    expect(isConfigUnchanged(same)).toBe(true);
    const changed = makeCompare({ prompt: [{ op: "del", line: "a" }, { op: "add", line: "b" }] });
    expect(isPromptUnchanged(changed)).toBe(false);
    expect(flippedCount(makeCompare())).toBe(1);
  });
});

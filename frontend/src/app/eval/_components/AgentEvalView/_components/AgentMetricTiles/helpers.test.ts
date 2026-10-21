import { describe, it, expect } from "vitest";
import { makeRun } from "@/test/eval-fixtures";
import { tileDelta } from "./helpers";

describe("tileDelta", () => {
  it("is the difference of the shown percents as a two-decimal ratio with its direction", () => {
    const latest = makeRun({ recall: 0.82, precision: 0.91, citation_accuracy: 0.95 });
    const previous = makeRun({ recall: 0.78, precision: 0.93, citation_accuracy: 0.95 });
    expect(tileDelta(latest, previous, "recall")).toEqual({ direction: "up", amount: "0.04" });
    expect(tileDelta(latest, previous, "precision")).toEqual({ direction: "down", amount: "0.02" });
    expect(tileDelta(latest, previous, "citation_accuracy")).toEqual({ direction: "flat", amount: "0.00" });
  });

  it("is null without a previous run or when a value is undefined", () => {
    expect(tileDelta(makeRun(), null, "recall")).toBeNull();
    expect(tileDelta(makeRun({ recall: null }), makeRun(), "recall")).toBeNull();
  });
});

import { describe, it, expect } from "vitest";
import { recallSeries } from "./helpers";

describe("recallSeries", () => {
  it("keeps defined recalls in order and drops nulls", () => {
    const point = (recall: number | null) => ({ run_id: "r", recall, precision: null, citation_accuracy: null });
    expect(recallSeries([point(0.5), point(null), point(0.75)])).toEqual([0.5, 0.75]);
  });
});

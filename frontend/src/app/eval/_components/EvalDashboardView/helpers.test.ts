import { describe, it, expect } from "vitest";
import { isOverviewEmpty } from "./helpers";

describe("isOverviewEmpty", () => {
  it("is empty only without agents and without runs", () => {
    expect(isOverviewEmpty({ agents: [], recent_runs: [] })).toBe(true);
    expect(
      isOverviewEmpty({ agents: [{ agent_id: "a", name: "A", model: "m", cases_total: 1, latest: null, sparkline: [] }], recent_runs: [] }),
    ).toBe(false);
  });
});

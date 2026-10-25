import { describe, it, expect } from "vitest";
import { DEFAULT_INTERVAL_MIN } from "./constants";
import { intervalOf, triggersOf } from "./helpers";

describe("intervalOf", () => {
  it("returns the stored interval, or the default while none is stored", () => {
    expect(intervalOf({ polling_interval_min: 15 })).toBe(15);
    expect(intervalOf({ polling_interval_min: undefined } as never)).toBe(DEFAULT_INTERVAL_MIN);
  });
});

describe("triggersOf", () => {
  it("picks only the two trigger flags", () => {
    const stored = { enabled: true, agent_ids: ["a"], on_new_pr: false, on_new_commits: true, enabled_at: null };
    expect(triggersOf(stored)).toEqual({ on_new_pr: false, on_new_commits: true });
  });
});

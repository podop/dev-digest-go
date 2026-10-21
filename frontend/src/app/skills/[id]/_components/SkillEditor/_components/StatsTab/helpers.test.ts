import { describe, it, expect } from "vitest";
import { neverAttached } from "./helpers";

describe("StatsTab helpers", () => {
  it("neverAttached is true only for zero runs", () => {
    expect(neverAttached(0)).toBe(true);
    expect(neverAttached(2)).toBe(false);
  });
});

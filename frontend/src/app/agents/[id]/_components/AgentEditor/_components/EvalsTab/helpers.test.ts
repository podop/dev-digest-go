import { describe, it, expect } from "vitest";
import { dashboardHref } from "./helpers";

describe("EvalsTab helpers", () => {
  it("dashboardHref targets the per-agent view", () => {
    expect(dashboardHref("ag 1")).toBe("/eval?agent=ag%201");
  });
});

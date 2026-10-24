import { describe, it, expect } from "vitest";
import { formatBound, formatMoney, monthStartDay, parseCap } from "./helpers";

describe("parseCap", () => {
  it("reads empty input as no cap and strips $ and spaces from amounts", () => {
    expect(parseCap("")).toEqual({ kind: "clear" });
    expect(parseCap("  $ ")).toEqual({ kind: "clear" });
    expect(parseCap("$50")).toEqual({ kind: "value", value: 50 });
    expect(parseCap(" 50.5 ")).toEqual({ kind: "value", value: 50.5 });
    expect(parseCap("$ 1 000")).toEqual({ kind: "value", value: 1000 });
    expect(parseCap("0")).toEqual({ kind: "value", value: 0 });
    expect(parseCap(".5")).toEqual({ kind: "value", value: 0.5 });
    expect(parseCap("50.")).toEqual({ kind: "value", value: 50 });
    expect(parseCap("1000000")).toEqual({ kind: "value", value: 1_000_000 });
  });

  it("rejects non-numbers, three decimals and out-of-range amounts", () => {
    for (const raw of ["abc", "1e3", "5,0", ".", "5-"]) {
      expect(parseCap(raw), raw).toEqual({ kind: "error", error: "notNumber" });
    }
    expect(parseCap("50.123")).toEqual({ kind: "error", error: "decimals" });
    expect(parseCap("-5")).toEqual({ kind: "error", error: "range" });
    expect(parseCap("1000000.01")).toEqual({ kind: "error", error: "range" });
    expect(parseCap("1000001")).toEqual({ kind: "error", error: "range" });
  });
});

describe("formatMoney", () => {
  it("shows two decimals, half up", () => {
    expect(formatMoney(18.4)).toBe("$18.40");
    expect(formatMoney(0)).toBe("$0.00");
    expect(formatMoney(50.12)).toBe("$50.12");
    expect(formatMoney(12.345678)).toBe("$12.35");
    expect(formatMoney(1.005)).toBe("$1.01");
    expect(formatMoney(0.125)).toBe("$0.13");
    expect(formatMoney(0.004999)).toBe("$0.00");
  });

  it("formats bounds and month start days", () => {
    expect(formatBound(1_000_000)).toBe("$1,000,000");
    expect(monthStartDay("2026-10-01T00:00:00.000Z")).toBe("2026-10-01");
  });
});

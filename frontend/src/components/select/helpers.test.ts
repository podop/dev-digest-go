import { describe, it, expect } from "vitest";
import { moveActive, normalizeOptions, placePopup, typeaheadIndex } from "./helpers";

describe("normalizeOptions", () => {
  it("expands bare strings and keeps {value,label} pairs as-is", () => {
    expect(normalizeOptions(["openai", { value: "anthropic", label: "Anthropic" }])).toEqual([
      { value: "openai", label: "openai" },
      { value: "anthropic", label: "Anthropic" },
    ]);
  });

  it("returns an empty array for an empty list", () => {
    expect(normalizeOptions([])).toEqual([]);
  });
});

describe("moveActive", () => {
  it("clamps ArrowDown/ArrowUp at the ends without wrapping", () => {
    expect(moveActive("ArrowDown", 2, 3)).toBe(2);
    expect(moveActive("ArrowDown", 0, 3)).toBe(1);
    expect(moveActive("ArrowUp", 0, 3)).toBe(0);
    expect(moveActive("ArrowUp", 2, 3)).toBe(1);
  });

  it("Home/End jump to the first/last index", () => {
    expect(moveActive("Home", 2, 5)).toBe(0);
    expect(moveActive("End", 0, 5)).toBe(4);
  });

  it("returns -1 for an empty list", () => {
    expect(moveActive("ArrowDown", 0, 0)).toBe(-1);
    expect(moveActive("Home", -1, 0)).toBe(-1);
  });
});

describe("typeaheadIndex", () => {
  const labels = ["openai", "anthropic", "openrouter"];

  it("matches case-insensitively, forward from from+1", () => {
    expect(typeaheadIndex(labels, "an", -1)).toBe(1);
    expect(typeaheadIndex(labels, "AN", -1)).toBe(1);
  });

  it("wraps around the whole list", () => {
    // from=1 (anthropic): searching "open" must wrap past the end back to index 0.
    expect(typeaheadIndex(labels, "open", 1)).toBe(2);
    expect(typeaheadIndex(labels, "open", 2)).toBe(0);
  });

  it("returns -1 for an empty buffer, empty list, or no match", () => {
    expect(typeaheadIndex(labels, "", 0)).toBe(-1);
    expect(typeaheadIndex([], "a", 0)).toBe(-1);
    expect(typeaheadIndex(labels, "zzz", 0)).toBe(-1);
  });
});

describe("placePopup", () => {
  const rect = { left: 10, width: 200, top: 500, bottom: 540 };

  it("places below, gap away from the trigger, when there is enough room", () => {
    const pos = placePopup(rect, 800, 120);
    expect(pos).toEqual({ left: 10, width: 200, top: 546, maxHeight: 254 });
  });

  it("flips above when there is not enough room below but there is above", () => {
    // Only 6px below at this viewport height, 494px above.
    const pos = placePopup(rect, 552, 300);
    expect(pos.top).toBeUndefined();
    expect(pos.bottom).toBe(58);
    expect(pos.left).toBe(10);
    expect(pos.width).toBe(200);
    expect(pos.maxHeight).toBe(280);
  });

  it("clamps maxHeight to the max param even with plenty of room", () => {
    const pos = placePopup(rect, 2000, 1000, 6, 280);
    expect(pos.maxHeight).toBe(280);
  });
});

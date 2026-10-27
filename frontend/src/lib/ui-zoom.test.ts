import { afterEach, describe, expect, it, vi } from "vitest";
import { cssRect, cssViewport, rootZoom } from "./ui-zoom";

afterEach(() => {
  vi.restoreAllMocks();
});

function box(el: HTMLElement, r: { left: number; top: number; width: number; height: number }) {
  el.getBoundingClientRect = () =>
    ({ ...r, right: r.left + r.width, bottom: r.top + r.height, x: r.left, y: r.top, toJSON: () => r }) as DOMRect;
}

describe("ui-zoom", () => {
  it("is 1 without a root zoom, so rects pass through", () => {
    const el = document.createElement("div");
    box(el, { left: 100, top: 40, width: 200, height: 20 });
    expect(rootZoom()).toBe(1);
    expect(cssRect(el)).toEqual({ top: 40, bottom: 60, left: 100, right: 300, width: 200, height: 20 });
  });

  it("divides screen px by the root zoom", () => {
    // jsdom does not compute `zoom`; stand in for the browser's computed value.
    vi.spyOn(window, "getComputedStyle").mockReturnValue({ zoom: "0.5" } as CSSStyleDeclaration);
    const el = document.createElement("div");
    box(el, { left: 100, top: 40, width: 200, height: 20 });
    expect(rootZoom()).toBe(0.5);
    expect(cssRect(el)).toEqual({ top: 80, bottom: 120, left: 200, right: 600, width: 400, height: 40 });
    expect(cssViewport()).toEqual({ width: window.innerWidth * 2, height: window.innerHeight * 2 });
  });
});

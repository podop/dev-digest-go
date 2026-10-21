import { describe, it, expect } from "vitest";
import { visibleBody } from "./comments";

describe("visibleBody", () => {
  it("hides HTML comments like GitHub does, keeping the markdown", () => {
    expect(visibleBody("**bug**\n\nwhy\n\n<!-- devdigest-finding:f1 -->")).toBe("**bug**\n\nwhy");
    expect(visibleBody("a <!-- multi\nline --> b")).toBe("a  b");
    expect(visibleBody("plain")).toBe("plain");
    expect(visibleBody("a<!--x--><!--y-->b")).toBe("ab");
  });

  it("keeps an unclosed <!-- as text", () => {
    expect(visibleBody("a <!-- b")).toBe("a <!-- b");
    expect(visibleBody("<!--x--> a <!-- b")).toBe("a <!-- b");
  });

  it("stays linear on many unclosed <!-- (the lazy regex was quadratic)", () => {
    const body = "<!--".repeat(50_000);
    const start = performance.now();
    expect(visibleBody(body)).toBe(body);
    expect(performance.now() - start).toBeLessThan(500);
  });
});

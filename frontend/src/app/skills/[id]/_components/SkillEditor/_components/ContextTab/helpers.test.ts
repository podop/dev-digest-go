import { describe, it, expect } from "vitest";
import { serializesAs } from "./helpers";

describe("serializesAs", () => {
  it("is the heading plus one '- path' line per attached document, in order", () => {
    expect(serializesAs(["specs/a.md", "docs/b.md"])).toBe("## Project context\n- specs/a.md\n- docs/b.md");
    expect(serializesAs([])).toBe("## Project context");
  });
});

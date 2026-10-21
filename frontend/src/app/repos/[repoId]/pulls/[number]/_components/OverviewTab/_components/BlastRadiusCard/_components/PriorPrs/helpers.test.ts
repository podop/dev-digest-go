import { describe, it, expect } from "vitest";
import { fileBasename } from "./helpers";

describe("fileBasename", () => {
  it("keeps the last path segment", () => {
    expect(fileBasename("client/src/app/ConfigTab.tsx")).toBe("ConfigTab.tsx");
    expect(fileBasename("INSIGHTS.md")).toBe("INSIGHTS.md");
  });
});

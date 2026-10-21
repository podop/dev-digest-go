import { describe, it, expect } from "vitest";
import { parseFileRef } from "./helpers";

describe("parseFileRef", () => {
  it("parses path:start and path:start-end, keeping colons in the path", () => {
    expect(parseFileRef("src/b.ts:40-52")).toEqual({ file: "src/b.ts", start_line: 40, end_line: 52 });
    expect(parseFileRef("src/a.ts:12")).toEqual({ file: "src/a.ts", start_line: 12, end_line: null });
    expect(parseFileRef("c:/x/y.ts:7")).toEqual({ file: "c:/x/y.ts", start_line: 7, end_line: null });
  });

  it("rejects refs without a positive line", () => {
    expect(parseFileRef("src/a.ts")).toBeNull();
    expect(parseFileRef("src/a.ts:0")).toBeNull();
    expect(parseFileRef("src/a.ts:x")).toBeNull();
  });
});

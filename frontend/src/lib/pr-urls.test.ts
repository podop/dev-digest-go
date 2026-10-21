import { describe, expect, it } from "vitest";
import { parseDiffFocus, prDetailPath, prDiffHref } from "./pr-urls";

describe("prDiffHref", () => {
  it("links a line range to the Files changed tab", () => {
    expect(prDiffHref("r1", 7, { file: "src/a b.ts", start_line: 95, end_line: 100 })).toBe(
      "/repos/r1/pulls/7?tab=diff&file=src%2Fa+b.ts&line=95-100",
    );
  });

  it("uses a single line when the range is one line (or missing)", () => {
    expect(prDiffHref("r1", "7", { file: "a.ts", start_line: 12, end_line: 12 })).toBe(
      "/repos/r1/pulls/7?tab=diff&file=a.ts&line=12",
    );
    expect(prDiffHref("r1", "7", { file: "a.ts", start_line: 12 })).toBe("/repos/r1/pulls/7?tab=diff&file=a.ts&line=12");
  });
});

describe("parseDiffFocus", () => {
  it("round-trips what prDiffHref writes", () => {
    const sp = new URL(prDiffHref("r1", 7, { file: "src/a b.ts", start_line: 95, end_line: 100 }), "http://x").searchParams;
    expect(parseDiffFocus(sp.get("file"), sp.get("line"))).toEqual({ path: "src/a b.ts", start: 95, end: 100 });
  });

  it("reads a single line", () => {
    expect(parseDiffFocus("a.ts", "12")).toEqual({ path: "a.ts", start: 12, end: 12 });
  });

  it("rejects missing or malformed values", () => {
    expect(parseDiffFocus(null, "12")).toBeNull();
    expect(parseDiffFocus("a.ts", null)).toBeNull();
    expect(parseDiffFocus("a.ts", "L12")).toBeNull();
    expect(parseDiffFocus("a.ts", "0")).toBeNull();
  });

  it("never yields an end before the start", () => {
    expect(parseDiffFocus("a.ts", "20-10")).toEqual({ path: "a.ts", start: 20, end: 20 });
  });
});

describe("prDetailPath", () => {
  it("builds the PR detail route", () => {
    expect(prDetailPath("r1", 7)).toBe("/repos/r1/pulls/7");
  });
});

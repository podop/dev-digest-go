import { describe, expect, it } from "vitest";
import { applyMaskedEdit, maskKey } from "./helpers";

const DOT = "•";

describe("maskKey", () => {
  it.each([
    ["", ""],
    ["abc", "abc"],
    ["abcd", "abcd"],
    ["abcde", `${DOT}bcde`],
    ["sk-or-v1-abcdefgh1234", `${DOT.repeat(17)}1234`],
    ["x".repeat(32) + "1234", `${DOT.repeat(28)}1234`],
    ["x".repeat(100) + "1234", `${DOT.repeat(28)}1234`],
  ])("masks %j", (key, want) => {
    expect(maskKey(key)).toBe(want);
  });
});

describe("applyMaskedEdit", () => {
  it("appends typed characters and drops deleted ones at the end", () => {
    expect(applyMaskedEdit("", "a")).toBe("a");
    expect(applyMaskedEdit("abcdefgh", `${DOT.repeat(4)}efghi`)).toBe("abcdefghi");
    expect(applyMaskedEdit("abcdefgh", `${DOT.repeat(4)}efg`)).toBe("abcdefg");
  });

  it("replaces everything when the whole field is overwritten", () => {
    expect(applyMaskedEdit("abcdefgh", "new-secret-9876")).toBe("new-secret-9876");
    expect(applyMaskedEdit("abcdefgh", "")).toBe("");
  });

  it("keeps the hidden middle of a long key when the visible tail is edited", () => {
    const raw = "a".repeat(40) + "wxyz";
    expect(applyMaskedEdit(raw, `${maskKey(raw)}!`)).toBe(`${raw}!`);
  });

  it("returns the key unchanged for an identical display", () => {
    expect(applyMaskedEdit("abcdefgh", maskKey("abcdefgh"))).toBe("abcdefgh");
  });
});

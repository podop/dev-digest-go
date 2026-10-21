import { describe, it, expect } from "vitest";
import { ApiError } from "@/lib/api";
import { isSaveShortcut, saveProblem } from "./helpers";

describe("saveProblem", () => {
  it("maps stale_version and doc_not_found to a banner; anything else to null", () => {
    expect(saveProblem(new ApiError("stale", 409, "stale_version"))).toBe("stale");
    expect(saveProblem(new ApiError("gone", 404, "doc_not_found"))).toBe("gone");
    expect(saveProblem(new ApiError("boom", 500, "internal"))).toBeNull();
    expect(saveProblem(new Error("network"))).toBeNull();
  });
});

describe("isSaveShortcut", () => {
  const key = (k: string, mods: Partial<{ ctrlKey: boolean; metaKey: boolean; altKey: boolean }> = {}) => ({
    key: k,
    ctrlKey: false,
    metaKey: false,
    altKey: false,
    ...mods,
  });
  it("accepts Ctrl+S and Cmd+S in either case, nothing else", () => {
    expect(isSaveShortcut(key("s", { ctrlKey: true }))).toBe(true);
    expect(isSaveShortcut(key("S", { metaKey: true }))).toBe(true);
    expect(isSaveShortcut(key("s"))).toBe(false);
    expect(isSaveShortcut(key("a", { ctrlKey: true }))).toBe(false);
    expect(isSaveShortcut(key("s", { ctrlKey: true, altKey: true }))).toBe(false);
  });
});

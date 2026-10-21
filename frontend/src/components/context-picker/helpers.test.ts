import { describe, it, expect } from "vitest";
import type { ContextDoc } from "@devdigest/shared";
import { buildRows, filterRows, moveAttached, splitPath, toggleAttached } from "./helpers";

const doc = (path: string): ContextDoc => ({
  path,
  name: path.split("/").pop() ?? path,
  doc_type: "docs",
  size_bytes: 10,
  tokens: 3,
  updated_at: "2026-10-01T00:00:00.000Z",
  used_by: 0,
  source: "repo",
  editable: false,
});
const DOCS = [doc("docs/a.md"), doc("docs/b.md"), doc("specs/c.md")];

describe("context-picker helpers", () => {
  it("lists attached rows first in attach order, keeps unlisted attached paths as missing, then the rest", () => {
    const rows = buildRows(DOCS, ["specs/c.md", "gone/x.md"]);
    expect(rows.map((r) => [r.path, r.attached, r.missing])).toEqual([
      ["specs/c.md", true, false],
      ["gone/x.md", true, true],
      ["docs/a.md", false, false],
      ["docs/b.md", false, false],
    ]);
  });

  it("filters by path substring, case-insensitively", () => {
    const rows = buildRows(DOCS, []);
    expect(filterRows(rows, " SPECS ").map((r) => r.path)).toEqual(["specs/c.md"]);
    expect(filterRows(rows, "")).toHaveLength(3);
  });

  it("toggles a path (new goes last) and moves one onto another", () => {
    expect(toggleAttached(["a", "b"], "c", true)).toEqual(["a", "b", "c"]);
    expect(toggleAttached(["a", "b"], "a", false)).toEqual(["b"]);
    expect(moveAttached(["a", "b", "c"], "a", "c")).toEqual(["b", "c", "a"]);
    expect(moveAttached(["a", "b"], "a", "a")).toBeNull();
    expect(moveAttached(["a", "b"], "a", null)).toBeNull();
    expect(moveAttached(["a", "b"], "a", "zzz")).toBeNull();
  });

  it("splits a path into file name and folder", () => {
    expect(splitPath("docs/api/a.md")).toEqual({ name: "a.md", folder: "docs/api/" });
    expect(splitPath("README.md")).toEqual({ name: "README.md", folder: "" });
  });
});

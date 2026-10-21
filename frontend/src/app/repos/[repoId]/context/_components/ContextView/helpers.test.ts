import { describe, it, expect } from "vitest";
import type { ContextDoc } from "@devdigest/shared";
import {
  checkStorePath,
  classifyUpload,
  contextHref,
  filterDocs,
  newFolderPath,
  parseContextSearch,
  renameTarget,
  splitBySource,
  splitPath,
  storeRelative,
} from "./helpers";

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

describe("context helpers", () => {
  it("parses ?doc= and builds the screen URL back", () => {
    expect(parseContextSearch({})).toEqual({ doc: null, mode: "preview" });
    expect(parseContextSearch({ doc: "" })).toEqual({ doc: null, mode: "preview" });
    expect(parseContextSearch({ doc: ["docs/a.md", "docs/b.md"] })).toEqual({ doc: "docs/a.md", mode: "preview" });
    expect(parseContextSearch({ doc: "a.md", mode: "edit" })).toEqual({ doc: "a.md", mode: "edit" });
    expect(parseContextSearch({ doc: "a.md", mode: "nope" })).toEqual({ doc: "a.md", mode: "preview" });
    expect(contextHref("r1", "a.md", "edit")).toBe("/repos/r1/context?doc=a.md&mode=edit");
    expect(contextHref("r1", null, "edit")).toBe("/repos/r1/context");
    expect(contextHref("r 1", null)).toBe("/repos/r%201/context");
    expect(contextHref("r1", "docs/a b.md")).toBe("/repos/r1/context?doc=docs%2Fa+b.md");
  });

  it("splits a path into file name and folder", () => {
    expect(splitPath("docs/api/users.md")).toEqual({ name: "users.md", folder: "docs/api/" });
    expect(splitPath("README.md")).toEqual({ name: "README.md", folder: "" });
  });

  it("filters by a case-insensitive path substring and keeps the order", () => {
    const docs = [doc("specs/Auth.md"), doc("docs/api.md"), doc("insights/auth-notes.md")];
    expect(filterDocs(docs, " AUTH ").map((d) => d.path)).toEqual(["specs/Auth.md", "insights/auth-notes.md"]);
    expect(filterDocs(docs, "")).toHaveLength(3);
    expect(filterDocs(docs, "zzz")).toEqual([]);
  });
});

describe("store path helpers", () => {
  const R = ".devdigest/specs/";

  it("checks the store path shape like the server (root, .md, depth 5, segment characters)", () => {
    expect(checkStorePath(`${R}a.md`)).toBe(true);
    expect(checkStorePath(`${R}a/b/c/d/e/f.md`)).toBe(true); // 5 folders
    expect(checkStorePath(`${R}a/b/c/d/e/g/f.md`)).toBe(false); // 6 folders
    for (const bad of [`${R}a.txt`, `${R}../a.md`, `${R}a b.md`, `${R}é.md`, `${R}a\\b.md`, `${R}/a.md`, `${R}a//b.md`, "specs/a.md", `${R}${"x".repeat(520)}.md`]) {
      expect(checkStorePath(bad), bad).toBe(false);
    }
  });

  it("maps the inline rename text and the folder name to full store paths, or null when invalid", () => {
    expect(storeRelative(`${R}docs/a.md`)).toBe("docs/a.md");
    expect(renameTarget(" docs/b.md ")).toBe(`${R}docs/b.md`);
    expect(renameTarget("b.txt")).toBeNull();
    expect(renameTarget("")).toBeNull();
    expect(newFolderPath("docs-x/")).toBe(`${R}docs-x/untitled.md`);
    expect(newFolderPath("a/b")).toBe(`${R}a/b/untitled.md`);
    expect(newFolderPath("docs x")).toBeNull();
    expect(newFolderPath("  ")).toBeNull();
  });

  it("splits the list by source, keeping the order", () => {
    const store = { ...doc(`${R}s.md`), source: "store" as const, editable: true };
    const { store: st, repo } = splitBySource([doc("docs/a.md"), store, doc("docs/b.md")]);
    expect(st.map((d) => d.path)).toEqual([`${R}s.md`]);
    expect(repo.map((d) => d.path)).toEqual(["docs/a.md", "docs/b.md"]);
  });
});

describe("classifyUpload", () => {
  const file = (name: string, bytes: Uint8Array | number) => {
    const data = typeof bytes === "number" ? new Uint8Array(bytes) : bytes;
    return { name, size: data.byteLength, arrayBuffer: async () => data.buffer.slice(data.byteOffset, data.byteOffset + data.byteLength) as ArrayBuffer };
  };
  const text = (s: string) => new TextEncoder().encode(s);

  it("accepts UTF-8 markdown and names the reason for everything else", async () => {
    expect(await classifyUpload(file("a.md", text("# héllo")))).toEqual({ ok: true, content: "# héllo" });
    expect(await classifyUpload(file("b.txt", text("x")))).toEqual({ ok: false, reason: "not_md" });
    expect(await classifyUpload(file("my notes.md", text("x")))).toEqual({ ok: false, reason: "bad_name" });
    expect(await classifyUpload(file("c.md", 300 * 1024))).toEqual({ ok: false, reason: "too_large" });
    expect(await classifyUpload(file("d.md", new Uint8Array([0xff, 0xfe, 0x41])))).toEqual({ ok: false, reason: "not_utf8" });
    expect(await classifyUpload(file("e.md", text("a\0b")))).toEqual({ ok: false, reason: "has_nul" });
  });

  it("rejects an oversized file by its reported size without reading it", async () => {
    let read = false;
    const big = {
      name: "big.md",
      size: 999_999,
      arrayBuffer: async () => {
        read = true;
        return new ArrayBuffer(0);
      },
    };
    expect(await classifyUpload(big)).toEqual({ ok: false, reason: "too_large" });
    expect(read).toBe(false);
  });
});

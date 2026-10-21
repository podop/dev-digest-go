import { describe, it, expect } from "vitest";
import { makeCase } from "@/test/eval-fixtures";
import { ApiError } from "@/lib/api";
import {
  baseValues,
  buildCreateInput,
  buildPatch,
  formatCount,
  formatRanges,
  isDirty,
  normalizeCaseDiff,
  previewHighlight,
  previewStatus,
  saveError,
  splitPastedDiff,
  validateCase,
  type EditorValues,
} from "./helpers";

const HUNK = "@@ -10,3 +10,4 @@\n ctx\n+added\n+second\n ctx2\n";
const TWO_FILES = ["diff --git a/src/a.ts b/src/a.ts", "--- a/src/a.ts", "+++ b/src/a.ts", "@@ -1,1 +1,2 @@", " x", "+y", "diff --git a/src/b.ts b/src/b.ts", "--- a/src/b.ts", "+++ b/src/b.ts", "@@ -5,1 +5,2 @@", " p", "+q", ""].join("\n");

function form(over: Partial<EditorValues> = {}): EditorValues {
  return { ...baseValues(null), name: "n", file: "src/a.ts", startLine: "11", endLine: "12", diff: HUNK, ...over };
}

describe("splitPastedDiff", () => {
  it("wraps a header-less hunk for the typed file and lists the new-side runs", () => {
    const a = splitPastedDiff(HUNK, "src/a.ts");
    expect(a.hasHeaders).toBe(false);
    expect(a.hasHunk).toBe(true);
    expect(a.paths).toEqual(["src/a.ts"]);
    expect(a.files[0]).toMatchObject({ path: "src/a.ts", additions: 2, deletions: 0 });
    // server parser: the trailing empty line after the final newline counts as a context line
    expect(a.ranges["src/a.ts"]).toEqual([{ start: 10, end: 14 }]);
    expect(a.files[0]?.patch).toBe("@@ -10,3 +10,4 @@\n ctx\n+added\n+second\n ctx2");
  });

  it("knows a hunk exists before a file is typed, and offers every file of a headed diff", () => {
    const noFile = splitPastedDiff(HUNK, "");
    expect(noFile).toMatchObject({ hasHunk: true, files: [], hasHeaders: false });
    const two = splitPastedDiff(TWO_FILES, "");
    expect(two.hasHeaders).toBe(true);
    expect(two.paths).toEqual(["src/a.ts", "src/b.ts"]);
    expect(two.ranges["src/b.ts"]).toEqual([{ start: 5, end: 7 }]);
  });

  it("finds no hunk in plain text and is empty for blank or oversized input", () => {
    expect(splitPastedDiff("just some words", "src/a.ts").hasHunk).toBe(false);
    expect(splitPastedDiff("   ", "src/a.ts").files).toEqual([]);
    expect(splitPastedDiff("x".repeat(200_001), "src/a.ts").hasHunk).toBe(false);
  });

  it("converts CRLF and leaves a headed diff as given", () => {
    expect(normalizeCaseDiff("a\r\nb", "f.ts")).toBe("diff --git a/f.ts b/f.ts\n--- a/f.ts\n+++ b/f.ts\na\nb");
    expect(normalizeCaseDiff(TWO_FILES, "x")).toBe(TWO_FILES);
  });
});

describe("validateCase", () => {
  const check = (v: EditorValues) => validateCase(v, splitPastedDiff(v.diff, v.file));

  it("accepts a case whose lines intersect the diff", () => {
    expect(check(form()).ok).toBe(true);
  });

  it("flags a missing hunk, lines outside, an inverted range and a too long diff", () => {
    expect(check(form({ diff: "plain" })).noHunk).toBe(true);
    const outside = check(form({ startLine: "40", endLine: "41" }));
    expect(outside.linesOutside).toEqual([{ start: 10, end: 14 }]);
    expect(outside.ok).toBe(false);
    expect(check(form({ startLine: "12", endLine: "11" })).rangeInverted).toBe(true);
    const long = check(form({ diff: "x".repeat(200_001) }));
    expect(long.diffTooLong).toBe(true);
    expect(long.ok).toBe(false);
  });

  it("flags a file that is not among the headed diff's files, and a blank name", () => {
    expect(check(form({ diff: TWO_FILES, file: "src/c.ts" })).fileNotInDiff).toBe(true);
    expect(check(form({ diff: TWO_FILES, file: "src/b.ts", startLine: "5", endLine: "5" })).ok).toBe(true);
    expect(check(form({ name: "   " })).nameOk).toBe(false);
  });

  it("is not ok while a required field is empty", () => {
    expect(check(form({ file: "" })).ok).toBe(false);
    expect(check(form({ startLine: "" })).linesMissing).toBe(true);
    expect(check(form({ startLine: "1.5" })).linesInvalid).toBe(true);
  });
});

describe("previewStatus / previewHighlight", () => {
  it("names why the preview is blank, and the range to light up", () => {
    const v = (over: Partial<EditorValues>) => form(over);
    const status = (x: EditorValues) => previewStatus(validateCase(x, splitPastedDiff(x.diff, x.file)), splitPastedDiff(x.diff, x.file));
    expect(status(v({ diff: "" }))).toBe("empty");
    expect(status(v({ diff: "words" }))).toBe("noHunk");
    expect(status(v({ file: "" }))).toBe("enterFile");
    expect(status(v({}))).toBe("ready");
    expect(previewHighlight(v({}))).toEqual({ path: "src/a.ts", start: 11, end: 12 });
    expect(previewHighlight(v({ endLine: "3" }))).toBeNull();
  });
});

describe("request bodies", () => {
  it("builds a create body with trimmed text, null for blanks and the raw diff", () => {
    const body = buildCreateInput(form({ name: " leak ", notes: " ", severity: "WARNING", title: " t ", prTitle: "", prDescription: "d" }));
    expect(body).toEqual({
      name: "leak",
      notes: null,
      expectation: { kind: "must_find", file: "src/a.ts", start_line: 11, end_line: 12, severity: "WARNING", category: null, title: "t" },
      input_diff: HUNK,
      input_meta: { pr_title: null, pr_description: "d" },
    });
  });

  it("builds a patch with the changed fields only, and null for none", () => {
    const stored = makeCase();
    const base = baseValues(stored);
    expect(buildPatch(base, { ...base })).toBeNull();
    expect(isDirty(base, base)).toBe(false);
    const patch = buildPatch(base, { ...base, kind: "must_not_flag", endLine: "13", category: "", diff: "x" });
    expect(patch).toEqual({ kind: "must_not_flag", start_line: 10, end_line: 13, category: null, input_diff: "x" });
  });

  it("reads a stored lower-case severity as the contract's value without sending it back", () => {
    const base = baseValues(makeCase());
    expect(base.severity).toBe("CRITICAL");
    expect(buildPatch(base, { ...base, name: "new" })).toEqual({ name: "new" });
  });
});

describe("saveError / formatting", () => {
  it("maps server codes to the field they belong to", () => {
    const err = (code: string, details?: unknown, status = 422) => new ApiError("m", status, code, details);
    expect(saveError(err("expectation_lines_not_in_diff", { ranges: [{ start: 1, end: 3 }, "junk"] }))).toEqual({ field: "lines", key: "linesOutside", ranges: [{ start: 1, end: 3 }] });
    expect(saveError(err("expectation_file_not_in_diff", { files: ["a", 5] }))).toEqual({ field: "file", key: "fileNotInDiff", files: ["a"] });
    expect(saveError(err("diff_invalid")).field).toBe("diff");
    expect(saveError(err("patch_too_large")).key).toBe("patchTooLarge");
    expect(saveError(err("eval_case_limit")).key).toBe("limit");
    expect(saveError(err("x", undefined, 404)).key).toBe("notFound");
    expect(saveError(new Error("boom")).key).toBe("generic");
  });

  it("formats the counter and the range list", () => {
    expect(formatCount(200000)).toBe("200 000");
    expect(formatCount(12)).toBe("12");
    expect(formatRanges([{ start: 1, end: 1 }, { start: 3, end: 5 }])).toBe("1, 3–5");
    expect(formatRanges(Array.from({ length: 8 }, (_, i) => ({ start: i * 3, end: i * 3 })))).toMatch(/…$/);
  });
});

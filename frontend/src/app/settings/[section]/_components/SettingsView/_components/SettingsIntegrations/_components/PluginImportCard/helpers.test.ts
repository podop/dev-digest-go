import { describe, it, expect } from "vitest";
import { ApiError } from "@/lib/api";
import { checkFile, classifyFailure, conflictsFromDetails, hasConflicts, MAX_SHOWN_ISSUES } from "./helpers";

const MIB = 1024 * 1024;

describe("checkFile", () => {
  it("accepts .zip and .json by extension (any case) and refuses everything else with no size lookup", () => {
    expect(checkFile({ name: "a.zip", size: 10 })).toEqual({ ok: true, kind: "zip" });
    expect(checkFile({ name: "B.JSON", size: 10 })).toEqual({ ok: true, kind: "json" });
    for (const name of ["a.tar", "notes.md", "zip", "a.zip.exe", ""]) {
      expect(checkFile({ name, size: 1 })).toEqual({ ok: false, problem: { kind: "type" } });
    }
  });

  it("applies the per-kind limit: 5 MiB for a zip, 20 MiB for a json", () => {
    expect(checkFile({ name: "a.zip", size: 5 * MIB })).toEqual({ ok: true, kind: "zip" });
    expect(checkFile({ name: "a.zip", size: 5 * MIB + 1 })).toEqual({ ok: false, problem: { kind: "size", maxMb: 5 } });
    expect(checkFile({ name: "a.json", size: 5 * MIB + 1 })).toEqual({ ok: true, kind: "json" });
    expect(checkFile({ name: "a.json", size: 20 * MIB + 1 })).toEqual({ ok: false, problem: { kind: "size", maxMb: 20 } });
  });
});

describe("classifyFailure", () => {
  it("keeps the message and the first issues of a 4xx", () => {
    const issues = Array.from({ length: 9 }, (_, i) => `rule ${i}`);
    const got = classifyFailure(new ApiError("Not a plugin", 422, "invalid_plugin", { issues: [...issues, 7] }));
    expect(got).toEqual({ kind: "message", message: "Not a plugin", issues: issues.slice(0, MAX_SHOWN_ISSUES) });
    expect(classifyFailure(new ApiError("Too big", 413, "payload_too_large"))).toEqual({
      kind: "message",
      message: "Too big",
      issues: [],
    });
  });

  it("reads the conflicts of a 409 and falls back to the message when details are malformed", () => {
    expect(classifyFailure(new ApiError("Conflict", 409, "plugin_conflict", { plugin: true, skills: ["a", 1] }))).toEqual({
      kind: "conflict",
      message: "Conflict",
      conflicts: { plugin: true, skills: ["a"] },
    });
    expect(classifyFailure(new ApiError("Conflict", 409, "plugin_conflict", "oops"))).toEqual({
      kind: "message",
      message: "Conflict",
      issues: [],
    });
  });

  it("treats network failures, 5xx and foreign errors as one generic failure", () => {
    expect(classifyFailure(new ApiError("Cannot reach", 0, "network_error"))).toEqual({ kind: "failed" });
    expect(classifyFailure(new ApiError("boom", 503, "internal_error", { issues: ["x"] }))).toEqual({ kind: "failed" });
    expect(classifyFailure(new Error("Not a .zip or .json file"))).toEqual({ kind: "failed" });
  });
});

describe("conflicts", () => {
  it("hasConflicts is true for a plugin or a skill clash only", () => {
    expect(hasConflicts({ plugin: false, skills: [] })).toBe(false);
    expect(hasConflicts({ plugin: true, skills: [] })).toBe(true);
    expect(hasConflicts({ plugin: false, skills: ["s"] })).toBe(true);
    expect(conflictsFromDetails({ skills: [] })).toBeNull();
  });
});

import { describe, it, expect } from "vitest";
import { makeCase } from "@/test/eval-fixtures";
import { countPassing } from "./helpers";

describe("EvalCasesSection helpers", () => {
  it("countPassing counts cases whose last run passed", () => {
    const status = (s: "passed" | "failed" | "error") => ({ run_id: "r", status: s, ran_at: "2026-10-14T10:00:00.000Z" });
    const cases = [makeCase({ last_result: status("passed") }), makeCase({ last_result: status("failed") }), makeCase({ last_result: status("error") }), makeCase()];
    expect(countPassing(cases)).toBe(1);
    expect(countPassing([])).toBe(0);
  });
});

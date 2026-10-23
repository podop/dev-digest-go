import { describe, it, expect } from "vitest";
import { durationParts, nextSort, openAgeParts, rankRows, sortRows } from "./helpers";
import { row } from "./fixtures";

const ids = (rows: ReturnType<typeof sortRows>) => rows.map((r) => r.row.pr_id);

describe("sortRows", () => {
  const rows = rankRows([
    row({ pr_id: "a", health: 70, size: "L", rounds: 1, cost_usd: 0.5, findings: { CRITICAL: 0, WARNING: 5, SUGGESTION: 0 }, time_to_merge_s: 100 }),
    row({ pr_id: "b", health: null, size: null, rounds: 3, cost_usd: 0.1, findings: { CRITICAL: 1, WARNING: 0, SUGGESTION: 0 }, time_to_merge_s: null, open_age_s: 50 }),
    row({ pr_id: "c", health: 95, size: "S", rounds: 2, cost_usd: 0.9, findings: { CRITICAL: 0, WARNING: 5, SUGGESTION: 1 }, time_to_merge_s: null, open_age_s: null }),
  ]);

  it("keeps the server order without a sort and the rank after sorting", () => {
    expect(ids(sortRows(rows, null))).toEqual(["a", "b", "c"]);
    expect(sortRows(rows, { key: "health", dir: "desc" }).map((r) => r.rank)).toEqual([3, 1, 2]);
  });

  it("sorts every column both ways with missing values last", () => {
    expect(ids(sortRows(rows, { key: "health", dir: "desc" }))).toEqual(["c", "a", "b"]);
    expect(ids(sortRows(rows, { key: "health", dir: "asc" }))).toEqual(["a", "c", "b"]);
    expect(ids(sortRows(rows, { key: "size", dir: "desc" }))).toEqual(["a", "c", "b"]);
    expect(ids(sortRows(rows, { key: "rounds", dir: "asc" }))).toEqual(["a", "c", "b"]);
    expect(ids(sortRows(rows, { key: "cost", dir: "desc" }))).toEqual(["c", "a", "b"]);
    expect(ids(sortRows(rows, { key: "findings", dir: "desc" }))).toEqual(["b", "c", "a"]);
    expect(ids(sortRows(rows, { key: "time", dir: "asc" }))).toEqual(["b", "a", "c"]);
    expect(ids(sortRows(rows, { key: "time", dir: "desc" }))).toEqual(["a", "b", "c"]);
  });
});

describe("nextSort", () => {
  it("cycles descending, ascending, server rank", () => {
    const first = nextSort(null, "cost");
    expect(first).toEqual({ key: "cost", dir: "desc" });
    const second = nextSort(first, "cost");
    expect(second).toEqual({ key: "cost", dir: "asc" });
    expect(nextSort(second, "cost")).toBeNull();
    expect(nextSort(second, "size")).toEqual({ key: "size", dir: "desc" });
  });
});

describe("durations", () => {
  it("formats review and merge time in hours, then days", () => {
    expect(durationParts(2880)).toEqual({ unit: "hours", value: "0.8" });
    expect(durationParts(6840)).toEqual({ unit: "hours", value: "1.9" });
    expect(durationParts(3 * 86400)).toEqual({ unit: "days", value: "3.0" });
  });

  it("formats the open age in whole hours, then whole days", () => {
    expect(openAgeParts(5 * 3600 + 100)).toEqual({ unit: "hours", value: "5" });
    expect(openAgeParts(6 * 86400 + 3600)).toEqual({ unit: "days", value: "6" });
  });
});

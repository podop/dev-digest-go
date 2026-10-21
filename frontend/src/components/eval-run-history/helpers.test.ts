import { describe, it, expect } from "vitest";
import { makeRun } from "@/test/eval-fixtures";
import { followedRunId, runVersion, runningRun } from "./helpers";

describe("eval-run-history helpers", () => {
  it("followedRunId prefers the running run of the list over the one just started", () => {
    const runs = [makeRun({ id: "a", status: "running" }), makeRun({ id: "b" })];
    expect(followedRunId(runs, "z")).toBe("a");
    expect(followedRunId([makeRun({ id: "b" })], "z")).toBe("z");
    expect(followedRunId(undefined, undefined)).toBeUndefined();
  });

  it("runningRun trusts the polled detail over the list", () => {
    const running = makeRun({ id: "a", status: "running", cases_finished: 2 });
    const finished = makeRun({ id: "a" });
    expect(runningRun(undefined, [running])).toBe(running);
    expect(runningRun({ run: finished, results: [] }, [running])).toBeNull();
    expect(runningRun(undefined, [finished])).toBeNull();
  });

  it("runVersion is the skill's version in the skill variant and the agent's otherwise", () => {
    const run = makeRun({ owner_kind: "skill", skill_id: "sk1", skill_version: 5, agent_version: 3 });
    expect(runVersion(run, "skill")).toBe(5);
    expect(runVersion(run, "agent")).toBe(3);
    expect(runVersion({ ...run, skill_version: null }, "skill")).toBe(3);
  });
});

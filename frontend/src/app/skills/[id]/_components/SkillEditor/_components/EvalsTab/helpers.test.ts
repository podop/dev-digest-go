import { describe, it, expect } from "vitest";
import { AGENT } from "@/test/skill-fixtures";
import { makeRun } from "@/test/eval-fixtures";
import { defaultHostAgentId, effectiveHostAgentId, latestFinishedPair, runAllBlocker } from "./helpers";

const A = { ...AGENT, id: "a", name: "Alpha" };
const B = { ...AGENT, id: "b", name: "Beta" };

describe("skill Evals tab helpers", () => {
  it("latestFinishedPair skips running runs but counts completed and failed ones", () => {
    const runs = [
      makeRun({ id: "r4", status: "running" }),
      makeRun({ id: "r3", status: "failed" }),
      makeRun({ id: "r2", status: "completed" }),
      makeRun({ id: "r1", status: "completed" }),
    ];
    const { latest, previous } = latestFinishedPair(runs);
    expect([latest?.id, previous?.id]).toEqual(["r3", "r2"]);
    expect(latestFinishedPair(undefined)).toEqual({ latest: null, previous: null });
  });

  it("defaults to the first agent that uses the skill, else the first by name", () => {
    expect(defaultHostAgentId([B, A], [{ id: "b", name: "Beta", enabled: true }])).toBe("b");
    expect(defaultHostAgentId([B, A], [])).toBe("a");
    expect(defaultHostAgentId([B, A], undefined)).toBe("a");
    expect(defaultHostAgentId([], [])).toBeNull();
  });

  it("keeps a pick only while that agent exists", () => {
    expect(effectiveHostAgentId([A, B], [], "b")).toBe("b");
    expect(effectiveHostAgentId([A], [], "b")).toBe("a");
    expect(effectiveHostAgentId([A, B], [], null)).toBe("a");
  });

  it("names why Run all is blocked", () => {
    expect(runAllBlocker({ noCases: true, noAgents: true })).toBe("noCases");
    expect(runAllBlocker({ noCases: false, noAgents: true })).toBe("noAgents");
    expect(runAllBlocker({ noCases: false, noAgents: false })).toBeNull();
  });
});

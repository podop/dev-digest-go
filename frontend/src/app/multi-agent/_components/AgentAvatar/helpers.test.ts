import { describe, expect, it } from "vitest";
import { FALLBACK_COLORS, FALLBACK_ICON } from "./constants";
import { agentPersona } from "./helpers";

describe("agentPersona", () => {
  it.each([
    ["Security Reviewer", "#ef4444", "Shield"],
    ["Performance Reviewer", "#f59e0b", "Zap"],
    ["Test Quality Reviewer", "#14b8a6", "FlaskConical"],
    ["Architecture", "#10b981", "Boxes"],
    ["Customer-Facing", "#8b5cf6", "Users"],
    ["Junior Mentor", "#3b82f6", "Lightbulb"],
  ])("maps %s by its name", (name, color, icon) => {
    expect(agentPersona(name, "id-1")).toEqual({ color, icon });
  });

  it("gives other agents a stable palette colour hashed from the id", () => {
    const a = agentPersona("Docs bot", "agent-42");
    expect(a).toEqual(agentPersona("Renamed bot", "agent-42"));
    expect(FALLBACK_COLORS).toContain(a.color);
    expect(a.icon).toBe(FALLBACK_ICON);
  });

  it("falls back to the name when there is no id (deleted agent)", () => {
    expect(agentPersona("Deleted agent", "")).toEqual(agentPersona("Deleted agent"));
  });
});

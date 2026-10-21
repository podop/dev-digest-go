import { describe, it, expect } from "vitest";
import type { ContextDoc } from "@devdigest/shared";
import { makeSkill } from "@/test/skill-fixtures";
import { enabledSkillIds, runTokens } from "./helpers";

const doc = (path: string, tokens: number): ContextDoc => ({
  path,
  name: path,
  doc_type: "docs",
  size_bytes: tokens * 4,
  tokens,
  updated_at: "2026-10-01T00:00:00.000Z",
  used_by: 0,
  source: "repo",
  editable: false,
});
const DOCS = [doc("a.md", 100), doc("b.md", 200), doc("c.md", 400)];

describe("runTokens", () => {
  it("de-duplicates across the agent and its skills, splits out the skill-only part, ignores unlisted paths", () => {
    expect(runTokens(DOCS, ["a.md"], [["b.md", "a.md"], ["c.md", "gone.md"]])).toEqual({ total: 700, fromSkills: 600 });
    expect(runTokens(DOCS, [], [])).toEqual({ total: 0, fromSkills: 0 });
  });
});

describe("enabledSkillIds", () => {
  it("keeps link order and drops disabled or unknown skills", () => {
    const skills = [makeSkill({ id: "s1" }), makeSkill({ id: "s2", enabled: false }), makeSkill({ id: "s3" })];
    const links = [
      { agent_id: "ag", skill_id: "s3", order: 0 },
      { agent_id: "ag", skill_id: "s2", order: 1 },
      { agent_id: "ag", skill_id: "s1", order: 2 },
      { agent_id: "ag", skill_id: "ghost", order: 3 },
    ];
    expect(enabledSkillIds(skills, links)).toEqual(["s3", "s1"]);
  });
});

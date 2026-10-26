/* SkillEditor — the plugin label above the tabs (FR18, AC21); the tabs themselves are covered
   by their own tests. */
import React from "react";
import { describe, it, expect, afterEach, vi } from "vitest";
import { renderWithProviders, screen, cleanup } from "@/test/render";
import { mockFetch } from "@/test/fetch-mock";
import { makeSkill } from "@/test/skill-fixtures";
import type { Skill } from "@devdigest/shared";
import { SkillEditor } from "./SkillEditor";
import { useSkillDraft } from "./useSkillDraft";

vi.mock("next/navigation", () => ({ useRouter: () => ({ push: vi.fn(), replace: vi.fn() }) }));

afterEach(cleanup);

function Editor({ skill }: { skill: Skill }) {
  const draft = useSkillDraft(skill);
  return <SkillEditor skill={skill} draft={draft} tab="preview" onTab={() => {}} />;
}

describe("SkillEditor plugin label", () => {
  it("labels a plugin's skill, marks a disabled plugin, and shows nothing for an unowned skill", () => {
    mockFetch({});
    const { rerender } = renderWithProviders(<Editor skill={makeSkill()} />);
    expect(screen.queryByText(/From plugin/)).toBeNull();

    rerender(<Editor skill={makeSkill({ plugin: { id: "p1", name: "acme", enabled: true } })} />);
    expect(screen.getByText("From plugin acme")).toBeInTheDocument();
    expect(screen.queryByText("plugin disabled")).toBeNull();

    rerender(<Editor skill={makeSkill({ plugin: { id: "p1", name: "acme", enabled: false } })} />);
    expect(screen.getByText("From plugin acme")).toBeInTheDocument();
    expect(screen.getByText("plugin disabled")).toBeInTheDocument();
  });
});

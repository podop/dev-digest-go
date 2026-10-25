import { describe, it, expect } from "vitest";
import { NAV } from "@devdigest/ui";
import type { Repo } from "@/lib/types";
import { activeKeyFor, switcherWorkspaceName, toShellRepo } from "./helpers";

describe("activeKeyFor", () => {
  it("highlights the tour for a repo's onboarding route", () => {
    expect(activeKeyFor("/repos/r1/onboarding")).toBe("onboarding-tour");
  });

  it("does not highlight anything on the add-repository screen", () => {
    expect(activeKeyFor("/onboarding")).toBe("");
  });

  it("highlights the PR Leaderboard on /leaderboard and not Pull Requests", () => {
    expect(activeKeyFor("/leaderboard")).toBe("pr-leaderboard");
    expect(activeKeyFor("/leaderboard?period=30d")).toBe("pr-leaderboard");
  });

  it("keeps the other repo routes", () => {
    expect(activeKeyFor("/repos/r1/context")).toBe("context");
    expect(activeKeyFor("/repos/r1/pulls")).toBe("pulls");
  });
});

describe("switcherWorkspaceName / toShellRepo", () => {
  const repo = {
    id: "r1",
    full_name: "acme/payments",
    default_branch: "main",
    last_polled_at: "2026-10-25T10:00:00Z",
  } as Repo;

  it("keeps a name up to 32 code points whole and cuts a longer one with an ellipsis", () => {
    expect(switcherWorkspaceName("acme · payments")).toBe("acme · payments");
    expect(switcherWorkspaceName("x".repeat(32))).toBe("x".repeat(32));
    expect(switcherWorkspaceName("x".repeat(33))).toBe(`${"x".repeat(32)}…`);
    expect(switcherWorkspaceName("😀".repeat(40))).toBe(`${"😀".repeat(32)}…`);
  });

  it("gives no name for a missing or blank one", () => {
    expect(switcherWorkspaceName(undefined)).toBe("");
    expect(switcherWorkspaceName(null)).toBe("");
    expect(switcherWorkspaceName("  ")).toBe("");
  });

  it("appends the name to the sync label only when there is one", () => {
    expect(toShellRepo(repo).syncedLabel).toBe("synced");
    expect(toShellRepo(repo, undefined).syncedLabel).toBe("synced");
    expect(toShellRepo(repo, "acme").syncedLabel).toBe("synced · acme");
    expect(toShellRepo({ ...repo, last_polled_at: null }, "acme").syncedLabel).toBe("not synced · acme");
  });
});

describe("NAV", () => {
  it("lists the PR Leaderboard directly below Pull Requests in WORKSPACE", () => {
    const group = NAV.find((g) => g.section === "WORKSPACE");
    const keys = group?.items.map((i) => i.key) ?? [];
    expect(keys.indexOf("pr-leaderboard")).toBe(keys.indexOf("pulls") + 1);
    expect(group?.items.find((i) => i.key === "pr-leaderboard")).toMatchObject({
      label: "PR Leaderboard",
      icon: "Trophy",
      href: "/leaderboard",
    });
  });

  it("lists the onboarding tour in the workspace group", () => {
    const items = NAV.flatMap((g) => g.items);
    expect(items.find((i) => i.key === "onboarding-tour")?.href).toBe("/repos/:repoId/onboarding");
  });

  it("lists the Eval Dashboard in the Skills Lab group and highlights it on /eval", () => {
    const group = NAV.find((g) => g.items.some((i) => i.key === "eval"));
    expect(group?.section).toBe("SKILLS LAB");
    expect(group?.items.find((i) => i.key === "eval")?.href).toBe("/eval");
    expect(activeKeyFor("/eval")).toBe("eval");
  });
});

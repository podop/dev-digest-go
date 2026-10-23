import { describe, it, expect } from "vitest";
import { NAV } from "@devdigest/ui";
import { activeKeyFor } from "./helpers";

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

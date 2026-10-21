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

  it("keeps the other repo routes", () => {
    expect(activeKeyFor("/repos/r1/context")).toBe("context");
    expect(activeKeyFor("/repos/r1/pulls")).toBe("pulls");
  });
});

describe("NAV", () => {
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

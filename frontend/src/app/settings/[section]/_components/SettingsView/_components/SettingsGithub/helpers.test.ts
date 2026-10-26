import { describe, it, expect } from "vitest";
import { lacksRepoScope, scopesText } from "./helpers";

describe("scope rules", () => {
  it("joins scopes with ' · ' and warns only for reported scopes without repo (A11)", () => {
    expect(scopesText(["repo", "read:org"])).toBe("repo · read:org");
    expect(lacksRepoScope(["read:org"])).toBe(true);
    expect(lacksRepoScope([])).toBe(true);
    expect(lacksRepoScope(["repo", "read:org"])).toBe(false);
    expect(lacksRepoScope(null)).toBe(false);
    expect(lacksRepoScope(undefined)).toBe(false);
  });
});

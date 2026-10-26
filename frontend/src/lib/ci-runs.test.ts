import { describe, it, expect } from "vitest";
import { pullRequestHref } from "./ci-runs";

describe("pullRequestHref", () => {
  it("links a run's PR on GitHub (CI runs are GitHub-only)", () => {
    expect(pullRequestHref({ repo: "acme/api", pr_number: 7 })).toBe("https://github.com/acme/api/pull/7");
  });

  it("is null without a PR number or for a name that is not owner/name", () => {
    expect(pullRequestHref({ repo: "acme/api", pr_number: null })).toBeNull();
    expect(pullRequestHref({ repo: "acme/platform/payments", pr_number: 7 })).toBeNull();
  });
});

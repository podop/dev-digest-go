import { describe, it, expect } from "vitest";
import type { CiRefreshResult } from "@devdigest/shared";
import { gitlabRefreshErrorCode, pullRequestHref, pullRequestRef, refreshFailures, runRepoLabel } from "./ci-runs";

describe("pullRequestHref", () => {
  it("links a GitHub run's PR from owner/name", () => {
    expect(pullRequestHref({ repo: "acme/api", pr_number: 7 })).toBe("https://github.com/acme/api/pull/7");
    expect(pullRequestHref({ repo: "acme/api", pr_number: 7, provider: "github", pr_url: "https://evil.example/x" })).toBe(
      "https://github.com/acme/api/pull/7",
    );
  });

  it("is null without a PR number or for a GitHub name that is not owner/name", () => {
    expect(pullRequestHref({ repo: "acme/api", pr_number: null })).toBeNull();
    expect(pullRequestHref({ repo: "acme/platform/payments", pr_number: 7 })).toBeNull();
  });

  it("links a GitLab run only through the server's http(s) pr_url (EC34)", () => {
    const run = { repo: "acme/platform/payments", pr_number: 4, provider: "gitlab" } as const;
    const url = "https://git.corp.example/gitlab/acme/platform/payments/-/merge_requests/4";
    expect(pullRequestHref({ ...run, pr_url: url })).toBe(url);
    expect(pullRequestHref({ ...run, pr_url: null })).toBeNull();
    expect(pullRequestHref(run)).toBeNull();
    expect(pullRequestHref({ ...run, pr_url: "javascript:alert(1)" })).toBeNull();
    expect(pullRequestHref({ ...run, pr_number: null, pr_url: url })).toBeNull();
  });
});

describe("pullRequestRef", () => {
  it("is #<n> on GitHub, !<n> on GitLab and null without a number", () => {
    expect(pullRequestRef({ pr_number: 7 })).toBe("#7");
    expect(pullRequestRef({ pr_number: 7, provider: "github" })).toBe("#7");
    expect(pullRequestRef({ pr_number: 4, provider: "gitlab" })).toBe("!4");
    expect(pullRequestRef({ pr_number: null, provider: "gitlab" })).toBeNull();
  });
});

describe("runRepoLabel", () => {
  it("adds the host for GitLab only", () => {
    expect(runRepoLabel({ repo: "acme/api", provider: "github", host: "github.com" })).toBe("acme/api");
    expect(runRepoLabel({ repo: "acme/api" })).toBe("acme/api");
    expect(runRepoLabel({ repo: "acme/platform/payments", provider: "gitlab", host: "git.corp.example" })).toBe(
      "acme/platform/payments · git.corp.example",
    );
    expect(runRepoLabel({ repo: null })).toBeNull();
  });
});

describe("refreshFailures", () => {
  it("lists error and not_checked installations, naming the host of a GitLab one", () => {
    const result = {
      ingested: 0,
      degraded: true,
      installations: [
        { installation_id: "i1", repo: "acme/api", host: "github.com", state: "error", error_code: "github_rate_limited", ingested: 0 },
        { installation_id: "i2", repo: "acme/platform/payments", host: "git.corp.example", state: "error", error_code: "gitlab_token_missing", ingested: 0 },
        { installation_id: "i3", repo: "acme/web", state: "not_checked", ingested: 0 },
        { installation_id: "i4", repo: "acme/ok", state: "ok", ingested: 2 },
      ],
    } as CiRefreshResult;
    expect(refreshFailures(result)).toEqual([
      { installationId: "i1", repo: "acme/api", code: "github_rate_limited" },
      { installationId: "i2", repo: "acme/platform/payments · git.corp.example", code: "gitlab_token_missing" },
      { installationId: "i3", repo: "acme/web", code: "not_checked" },
    ]);
    expect(refreshFailures(undefined)).toEqual([]);
  });
});

describe("gitlabRefreshErrorCode", () => {
  it("accepts the five GitLab refresh codes only", () => {
    for (const code of ["gitlab_permission_denied", "gitlab_rate_limited", "gitlab_unavailable", "repo_host_not_connected", "gitlab_token_missing"]) {
      expect(gitlabRefreshErrorCode(code)).toBe(code);
    }
    expect(gitlabRefreshErrorCode("github_rate_limited")).toBeNull();
    expect(gitlabRefreshErrorCode("not_checked")).toBeNull();
  });
});

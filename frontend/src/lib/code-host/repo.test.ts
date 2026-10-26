import { describe, it, expect } from "vitest";
import { blobUrl, codeHostName, isGitLab, isNotConnected, issueUrl, prRef, prUrl, repoLabel, webRoot } from "./repo";

const GH = { provider: "github", host: "github.com", full_name: "acme/web", web_url: "https://github.com/acme/web" } as const;
const GL = {
  provider: "gitlab",
  host: "git.corp.example:8443",
  full_name: "acme/platform/payments",
  web_url: "https://git.corp.example:8443/gitlab/acme/platform/payments",
} as const;

describe("provider checks", () => {
  it("isGitLab / isNotConnected read provider and connection_id; a missing provider is GitHub", () => {
    expect(isGitLab(GL)).toBe(true);
    expect(isGitLab(GH)).toBe(false);
    expect(isGitLab({})).toBe(false);
    expect(isGitLab(null)).toBe(false);
    expect(isNotConnected({ provider: "gitlab", connection_id: null })).toBe(true);
    expect(isNotConnected({ provider: "gitlab", connection_id: "c1" })).toBe(false);
    expect(isNotConnected({ provider: "github", connection_id: null })).toBe(false);
    expect(isNotConnected({ connection_id: null })).toBe(false);
    expect(isNotConnected(undefined)).toBe(false);
    expect(codeHostName(GL)).toBe("GitLab");
    expect(codeHostName(GH)).toBe("GitHub");
  });
});

describe("references and labels", () => {
  it("prRef is !n for GitLab and #n for GitHub (and for an unknown repo)", () => {
    expect(prRef(GL, 12)).toBe("!12");
    expect(prRef(GH, 12)).toBe("#12");
    expect(prRef(undefined, "7")).toBe("#7");
  });

  it("repoLabel adds the host for GitLab only", () => {
    expect(repoLabel(GL)).toBe("acme/platform/payments · git.corp.example:8443");
    expect(repoLabel(GH)).toBe("acme/web");
    expect(repoLabel({ provider: "gitlab", full_name: "a/b" })).toBe("a/b");
  });
});

describe("links", () => {
  it("GitHub keeps today's URLs, from web_url or — without one — from the full name", () => {
    const bare = { provider: "github", full_name: "acme/web" } as const;
    for (const repo of [GH, bare, { full_name: "acme/web" }]) {
      expect(prUrl(repo, 5)).toBe("https://github.com/acme/web/pull/5");
      expect(issueUrl(repo, 9)).toBe("https://github.com/acme/web/issues/9");
      expect(blobUrl(repo, "abc", "src/a b/ü.ts")).toBe("https://github.com/acme/web/blob/abc/src/a%20b/%C3%BC.ts");
      expect(blobUrl(repo, "abc", "a.ts", 3)).toBe("https://github.com/acme/web/blob/abc/a.ts#L3");
      expect(blobUrl(repo, "abc", "a.ts", 3, 3)).toBe("https://github.com/acme/web/blob/abc/a.ts#L3");
      expect(blobUrl(repo, "abc", "a.ts", 3, 9)).toBe("https://github.com/acme/web/blob/abc/a.ts#L3-L9");
    }
  });

  it("GitLab links are built from web_url with the /-/ routes and #L<a>-<b>", () => {
    const root = "https://git.corp.example:8443/gitlab/acme/platform/payments";
    expect(prUrl(GL, 5)).toBe(`${root}/-/merge_requests/5`);
    expect(issueUrl(GL, 9)).toBe(`${root}/-/issues/9`);
    expect(blobUrl(GL, "abc", "src/a b.ts", 3, 9)).toBe(`${root}/-/blob/abc/src/a%20b.ts#L3-9`);
    expect(blobUrl(GL, "abc", "a.ts", 3, 3)).toBe(`${root}/-/blob/abc/a.ts#L3`);
    expect(blobUrl(GL, "abc", "a.ts")).toBe(`${root}/-/blob/abc/a.ts`);
    expect(prUrl({ ...GL, web_url: `${root}/` }, 5)).toBe(`${root}/-/merge_requests/5`);
  });

  it("no link without a usable https web_url: a GitLab repo with none, a non-https or malformed value, no repo", () => {
    expect(prUrl({ provider: "gitlab", full_name: "a/b" }, 1)).toBeNull();
    expect(prUrl({ ...GL, web_url: "javascript:alert(1)" }, 1)).toBeNull();
    expect(prUrl({ ...GL, web_url: "http://gitlab.example/a/b" }, 1)).toBeNull();
    expect(prUrl({ ...GL, web_url: "not a url" }, 1)).toBeNull();
    expect(issueUrl(null, 1)).toBeNull();
    expect(blobUrl(undefined, "abc", "a.ts")).toBeNull();
    expect(webRoot({ provider: "github" })).toBeNull();
  });
});

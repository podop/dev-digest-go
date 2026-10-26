import { describe, it, expect } from "vitest";
import {
  AutomaticReviewsStatus,
  ConnTestRequest,
  ConnTestResult,
  GitLabConnection,
  GitLabConnectionInput,
  GitLabConnectionUpdate,
  GitLabConnTestResult,
  LeaderboardRepo,
  Repo,
  RepoInput,
  parseRepoUrl,
  REPO_URL_INVALID_MESSAGE,
} from "@devdigest/shared";

describe("parseRepoUrl", () => {
  it("keeps the GitHub forms: https and ssh, .git, trailing slash, exactly owner/name", () => {
    const want = { provider: "github", host: "github.com", path: "acme/web", owner: "acme", name: "web" };
    for (const url of [
      "https://github.com/acme/web",
      "https://github.com/acme/web.git",
      "https://github.com/acme/web/",
      "https://github.com/acme/web.git/",
      "git@github.com:acme/web",
      "git@github.com:acme/web.git",
      "  https://github.com/acme/web  ",
    ]) {
      expect(parseRepoUrl(url), url).toEqual(want);
    }
    for (const url of ["https://github.com/acme", "https://github.com/acme/web/pulls", "http://github.com/acme/web"]) {
      expect(parseRepoUrl(url), url).toBeNull();
    }
  });

  it("takes 2 to 21 segments for any other host and splits owner (namespace) from name", () => {
    expect(parseRepoUrl("https://gitlab.com/group/project")).toEqual({
      provider: "gitlab",
      host: "gitlab.com",
      path: "group/project",
      owner: "group",
      name: "project",
    });
    expect(parseRepoUrl("https://GitLab.com:443/a/b/c.git/")).toMatchObject({ host: "gitlab.com", owner: "a/b", name: "c" });
    expect(parseRepoUrl("https://git.corp.example:8443/g/p")).toMatchObject({ host: "git.corp.example:8443" });
    expect(parseRepoUrl("git@git.corp.example:g/s/p.git")).toMatchObject({ provider: "gitlab", host: "git.corp.example", path: "g/s/p" });
    const deep = (n: number) => `https://gitlab.com/${Array.from({ length: n }, (_, i) => `s${i}`).join("/")}`;
    expect(parseRepoUrl(deep(21))).not.toBeNull();
    expect(parseRepoUrl(deep(22))).toBeNull();
    expect(parseRepoUrl(deep(1))).toBeNull();
  });

  it("refuses unsafe or foreign shapes: http, ssh://, userinfo, query, fragment, '-' segments, dot segments", () => {
    for (const url of [
      "http://gitlab.com/a/b",
      "ssh://git@gitlab.com/a/b",
      "https://user:pw@gitlab.com/a/b",
      "https://gitlab.com/a/b?x=1",
      "https://gitlab.com/a/b#frag",
      "https://gitlab.com/a/b/-/merge_requests/1",
      "https://gitlab.com/a/-b",
      "https://gitlab.com/a/../b",
      "https://gitlab.com/a/.git",
      "https://gitlab.com//a/b",
      "https://gitlab.com/a//b",
      "https://gitlab.com:99999/a/b",
      "https://gitlab.com:0/a/b",
      "git@gitlab.com:a",
      "git@gitlab.com:a/b/",
      "not a url",
      "",
    ]) {
      expect(parseRepoUrl(url), url).toBeNull();
    }
  });

  it("RepoInput trims and answers the FR12 message", () => {
    expect(RepoInput.parse({ url: "  https://gitlab.com/g/p " }).url).toBe("https://gitlab.com/g/p");
    const bad = RepoInput.safeParse({ url: "https://gitlab.com/g" });
    expect(bad.success).toBe(false);
    expect(bad.error?.issues[0]).toMatchObject({ path: ["url"], message: REPO_URL_INVALID_MESSAGE });
    expect(REPO_URL_INVALID_MESSAGE).toBe(
      "Must be a GitHub or GitLab repository URL, e.g. https://github.com/owner/repo or https://gitlab.com/group/project",
    );
  });
});

const REPO = {
  id: "r1",
  workspace_id: "w1",
  owner: "acme/platform",
  name: "payments",
  full_name: "acme/platform/payments",
  default_branch: "main",
  clone_path: null,
  last_polled_at: null,
  created_by: null,
};

describe("shapes", () => {
  it("Repo and LeaderboardRepo take the new keys and still accept the old shape", () => {
    const gl = { ...REPO, provider: "gitlab", host: "gitlab.com", web_url: "https://gitlab.com/acme/platform/payments", connection_id: null };
    expect(Repo.strict().parse(gl)).toEqual(gl);
    expect(Repo.strict().parse(REPO)).toEqual(REPO);
    expect(Repo.safeParse({ ...gl, provider: "bitbucket" }).success).toBe(false);
    const lb = { id: "r1", full_name: "a/b", last_polled_at: null, provider: "gitlab", host: "gitlab.com", web_url: "https://gitlab.com/a/b" };
    expect(LeaderboardRepo.strict().parse(lb)).toEqual(lb);
    expect(LeaderboardRepo.strict().safeParse({ id: "r1", full_name: "a/b", last_polled_at: null }).success).toBe(true);
  });

  it("a connection carries no token field and the test shapes take gitlab", () => {
    const conn = {
      id: "c1",
      name: "Corp",
      base_url: "https://git.corp.example",
      host: "git.corp.example",
      token_configured: true,
      repo_count: 2,
      created_at: "2026-10-26T10:00:00.000Z",
      updated_at: "2026-10-26T10:00:00.000Z",
    };
    expect(GitLabConnection.strict().parse(conn)).toEqual(conn);
    expect(GitLabConnection.strict().safeParse({ ...conn, token: "x" }).success).toBe(false);

    expect(ConnTestRequest.parse({ provider: "gitlab", connection_id: "c1", key: "glpat-x" })).toEqual({
      provider: "gitlab",
      connection_id: "c1",
      key: "glpat-x",
    });
    const result = {
      provider: "gitlab",
      ok: true,
      message: "Connected as @octo",
      latency_ms: 120,
      login: "octo",
      scopes: ["read_api"],
      expires_at: "2026-11-01",
      instance_version: "17.5.1-ee",
      token_access: "read_only",
      warnings: [{ code: "read_only_token", message: "Read-only token" }],
    };
    expect(GitLabConnTestResult.strict().parse(result)).toEqual(result);
    expect(ConnTestResult.strict().parse(result)).toEqual(result);
    expect(ConnTestResult.strict().safeParse({ ...result, warnings: [{ code: "other", message: "x" }] }).success).toBe(false);
    const failed = { ...result, ok: false, latency_ms: null, login: null, scopes: null, expires_at: null, instance_version: null, token_access: null, warnings: [] };
    expect(GitLabConnTestResult.strict().safeParse(failed).success).toBe(true);
  });

  it("the input rules: token 1-4096 without whitespace, name up to 80, PUT all optional", () => {
    const ok = { base_url: "https://gitlab.com", name: "  GL ", token: "glpat-abc" };
    expect(GitLabConnectionInput.parse(ok).name).toBe("GL");
    expect(GitLabConnectionInput.parse({ base_url: "https://gitlab.com" })).toEqual({ base_url: "https://gitlab.com" });
    for (const token of ["", "a b", "a\tb", "a\u0000b", "x".repeat(4097)]) {
      expect(GitLabConnectionInput.safeParse({ ...ok, token }).success, JSON.stringify(token)).toBe(false);
    }
    expect(GitLabConnectionInput.safeParse({ ...ok, token: null }).success).toBe(true);
    expect(GitLabConnectionInput.safeParse({ ...ok, name: "x".repeat(81) }).success).toBe(false);
    expect(GitLabConnectionUpdate.safeParse({}).success).toBe(true);
    expect(GitLabConnectionUpdate.safeParse({ token: null }).success).toBe(true);
  });

  it("the automatic-reviews state takes no_code_host_token", () => {
    const base = { last_poll_at: null, prs_tracked: 0, poll_interval_min: 5 };
    expect(AutomaticReviewsStatus.safeParse({ ...base, state: "no_code_host_token" }).success).toBe(true);
  });
});

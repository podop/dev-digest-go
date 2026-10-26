import { describe, expect, it } from "vitest";
import type { GitLabConnection, SecretsStatus } from "@/lib/types";
import type { SetupStep } from "./constants";
import {
  connectionForUrl,
  gitlabTokenCount,
  gitlabTokenPageUrl,
  hostKeyOf,
  initialHost,
  initialProvider,
  lacksRepoScope,
  resolveStep,
  stepsDone,
  type StepDone,
} from "./helpers";

const NONE: SecretsStatus = { openai: false, anthropic: false, openrouter: false, github: false };
const ALL: StepDone = { model: true, "code-host": true, repo: true };
const NOTHING: StepDone = { model: false, "code-host": false, repo: false };

describe("resolveStep (the /setup?step= contract, FR22)", () => {
  const cases: { name: string; requested: string | undefined; done: StepDone; want: SetupStep }[] = [
    { name: "model is honoured when nothing is done", requested: "model", done: NOTHING, want: "model" },
    { name: "model is honoured when everything is done (Run setup again, D14)", requested: "model", done: ALL, want: "model" },
    { name: "code-host is honoured once the model key exists", requested: "code-host", done: { model: true, "code-host": false, repo: false }, want: "code-host" },
    { name: "code-host without a model key falls back to model", requested: "code-host", done: NOTHING, want: "model" },
    { name: "repo is honoured when the first two are done", requested: "repo", done: { model: true, "code-host": true, repo: false }, want: "repo" },
    { name: "repo without a code host falls back to code-host (EC14)", requested: "repo", done: { model: true, "code-host": false, repo: false }, want: "code-host" },
    { name: "repo with nothing falls back to model", requested: "repo", done: NOTHING, want: "model" },
    { name: "absent opens the first incomplete step", requested: undefined, done: { model: true, "code-host": false, repo: false }, want: "code-host" },
    { name: "absent with everything done opens repo", requested: undefined, done: ALL, want: "repo" },
    { name: "the old id github is the code-host step (FR38, AC30)", requested: "github", done: { model: true, "code-host": false, repo: false }, want: "code-host" },
    { name: "the old id github is honoured when everything is done", requested: "github", done: ALL, want: "code-host" },
    { name: "the old id github without a model key falls back to model", requested: "github", done: NOTHING, want: "model" },
    { name: "unknown opens the first incomplete step", requested: "nope", done: NOTHING, want: "model" },
    { name: "empty opens the first incomplete step", requested: "", done: { model: true, "code-host": true, repo: false }, want: "repo" },
    { name: "a prototype-chain key is unknown", requested: "constructor", done: ALL, want: "repo" },
  ];
  it.each(cases)("$name", ({ requested, done, want }) => {
    expect(resolveStep(requested, done)).toBe(want);
  });
});

describe("stepsDone", () => {
  it("counts any model key, an env-provided token and tested providers, and the repo count", () => {
    expect(stepsDone(NONE, 0, [])).toEqual(NOTHING);
    expect(stepsDone({ ...NONE, anthropic: true, github: true }, 2, [])).toEqual(ALL);
    // A passing test stores the key before the secrets-status refetch lands.
    expect(stepsDone(NONE, 0, ["openai", "github"])).toEqual({ model: true, "code-host": true, repo: false });
    // A custom provider completes the model step with no key at all (FR21, D21).
    expect(stepsDone(NONE, 0, [], 1)).toEqual({ model: true, "code-host": false, repo: false });
  });
});

describe("stepsDone with GitLab (FR38)", () => {
  it("a GitLab connection with a token, or a passing GitLab test, completes the code-host step", () => {
    expect(stepsDone(NONE, 0, [], 0, 1)["code-host"]).toBe(true);
    expect(stepsDone(NONE, 0, ["gitlab"])["code-host"]).toBe(true);
    expect(stepsDone(NONE, 0, [], 0, 0)["code-host"]).toBe(false);
    expect(stepsDone({ ...NONE, github: true }, 0, [])["code-host"]).toBe(true);
  });
});

const conn = (over: Partial<GitLabConnection>): GitLabConnection => ({
  id: "c1",
  name: "gitlab.com",
  base_url: "https://gitlab.com",
  host: "gitlab.com",
  token_configured: true,
  repo_count: 0,
  created_at: "2026-10-26T10:00:00Z",
  updated_at: "2026-10-26T10:00:00Z",
  ...over,
});

describe("GitLab connection helpers", () => {
  it("counts only connections with a token", () => {
    expect(gitlabTokenCount([])).toBe(0);
    expect(gitlabTokenCount([conn({ token_configured: false }), conn({ id: "c2" })])).toBe(1);
  });

  it("derives the host key like the server: lower-case, port unless 443, https only", () => {
    expect(hostKeyOf("https://GitLab.com")).toBe("gitlab.com");
    expect(hostKeyOf(" https://gitlab.com:443/ ")).toBe("gitlab.com");
    expect(hostKeyOf("https://git.corp.example:8443/gitlab")).toBe("git.corp.example:8443");
    expect(hostKeyOf("https://gitlab.com./")).toBe("gitlab.com");
    expect(hostKeyOf("http://gitlab.com")).toBeNull();
    expect(hostKeyOf("gitlab.com")).toBeNull();
    expect(hostKeyOf("")).toBeNull();
  });

  it("finds the connection of the typed URL's host, whatever the path prefix or case", () => {
    const list = [conn({}), conn({ id: "c2", host: "git.corp.example:8443", base_url: "https://git.corp.example:8443/gitlab" })];
    expect(connectionForUrl(list, "https://GITLAB.com")?.id).toBe("c1");
    expect(connectionForUrl(list, "https://git.corp.example:8443")?.id).toBe("c2");
    expect(connectionForUrl(list, "https://git.corp.example")).toBeUndefined();
    expect(connectionForUrl(list, "nonsense")).toBeUndefined();
  });

  it("opens on GitLab only when GitLab is the sole connected host", () => {
    expect(initialHost(NONE, 0)).toBe("github");
    expect(initialHost({ ...NONE, github: true }, 2)).toBe("github");
    expect(initialHost(NONE, 1)).toBe("gitlab");
  });

  it("links the token page of the typed instance (path prefix kept), https without credentials or query only", () => {
    const page = "/-/user_settings/personal_access_tokens?name=DevDigest&scopes=api,read_repository";
    expect(gitlabTokenPageUrl("https://gitlab.com")).toBe(`https://gitlab.com${page}`);
    expect(gitlabTokenPageUrl("https://git.example.com/gitlab/")).toBe(`https://git.example.com/gitlab${page}`);
    for (const bad of ["http://gitlab.com", "javascript:alert(1)", "https://u:p@gitlab.com", "https://gitlab.com?x=1", "https://gitlab.com#x", "x"]) {
      expect(gitlabTokenPageUrl(bad)).toBeNull();
    }
  });
});

describe("initialProvider and lacksRepoScope", () => {
  it("opens on OpenRouter unless another provider already has a key", () => {
    expect(initialProvider(NONE)).toBe("openrouter");
    expect(initialProvider({ ...NONE, openai: true, anthropic: true })).toBe("openai");
  });

  it("warns only for a classic token (scopes reported) without repo", () => {
    expect(lacksRepoScope(null)).toBe(false);
    expect(lacksRepoScope(undefined)).toBe(false);
    expect(lacksRepoScope([])).toBe(true);
    expect(lacksRepoScope(["gist"])).toBe(true);
    expect(lacksRepoScope(["gist", "repo"])).toBe(false);
  });
});

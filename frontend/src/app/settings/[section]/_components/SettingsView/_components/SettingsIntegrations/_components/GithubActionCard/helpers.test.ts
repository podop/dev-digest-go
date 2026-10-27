import { describe, it, expect } from "vitest";
import { entryKey, installedAgents, isGitLabEntry, lastRunView, manageHref, relativeTime, runStatusKey } from "./helpers";

const NOW = Date.parse("2026-10-26T12:00:00.000Z");
const ago = (ms: number) => new Date(NOW - ms).toISOString();

describe("GithubActionCard helpers", () => {
  it("relativeTime words a past time and gives null for a missing or invalid one", () => {
    expect(relativeTime(ago(2 * 3600_000), NOW)).toBe("2 hours ago");
    expect(relativeTime(ago(3 * 86400_000), NOW)).toBe("3 days ago");
    expect(relativeTime(null, NOW)).toBeNull();
    expect(relativeTime("not a date", NOW)).toBeNull();
  });

  it("runStatusKey maps the wire statuses and falls back to unknown", () => {
    expect(runStatusKey("no_findings")).toBe("noFindings");
    expect(runStatusKey("succeeded")).toBe("succeeded");
    expect(runStatusKey("exploded")).toBe("unknown");
    expect(runStatusKey(null)).toBe("unknown");
  });

  it("lastRunView is null without a run or a time, and drops every link that is not http(s)", () => {
    const base = { status: "failed" as const, ran_at: ago(60_000), agent_id: "a1" };
    expect(lastRunView(null, NOW)).toBeNull();
    expect(lastRunView({ ...base, ran_at: null, github_url: null }, NOW)).toBeNull();
    expect(lastRunView({ ...base, github_url: "https://github.com/acme/api/actions/runs/1" }, NOW)).toMatchObject({
      statusKey: "failed",
      time: "1 minute ago",
      href: "https://github.com/acme/api/actions/runs/1",
    });
    for (const bad of ["javascript:alert(1)", "data:text/html,x", "//evil.example/x", "", null]) {
      expect(lastRunView({ ...base, github_url: bad }, NOW)?.href, String(bad)).toBeNull();
    }
  });

  it("manageHref opens the CI tab of the agent editor; installedAgents lists each agent once", () => {
    expect(manageHref("ag 1")).toBe("/agents/ag%201?tab=ci");
    const inst = (id: string, agent: string, name: string) => ({ id, agent_id: agent, agent_name: name, slug: null, installed_at: "" });
    expect(installedAgents([inst("i1", "a1", "One"), inst("i2", "a2", "Two"), inst("i3", "a1", "One")])).toEqual([
      { id: "a1", name: "One" },
      { id: "a2", name: "Two" },
    ]);
  });

  it("isGitLabEntry reads target_type or provider (absent on pre-GitLab payloads) and entryKey tells hosts apart", () => {
    expect(isGitLabEntry({ target_type: "gitlab_ci" })).toBe(true);
    expect(isGitLabEntry({ provider: "gitlab" })).toBe(true);
    expect(isGitLabEntry({ provider: "github", target_type: "gha" })).toBe(false);
    expect(isGitLabEntry({})).toBe(false);
    expect(entryKey({ repo: "a/b", host: "gitlab.com", target_type: "gitlab_ci" })).not.toBe(
      entryKey({ repo: "a/b", host: "github.com", target_type: "gha" }),
    );
  });
});

import { describe, it, expect } from "vitest";
import type { CiRepoEntry } from "@/lib/types";
import { pluginCiInstallationCount } from "./helpers";

const PLUGIN = { id: "p1", name: "acme", enabled: false };

function install(id: string, agentId: string) {
  return { id, agent_id: agentId, agent_name: agentId, slug: null, installed_at: "2026-10-26T10:00:00.000Z" };
}

describe("pluginCiInstallationCount", () => {
  it("counts the installations of the plugin's agents across every repository, and only those", () => {
    const agents = [
      { id: "a1", plugin: PLUGIN },
      { id: "a2", plugin: PLUGIN },
      { id: "a3", plugin: { id: "p2", name: "other", enabled: true } },
      { id: "a4", plugin: null },
      { id: "a5" },
    ];
    const repos: CiRepoEntry[] = [
      { repo: "acme/api", installations: [install("i1", "a1"), install("i2", "a2"), install("i3", "a3")], last_run: null },
      { repo: "acme/web", installations: [install("i4", "a1"), install("i5", "a4"), install("i6", "gone")], last_run: null },
    ];
    expect(pluginCiInstallationCount("p1", agents, repos)).toBe(3);
    expect(pluginCiInstallationCount("p2", agents, repos)).toBe(1);
    expect(pluginCiInstallationCount("none", agents, repos)).toBe(0);
    expect(pluginCiInstallationCount("p1", agents, [])).toBe(0);
  });
});

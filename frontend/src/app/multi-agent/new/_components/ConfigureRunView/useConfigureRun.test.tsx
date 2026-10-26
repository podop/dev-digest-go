/* useConfigureRun — FR18 / AC21: agents of a disabled plugin are not offered, and a `?agents=`
   preselection naming one is dropped, so it is never submitted. */
import { describe, it, expect, afterEach, vi } from "vitest";
import { renderHookWithProviders, cleanup, act, waitFor } from "@/test/render";
import { mockFetch } from "@/test/fetch-mock";
import { AGENT } from "@/test/skill-fixtures";

const nav = vi.hoisted(() => ({ push: vi.fn() }));
vi.mock("next/navigation", () => ({ useRouter: () => nav }));
vi.mock("@/lib/repo-context", () => ({ useActiveRepo: () => ({ repoId: "r1", reposLoaded: true }) }));

import { useConfigureRun } from "./useConfigureRun";

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

const OFF = { id: "p2", name: "beta", enabled: false };
const ON = { id: "p1", name: "acme", enabled: true };
const UNOWNED = { ...AGENT, id: "own", name: "Unowned" };
const OF_ENABLED = { ...AGENT, id: "on", name: "Of enabled plugin", plugin: ON };
const OF_DISABLED = { ...AGENT, id: "off", name: "Of disabled plugin", plugin: OFF };

function stub() {
  return mockFetch({
    "GET /agents": [UNOWNED, OF_ENABLED, OF_DISABLED],
    "GET /repos/r1/pulls": [{ id: "pr1" }],
    "GET /runs/estimates": [],
    "POST /pulls/pr1/review": { runs: [], multi_agent_run_id: "m1" },
  });
}

describe("useConfigureRun plugin filter", () => {
  it("does not offer an agent of a disabled plugin, by default or after select all, and submits only the others", async () => {
    const api = stub();
    const { result } = renderHookWithProviders(() => useConfigureRun({ prId: "pr1", agentIds: null }));

    await waitFor(() => expect(result.current.agents.length).toBeGreaterThan(0));
    expect(result.current.agents.map((a) => a.id)).toEqual(["own", "on"]);
    await waitFor(() => expect(result.current.selectedPr).toBe("pr1"));
    act(() => result.current.selectAll());
    expect(result.current.selectedIds).toEqual(["own", "on"]);

    act(() => result.current.start());
    await waitFor(() => expect(api.requests("POST", "/pulls/pr1/review")).toHaveLength(1));
    expect(api.requests("POST", "/pulls/pr1/review")[0]?.body).toEqual({ agentIds: ["own", "on"] });
  });

  it("drops a ?agents= preselection that names an agent of a disabled plugin", async () => {
    const api = stub();
    const { result } = renderHookWithProviders(() => useConfigureRun({ prId: "pr1", agentIds: ["off", "on", "own"] }));

    await waitFor(() => expect(result.current.agents.length).toBeGreaterThan(0));
    await waitFor(() => expect(result.current.selectedPr).toBe("pr1"));
    expect(result.current.selectedIds).toEqual(["own", "on"]);

    act(() => result.current.start());
    await waitFor(() => expect(api.requests("POST", "/pulls/pr1/review")).toHaveLength(1));
    expect(api.requests("POST", "/pulls/pr1/review")[0]?.body).toEqual({ agentIds: ["own", "on"] });
  });
});

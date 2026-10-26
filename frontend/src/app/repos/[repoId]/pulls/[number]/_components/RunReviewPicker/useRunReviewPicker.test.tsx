/* useRunReviewPicker — FR18 / AC21: agents of a disabled plugin are not offered and never submitted. */
import { describe, it, expect, afterEach, vi } from "vitest";
import { renderHookWithProviders, cleanup, act, waitFor } from "@/test/render";
import { mockFetch } from "@/test/fetch-mock";
import { AGENT } from "@/test/skill-fixtures";

const nav = vi.hoisted(() => ({ push: vi.fn() }));
vi.mock("next/navigation", () => ({ useRouter: () => nav }));

import { useRunReviewPicker } from "./useRunReviewPicker";

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

const OFF = { id: "p2", name: "beta", enabled: false };
const ON = { id: "p1", name: "acme", enabled: true };
const UNOWNED = { ...AGENT, id: "own", name: "Unowned" };
const OF_ENABLED = { ...AGENT, id: "on", name: "Of enabled plugin", plugin: ON };
const OF_DISABLED = { ...AGENT, id: "off", name: "Of disabled plugin", plugin: OFF };
const OWN_DISABLED = { ...AGENT, id: "ownoff", name: "Own disabled", enabled: false };

describe("useRunReviewPicker plugin filter", () => {
  it("offers unowned and enabled-plugin agents only, and submits no agent of a disabled plugin", async () => {
    const api = mockFetch({
      "GET /agents": [UNOWNED, OF_ENABLED, OF_DISABLED, OWN_DISABLED],
      "POST /pulls/pr1/review": { runs: [{ run_id: "r1" }, { run_id: "r2" }], multi_agent_run_id: "m1" },
    });
    const { result } = renderHookWithProviders(() => useRunReviewPicker({ prId: "pr1" }));

    await waitFor(() => expect(result.current.agents.length).toBeGreaterThan(0));
    // own-enabled behaviour is unchanged: the own-disabled agent is still listed, just not preselected
    expect(result.current.agents.map((a) => a.id)).toEqual(["own", "on", "ownoff"]);
    expect(result.current.selectedIds).toEqual(["own", "on"]);

    // select all / toggling the disabled-plugin id cannot bring it back
    act(() => result.current.selectAll());
    act(() => result.current.toggle("off"));
    expect(result.current.selectedIds).toEqual(["own", "on", "ownoff"]);

    act(() => result.current.start());
    await waitFor(() => expect(api.requests("POST", "/pulls/pr1/review")).toHaveLength(1));
    expect(api.requests("POST", "/pulls/pr1/review")[0]?.body).toEqual({ agentIds: ["own", "on", "ownoff"] });
  });
});

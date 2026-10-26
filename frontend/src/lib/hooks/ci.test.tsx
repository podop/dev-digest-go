import { describe, it, expect, afterEach, vi } from "vitest";
import { renderHookWithProviders, cleanup, act, waitFor, createTestQueryClient } from "@/test/render";
import { mockFetch } from "@/test/fetch-mock";
import { ApiError } from "../api";
import { isQuietError } from "../query-meta";
import { useInstallCi, useWorkspaceCiInstallations } from "./ci";
import { ciKeys } from "./keys";

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

const REPOS = {
  repos: [
    {
      repo: "o/a",
      installations: [{ id: "i1", agent_id: "a1", agent_name: "A", slug: null, installed_at: "2026-10-26T10:00:00.000Z" }],
      last_run: null,
    },
  ],
};

describe("ci hooks of Settings › Integrations", () => {
  it("useWorkspaceCiInstallations reads GET /ci/installations and returns the repositories", async () => {
    const http = mockFetch({ "GET /ci/installations": REPOS });
    const { result } = renderHookWithProviders(() => useWorkspaceCiInstallations());

    await waitFor(() => expect(result.current.data).toEqual(REPOS.repos));
    expect(http.requests("GET", "/ci/installations")).toHaveLength(1);
  });

  it("useInstallCi opens the PR for the agent in the variables, refetches both installation lists and shows no global toast", async () => {
    const http = mockFetch({
      "POST /agents/:id/export-ci": { installation: null, files: [], pr_url: "https://github.com/o/a/pull/1" },
    });
    const qc = createTestQueryClient();
    const spy = vi.spyOn(qc, "invalidateQueries");
    const { result } = renderHookWithProviders(() => useInstallCi(), { queryClient: qc });

    await act(() => result.current.mutateAsync({ agentId: "a1", repo: "o/a" }));
    await act(() => result.current.mutateAsync({ agentId: "a2", repo: "o/a", runner: "self_hosted" }));

    expect(http.requests("POST", "/agents/a1/export-ci")[0]?.body).toEqual({ repo: "o/a", target: "gha", action: "open_pr" });
    expect(http.requests("POST", "/agents/a2/export-ci")[0]?.body).toEqual({
      repo: "o/a",
      target: "gha",
      action: "open_pr",
      runner: "self_hosted",
    });
    const keys = spy.mock.calls.map((c) => JSON.stringify(c[0]?.queryKey));
    expect(keys).toContain(JSON.stringify(ciKeys.installationsAll()));
    expect(keys).toContain(JSON.stringify(ciKeys.repoInstallations()));
    const meta = qc.getMutationCache().getAll().at(-1)?.meta;
    expect(isQuietError(new ApiError("x", 422, "workflow_insecure"), meta)).toBe(true);
  });
});

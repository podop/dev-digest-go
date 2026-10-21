import { describe, it, expect, afterEach } from "vitest";
import type { ContextAttachments } from "@devdigest/shared";
import { renderHookWithProviders, cleanup, act, waitFor } from "@/test/render";
import { mockFetch, jsonResponse } from "@/test/fetch-mock";
import { agentKeys } from "./keys";
import { useSetAgentContext } from "./project-context";

afterEach(cleanup);

const saved = (paths: string[]): ContextAttachments => ({ repo_id: "r1", paths });

describe("useSetAgentContext", () => {
  it("sends quick saves one after another and ends on the last list", async () => {
    let inFlight = 0;
    let maxInFlight = 0;
    const api = mockFetch({
      "PUT /agents/ag1/context": async (req) => {
        inFlight++;
        maxInFlight = Math.max(maxInFlight, inFlight);
        await new Promise((r) => setTimeout(r, 20));
        inFlight--;
        return req.body;
      },
      "GET /agents/ag1/context": saved(["stale.md"]),
    });
    const { result, queryClient } = renderHookWithProviders(() => useSetAgentContext("ag1", "r1"));
    const key = agentKeys.context("ag1", "r1");
    queryClient.setQueryData(key, saved([]));

    act(() => {
      result.current.mutate(["a.md"]);
      result.current.mutate(["a.md", "b.md"]);
    });
    await waitFor(() => expect(queryClient.getQueryData<ContextAttachments>(key)?.paths).toEqual(["a.md", "b.md"]));
    await waitFor(() => expect(api.requests("PUT")).toHaveLength(2));
    await waitFor(() => expect(inFlight).toBe(0));
    expect(queryClient.getQueryData<ContextAttachments>(key)?.paths).toEqual(["a.md", "b.md"]);

    expect(maxInFlight).toBe(1);
    expect(api.requests("PUT").map((r) => (r.body as ContextAttachments).paths)).toEqual([["a.md"], ["a.md", "b.md"]]);
    expect(api.requests("PUT")[0]?.body).toMatchObject({ repo_id: "r1" });
  });

  it("rolls the list back when the PUT fails", async () => {
    mockFetch({ "PUT /agents/ag1/context": jsonResponse({ error: { code: "internal", message: "boom" } }, 500) });
    const { result, queryClient } = renderHookWithProviders(() => useSetAgentContext("ag1", "r1"));
    const key = agentKeys.context("ag1", "r1");
    queryClient.setQueryData(key, saved(["a.md"]));

    act(() => result.current.mutate(["a.md", "b.md"]));
    await waitFor(() => expect(result.current.isError).toBe(true));
    expect(queryClient.getQueryData<ContextAttachments>(key)?.paths).toEqual(["a.md"]);
  });
});

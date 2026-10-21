import { describe, it, expect, afterEach } from "vitest";
import type { PrBrief, PrBriefResponse } from "@devdigest/shared";
import { renderHookWithProviders, cleanup, act, waitFor } from "@/test/render";
import { mockFetch, jsonResponse } from "@/test/fetch-mock";
import { prKeys } from "./keys";
import { useGenerateBrief } from "./brief";

afterEach(cleanup);

const brief = (summary: string) => ({ summary, head_sha: "abc" }) as unknown as PrBrief;

describe("useGenerateBrief", () => {
  it("POSTs without a body and writes the answer into the brief query", async () => {
    const fresh: PrBriefResponse = { brief: brief("fresh"), stale: false };
    const api = mockFetch({ "POST /pulls/pr-1/brief": fresh });
    const { result, queryClient } = renderHookWithProviders(() => useGenerateBrief("pr-1"));
    queryClient.setQueryData(prKeys.brief("pr-1"), { brief: null, stale: false });

    act(() => result.current.mutate());
    await waitFor(() => expect(result.current.isSuccess).toBe(true));

    expect(api.requests("POST")[0]?.body).toBeUndefined();
    expect(queryClient.getQueryData(prKeys.brief("pr-1"))).toEqual(fresh);
  });

  it("keeps the previous brief when generation fails", async () => {
    mockFetch({ "POST /pulls/pr-1/brief": jsonResponse({ error: { code: "generation_failed", message: "x" } }, 502) });
    const { result, queryClient } = renderHookWithProviders(() => useGenerateBrief("pr-1"));
    const previous: PrBriefResponse = { brief: brief("old"), stale: false };
    queryClient.setQueryData(prKeys.brief("pr-1"), previous);

    act(() => result.current.mutate());
    await waitFor(() => expect(result.current.isError).toBe(true));
    expect(queryClient.getQueryData(prKeys.brief("pr-1"))).toEqual(previous);
  });
});

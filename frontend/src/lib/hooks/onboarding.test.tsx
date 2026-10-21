import { describe, it, expect, afterEach } from "vitest";
import type { OnboardingTourState } from "@devdigest/shared";
import { renderHookWithProviders, cleanup, act, waitFor } from "@/test/render";
import { mockFetch, jsonResponse } from "@/test/fetch-mock";
import { repoKeys } from "./keys";
import { useGenerateOnboardingTour } from "./onboarding";

afterEach(cleanup);

describe("useGenerateOnboardingTour", () => {
  it("POSTs without a body and writes the answer into the tour query", async () => {
    const ready = { status: "ready", stale: false, tour: { repo_id: "r1" } } as unknown as OnboardingTourState;
    const api = mockFetch({ "POST /repos/r1/onboarding": ready });
    const { result, queryClient } = renderHookWithProviders(() => useGenerateOnboardingTour("r1"));
    queryClient.setQueryData(repoKeys.onboarding("r1"), { status: "none" });

    act(() => result.current.mutate());
    await waitFor(() => expect(result.current.isSuccess).toBe(true));

    expect(api.requests("POST")[0]?.body).toBeUndefined();
    expect(queryClient.getQueryData(repoKeys.onboarding("r1"))).toEqual(ready);
  });

  it("keeps the previous tour when generation fails", async () => {
    mockFetch({ "POST /repos/r1/onboarding": jsonResponse({ error: { code: "generation_failed", message: "x" } }, 502) });
    const { result, queryClient } = renderHookWithProviders(() => useGenerateOnboardingTour("r1"));
    const previous = { status: "none" };
    queryClient.setQueryData(repoKeys.onboarding("r1"), previous);

    act(() => result.current.mutate());
    await waitFor(() => expect(result.current.isError).toBe(true));
    expect(queryClient.getQueryData(repoKeys.onboarding("r1"))).toEqual(previous);
  });
});

import { describe, it, expect, afterEach } from "vitest";
import type { QueryClient, QueryKey } from "@tanstack/react-query";
import { renderHookWithProviders, cleanup, act, waitFor, createTestQueryClient } from "@/test/render";
import { jsonResponse, mockFetch } from "@/test/fetch-mock";
import { ApiError } from "../api";
import { isQuietError } from "../query-meta";
import { agentKeys, customProviderKeys, providerKeys, settingsKeys } from "./keys";
import { useTestConnection } from "./core";
import { useProviderModels } from "./agents";
import { useCustomProviders, useDeleteCustomProvider, useSaveCustomProvider } from "./custom-providers";

afterEach(cleanup);

const invalidated = (qc: QueryClient, key: QueryKey) => qc.getQueryState(key)?.isInvalidated ?? false;

function seed(entries: QueryKey[]): QueryClient {
  const qc = createTestQueryClient();
  for (const key of entries) qc.setQueryData(key, { seeded: true });
  return qc;
}

const PROVIDER = {
  id: "custom:synapse",
  name: "Synapse",
  protocol: "openai",
  base_url: "https://gw.example.com/v1",
  plain_http: false,
  auth_mode: "bearer",
  auth_header: null,
  structured_output: "strict",
  key_configured: true,
  extra_headers: [],
  models: [],
  created_at: "2026-10-26T10:00:00.000Z",
  updated_at: "2026-10-26T10:00:00.000Z",
};

const quiet = (qc: QueryClient, code: string) =>
  isQuietError(new ApiError("x", 422, code), qc.getMutationCache().getAll().at(-1)?.meta);

describe("custom provider hooks", () => {
  it("useCustomProviders reads GET /settings/custom-providers", async () => {
    const http = mockFetch({ "GET /settings/custom-providers": [PROVIDER] });
    const { result } = renderHookWithProviders(() => useCustomProviders());

    await waitFor(() => expect(result.current.data).toEqual([PROVIDER]));
    expect(http.requests("GET", "/settings/custom-providers")).toHaveLength(1);
  });

  it("useSaveCustomProvider POSTs a new provider, PUTs an existing one under its encoded id, then refetches lists", async () => {
    const http = mockFetch({
      "POST /settings/custom-providers": () => jsonResponse(PROVIDER, 201),
      "PUT /settings/custom-providers/:id": PROVIDER,
    });
    const qc = seed([customProviderKeys.list(), providerKeys.models("custom:synapse"), providerKeys.models("openai"), agentKeys.list()]);
    const { result } = renderHookWithProviders(() => useSaveCustomProvider(), { queryClient: qc });
    const input = { id: "custom:synapse" as const, name: "Synapse", protocol: "openai" as const, base_url: "https://gw.example.com/v1", api_key: "sk-secret" };

    await act(() => result.current.mutateAsync({ mode: "create", input }));
    const { id: _id, ...update } = input;
    await act(() => result.current.mutateAsync({ mode: "update", id: "custom:synapse", input: update }));

    expect(http.requests("POST", "/settings/custom-providers")[0]?.body).toEqual(input);
    const put = http.requests("PUT").at(0);
    expect(put?.path).toBe("/settings/custom-providers/custom%3Asynapse");
    expect(put?.body).toEqual(update);
    expect(invalidated(qc, customProviderKeys.list())).toBe(true);
    expect(invalidated(qc, providerKeys.models("custom:synapse"))).toBe(true);
    expect(invalidated(qc, providerKeys.models("openai"))).toBe(true);
    expect(invalidated(qc, agentKeys.list())).toBe(false);
    for (const code of ["validation_error", "custom_provider_limit", "custom_provider_exists"]) expect(quiet(qc, code)).toBe(true);
    expect(quiet(qc, "custom_provider_not_found")).toBe(false);
  });

  it("useDeleteCustomProvider deletes by encoded id, drops its models and refetches agents and settings; provider_in_use stays quiet", async () => {
    const http = mockFetch({ "DELETE /settings/custom-providers/:id": { deleted: "custom:synapse" } });
    const qc = seed([customProviderKeys.list(), providerKeys.models("custom:synapse"), providerKeys.models("openai"), agentKeys.list(), settingsKeys.current()]);
    const { result } = renderHookWithProviders(() => useDeleteCustomProvider(), { queryClient: qc });

    await act(() => result.current.mutateAsync("custom:synapse"));

    expect(http.requests("DELETE")[0]?.path).toBe("/settings/custom-providers/custom%3Asynapse");
    expect(invalidated(qc, customProviderKeys.list())).toBe(true);
    expect(qc.getQueryState(providerKeys.models("custom:synapse"))).toBeUndefined();
    expect(qc.getQueryState(providerKeys.models("openai"))).toBeDefined();
    expect(invalidated(qc, agentKeys.list())).toBe(true);
    expect(invalidated(qc, settingsKeys.current())).toBe(true);
    expect(quiet(qc, "provider_in_use")).toBe(true);
    expect(quiet(qc, "validation_error")).toBe(false);
  });

  it("the conflict answer reaches the caller with its details", async () => {
    const details = { agents: [{ id: "a1", name: "Reviewer" }], feature_models: ["risk_brief"], default_model: true };
    mockFetch({
      "DELETE /settings/custom-providers/:id": jsonResponse({ error: { code: "provider_in_use", message: "in use", details } }, 409),
    });
    const { result } = renderHookWithProviders(() => useDeleteCustomProvider());

    let err: unknown;
    await act(async () => {
      err = await result.current.mutateAsync("custom:synapse").catch((e: unknown) => e);
    });

    expect(err).toBeInstanceOf(ApiError);
    expect((err as ApiError).code).toBe("provider_in_use");
    expect((err as ApiError).details).toEqual(details);
  });
});

describe("custom ids in existing hooks", () => {
  it("useProviderModels encodes the id in the path", async () => {
    const http = mockFetch({ "GET /providers/:id/models": [] });
    renderHookWithProviders(() => useProviderModels("custom:synapse"));

    await waitFor(() => expect(http.requests("GET")).toHaveLength(1));
    expect(http.requests("GET")[0]?.path).toBe("/providers/custom%3Asynapse/models");
  });

  it("testing a custom provider sends its id and refetches the custom list even when the test fails", async () => {
    const http = mockFetch({ "POST /settings/test-connection": { provider: "custom:synapse", ok: false, message: "401" } });
    const qc = seed([customProviderKeys.list(), providerKeys.models("openai")]);
    const { result } = renderHookWithProviders(() => useTestConnection(), { queryClient: qc });

    await act(() => result.current.mutateAsync({ provider: "custom:synapse", key: "sk-new" }));

    expect(http.requests("POST", "/settings/test-connection")[0]?.body).toEqual({ provider: "custom:synapse", key: "sk-new" });
    expect(invalidated(qc, customProviderKeys.list())).toBe(true);
    expect(invalidated(qc, providerKeys.models("openai"))).toBe(false);
  });
});

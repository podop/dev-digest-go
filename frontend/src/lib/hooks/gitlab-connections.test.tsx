import { describe, it, expect, afterEach } from "vitest";
import type { QueryClient, QueryKey } from "@tanstack/react-query";
import { renderHookWithProviders, cleanup, act, waitFor, createTestQueryClient } from "@/test/render";
import { jsonResponse, mockFetch } from "@/test/fetch-mock";
import { ApiError } from "../api";
import { isQuietError } from "../query-meta";
import { gitlabConnectionKeys, providerKeys, repoKeys, settingsKeys } from "./keys";
import { useAddRepo, useTestConnection } from "./core";
import { useDeleteGitLabConnection, useGitLabConnections, useSaveGitLabConnection } from "./gitlab-connections";

afterEach(cleanup);

const invalidated = (qc: QueryClient, key: QueryKey) => qc.getQueryState(key)?.isInvalidated ?? false;

function seed(entries: QueryKey[]): QueryClient {
  const qc = createTestQueryClient();
  for (const key of entries) qc.setQueryData(key, { seeded: true });
  return qc;
}

const CONN = {
  id: "c1",
  name: "Corp",
  base_url: "https://git.corp.example",
  host: "git.corp.example",
  token_configured: true,
  repo_count: 1,
  created_at: "2026-10-26T10:00:00.000Z",
  updated_at: "2026-10-26T10:00:00.000Z",
};

const quiet = (qc: QueryClient, code: string) =>
  isQuietError(new ApiError("x", 422, code), qc.getMutationCache().getAll().at(-1)?.meta);

describe("GitLab connection hooks", () => {
  it("useGitLabConnections reads GET /settings/gitlab-connections", async () => {
    const http = mockFetch({ "GET /settings/gitlab-connections": [CONN] });
    const { result } = renderHookWithProviders(() => useGitLabConnections());

    await waitFor(() => expect(result.current.data).toEqual([CONN]));
    expect(http.requests("GET", "/settings/gitlab-connections")).toHaveLength(1);
  });

  it("useSaveGitLabConnection POSTs, PUTs under the encoded id (null token = remove), then refetches connections, repos and the auto-review status", async () => {
    const http = mockFetch({
      "POST /settings/gitlab-connections": () => jsonResponse(CONN, 201),
      "PUT /settings/gitlab-connections/:id": CONN,
    });
    const qc = seed([gitlabConnectionKeys.list(), repoKeys.list(), repoKeys.pulls("r1"), settingsKeys.autoReviewsStatus(), settingsKeys.current()]);
    const { result } = renderHookWithProviders(() => useSaveGitLabConnection(), { queryClient: qc });
    const input = { name: "Corp", base_url: "https://git.corp.example", token: "glpat-secret" };

    await act(() => result.current.mutateAsync({ mode: "create", input }));
    await act(() => result.current.mutateAsync({ mode: "update", id: "c 1", input: { token: null } }));

    expect(http.requests("POST", "/settings/gitlab-connections")[0]?.body).toEqual(input);
    const put = http.requests("PUT")[0];
    expect(put?.path).toBe("/settings/gitlab-connections/c%201");
    expect(put?.body).toEqual({ token: null });
    expect(invalidated(qc, gitlabConnectionKeys.list())).toBe(true);
    expect(invalidated(qc, repoKeys.list())).toBe(true);
    expect(invalidated(qc, repoKeys.pulls("r1"))).toBe(true);
    expect(invalidated(qc, settingsKeys.autoReviewsStatus())).toBe(true);
    expect(invalidated(qc, settingsKeys.current())).toBe(false);
    for (const code of ["validation_error", "gitlab_connection_exists", "gitlab_connection_limit"]) expect(quiet(qc, code)).toBe(true);
    expect(quiet(qc, "gitlab_connection_not_found")).toBe(false);
  });

  it("useDeleteGitLabConnection reports how many repositories were disconnected and refetches the same reads", async () => {
    const http = mockFetch({ "DELETE /settings/gitlab-connections/:id": { deleted: "c1", disconnected_repos: 3 } });
    const qc = seed([gitlabConnectionKeys.list(), repoKeys.list(), settingsKeys.autoReviewsStatus()]);
    const { result } = renderHookWithProviders(() => useDeleteGitLabConnection(), { queryClient: qc });

    let deleted: unknown;
    await act(async () => {
      deleted = await result.current.mutateAsync("c1");
    });

    expect(deleted).toEqual({ deleted: "c1", disconnected_repos: 3 });
    expect(http.requests("DELETE")[0]?.path).toBe("/settings/gitlab-connections/c1");
    expect(invalidated(qc, gitlabConnectionKeys.list())).toBe(true);
    expect(invalidated(qc, repoKeys.list())).toBe(true);
    expect(invalidated(qc, settingsKeys.autoReviewsStatus())).toBe(true);
    expect(quiet(qc, "gitlab_connection_not_found")).toBe(true);
    expect(quiet(qc, "internal_error")).toBe(false);
  });

  it("a conflict answer reaches the caller with its code", async () => {
    mockFetch({
      "POST /settings/gitlab-connections": jsonResponse(
        { error: { code: "gitlab_connection_exists", message: "A GitLab connection for git.corp.example already exists" } },
        409,
      ),
    });
    const { result } = renderHookWithProviders(() => useSaveGitLabConnection());

    let err: unknown;
    await act(async () => {
      err = await result.current.mutateAsync({ mode: "create", input: { base_url: "https://git.corp.example" } }).catch((e: unknown) => e);
    });

    expect(err).toBeInstanceOf(ApiError);
    expect((err as ApiError).code).toBe("gitlab_connection_exists");
  });
});

describe("GitLab in existing hooks", () => {
  it("testing a connection sends connection_id and refetches the connection list even when the test fails, but no LLM reads", async () => {
    const http = mockFetch({ "POST /settings/test-connection": { provider: "gitlab", ok: false, message: "GitLab rejected the token (401)" } });
    const qc = seed([gitlabConnectionKeys.list(), providerKeys.models("openai"), settingsKeys.secretsStatus()]);
    const { result } = renderHookWithProviders(() => useTestConnection(), { queryClient: qc });

    await act(() => result.current.mutateAsync({ provider: "gitlab", connection_id: "c1", key: "glpat-new" }));

    expect(http.requests("POST", "/settings/test-connection")[0]?.body).toEqual({ provider: "gitlab", connection_id: "c1", key: "glpat-new" });
    expect(invalidated(qc, gitlabConnectionKeys.list())).toBe(true);
    expect(invalidated(qc, providerKeys.models("openai"))).toBe(false);
    expect(invalidated(qc, settingsKeys.secretsStatus())).toBe(false);
  });

  it("useAddRepo leaves every error to the add form (no extra toast) and refetches nothing on failure", async () => {
    mockFetch({ "POST /repos": jsonResponse({ error: { code: "repo_host_not_connected", message: "No GitLab connection for git.corp.example." } }, 422) });
    const qc = seed([repoKeys.list()]);
    const { result } = renderHookWithProviders(() => useAddRepo(), { queryClient: qc });

    let err: unknown;
    await act(async () => {
      err = await result.current.mutateAsync("https://git.corp.example/g/p").catch((e: unknown) => e);
    });

    expect((err as ApiError).code).toBe("repo_host_not_connected");
    expect(quiet(qc, "repo_host_not_connected")).toBe(true);
    expect(quiet(qc, "network_error")).toBe(true);
    expect(invalidated(qc, repoKeys.list())).toBe(false);
  });
});

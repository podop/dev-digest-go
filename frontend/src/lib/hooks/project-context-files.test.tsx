import { describe, it, expect, afterEach } from "vitest";
import type { ContextAttachments, ContextDocPreview } from "@devdigest/shared";
import { renderHookWithProviders, cleanup, act, waitFor } from "@/test/render";
import { mockFetch } from "@/test/fetch-mock";
import { ApiError } from "../api";
import { agentKeys, repoKeys, skillKeys } from "./keys";
import {
  useAgentContext,
  useContextDocs,
  useCreateContextFile,
  useDeleteContextFile,
  useRenameContextFile,
  useSaveContextFile,
  useSkillContext,
} from "./project-context";

afterEach(cleanup);

const OLD = ".devdigest/specs/a.md";
const NEW = ".devdigest/specs/b.md";

const preview = (path: string, version: number): ContextDocPreview =>
  ({ path, source: "store", editable: true, version, content: "x" }) as unknown as ContextDocPreview;

describe("store-file hooks", () => {
  it("a rename refetches the repo list, the agent context and the skill context", async () => {
    let agentPaths = [OLD];
    const api = mockFetch({
      "POST /repos/r1/context/files/rename": () => {
        agentPaths = [NEW];
        return preview(NEW, 2);
      },
      "GET /repos/r1/context": { repo_id: "r1", docs: [] },
      "GET /agents/ag1/context": () => ({ repo_id: "r1", paths: agentPaths }) satisfies ContextAttachments,
      "GET /skills/sk1/context": () => ({ repo_id: "r1", paths: agentPaths }) satisfies ContextAttachments,
    });
    const { result, queryClient } = renderHookWithProviders(() => ({
      list: useContextDocs("r1"),
      agent: useAgentContext("ag1", "r1"),
      skill: useSkillContext("sk1", "r1"),
      rename: useRenameContextFile("r1"),
    }));
    await waitFor(() => expect(result.current.agent.data?.paths).toEqual([OLD]));
    await waitFor(() => expect(result.current.skill.data?.paths).toEqual([OLD]));
    await waitFor(() => expect(result.current.list.isSuccess).toBe(true));

    await act(() => result.current.rename.mutateAsync({ path: OLD, new_path: NEW, base_version: 1 }));

    await waitFor(() => expect(queryClient.getQueryData<ContextAttachments>(agentKeys.context("ag1", "r1"))?.paths).toEqual([NEW]));
    await waitFor(() => expect(queryClient.getQueryData<ContextAttachments>(skillKeys.context("sk1", "r1"))?.paths).toEqual([NEW]));
    expect(api.requests("POST")[0]?.body).toEqual({ path: OLD, new_path: NEW, base_version: 1 });
    expect(api.requests("GET", "/agents/ag1/context")).toHaveLength(2);
    expect(api.requests("GET", "/repos/r1/context")).toHaveLength(2);
  });

  it("save puts the content with its base version and caches the new preview; a stale save is an ApiError", async () => {
    let stale = false;
    const api = mockFetch({
      "PUT /repos/r1/context/files": () =>
        stale
          ? new Response(JSON.stringify({ error: { code: "stale_version", message: "stale", details: { current_version: 3 } } }), {
              status: 409,
              headers: { "content-type": "application/json" },
            })
          : preview(OLD, 2),
    });
    const { result, queryClient } = renderHookWithProviders(() => useSaveContextFile("r1"));

    await act(() => result.current.mutateAsync({ path: OLD, content: "hi", base_version: 1 }));
    expect(api.requests("PUT")[0]?.search).toBe(`?path=${encodeURIComponent(OLD)}`);
    expect(api.requests("PUT")[0]?.body).toEqual({ content: "hi", base_version: 1 });
    expect(queryClient.getQueryData<ContextDocPreview>(repoKeys.contextDoc("r1", OLD))?.version).toBe(2);

    stale = true;
    let err: unknown;
    await act(async () => {
      err = await result.current.mutateAsync({ path: OLD, content: "hi", base_version: 1 }).catch((e: unknown) => e);
    });
    expect(err).toBeInstanceOf(ApiError);
    expect((err as ApiError).code).toBe("stale_version");
  });

  it("create posts an optional body and delete drops the cached preview", async () => {
    const api = mockFetch({
      "POST /repos/r1/context/files": () => preview(".devdigest/specs/untitled.md", 1),
      "DELETE /repos/r1/context/files": () => new Response(null, { status: 204 }),
    });
    const { result, queryClient } = renderHookWithProviders(() => ({
      create: useCreateContextFile("r1"),
      del: useDeleteContextFile("r1"),
    }));
    await act(() => result.current.create.mutateAsync({}));
    expect(api.requests("POST")).toHaveLength(1);

    queryClient.setQueryData(repoKeys.contextDoc("r1", OLD), preview(OLD, 1));
    await act(() => result.current.del.mutateAsync(OLD));
    expect(queryClient.getQueryData(repoKeys.contextDoc("r1", OLD))).toBeUndefined();
    expect(api.requests("DELETE")[0]?.search).toBe(`?path=${encodeURIComponent(OLD)}`);
  });
});

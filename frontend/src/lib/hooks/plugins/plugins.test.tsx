import { describe, it, expect, afterEach, onTestFinished, vi } from "vitest";
import type { QueryClient } from "@tanstack/react-query";
import { renderHookWithProviders, cleanup, act, waitFor, createTestQueryClient } from "@/test/render";
import { mockFetch } from "@/test/fetch-mock";
import { ApiError } from "../../api";
import { isQuietError } from "../../query-meta";
import { agentKeys, ciKeys, evalKeys, pluginKeys, skillKeys, workspaceKeys } from "../keys";
import {
  PLUGIN_EXPORT_FALLBACK_FILE_NAME,
  useExportPlugin,
  useInstallPlugin,
  usePlugins,
  usePreviewPlugin,
  useRemovePlugin,
  useSetPluginEnabled,
} from "./index";

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

const COUNTS = { agents: 1, skills: 2, eval_cases: 3, learnings: 4 };
const ROW = {
  id: "11111111-1111-4111-8111-111111111111",
  name: "acme",
  version: "1.0.0",
  source: "acme.zip",
  installed_at: "2026-10-26T10:00:00.000Z",
  enabled: false,
  description: "d",
  counts: COUNTS,
};
const LIST = {
  plugins: [ROW],
  last_export: { file_name: "acme-2026-10-26.zip", exported_at: "2026-10-26T10:00:00.000Z", counts: COUNTS },
};
const PREVIEW = {
  manifest: {
    name: "acme",
    version: "1.0.0",
    format: "devdigest-plugin/v1",
    exported_at: "2026-10-26T10:00:00.000Z",
    counts: { agents: 1, skills: 2, eval_cases: 3, conventions: 0 },
  },
  kind: "plugin",
  counts: { agents: 1, skills: 2, eval_cases: 3, conventions: 0, learnings: 4 },
  agents: [],
  skills: [],
  conflicts: { plugin: false, skills: [] },
  ignored: [],
  warnings: [],
};

function invalidated(qc: QueryClient) {
  const spy = vi.spyOn(qc, "invalidateQueries");
  return () => spy.mock.calls.map((c) => JSON.stringify(c[0]?.queryKey));
}

/** The `meta` the last mutation of the client was created with, as the global error toast sees it. */
function quiet(qc: QueryClient, code: string | undefined, status = 422): boolean {
  const meta = qc.getMutationCache().getAll().at(-1)?.meta;
  return isQuietError(new ApiError("m", status, code), meta);
}

function initOf(http: ReturnType<typeof mockFetch>, index = 0) {
  const init = http.fetch.mock.calls[index]?.[1] as RequestInit;
  return { init, headers: new Headers(init.headers) };
}

describe("plugin hooks", () => {
  it("usePlugins reads the rows and the last export", async () => {
    const http = mockFetch({ "GET /plugins": LIST });
    const { result } = renderHookWithProviders(() => usePlugins());

    await waitFor(() => expect(result.current.data).toEqual(LIST));
    expect(http.requests("GET", "/plugins")).toHaveLength(1);
  });

  it("export POSTs the selection as JSON, saves the zip under the server's name and refetches the list", async () => {
    const http = mockFetch({
      "POST /plugins/export": new Response("zip", {
        headers: {
          "content-type": "application/zip",
          "content-disposition": 'attachment; filename="acme-2026-10-26.zip"',
        },
      }),
      "GET /plugins": LIST,
    });
    URL.createObjectURL = () => "blob:test";
    URL.revokeObjectURL = () => {};
    onTestFinished(() => {
      Reflect.deleteProperty(URL, "createObjectURL");
      Reflect.deleteProperty(URL, "revokeObjectURL");
    });
    const names: string[] = [];
    vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(function (this: HTMLAnchorElement) {
      names.push(this.download);
    });
    const qc = createTestQueryClient();
    const keys = invalidated(qc);
    const { result } = renderHookWithProviders(() => useExportPlugin(), { queryClient: qc });

    const body = { name: "acme", agent_ids: [ROW.id], include_eval_cases: false, include_learnings: true };
    const out = await act(() => result.current.mutateAsync(body));

    expect(out).toEqual({ fileName: "acme-2026-10-26.zip", bytes: 3 });
    expect(names).toEqual(["acme-2026-10-26.zip"]);
    expect(http.requests("POST", "/plugins/export")[0]?.body).toEqual(body);
    expect(initOf(http).headers.get("content-type")).toBe("application/json");
    expect(keys()).toContain(JSON.stringify(pluginKeys.list()));

    http.on("POST /plugins/export", new Response("zip"));
    const again = await act(() => result.current.mutateAsync({}));
    expect(again.fileName).toBe(PLUGIN_EXPORT_FALLBACK_FILE_NAME);
  });

  it("export shows every failure in its dialog, so nothing is toasted", async () => {
    mockFetch({
      "POST /plugins/export": new Response(JSON.stringify({ error: { code: "nothing_to_export", message: "nothing" } }), {
        status: 422,
      }),
    });
    const qc = createTestQueryClient();
    const { result } = renderHookWithProviders(() => useExportPlugin(), { queryClient: qc });
    await act(async () => {
      await expect(result.current.mutateAsync({})).rejects.toMatchObject({ status: 422, code: "nothing_to_export" });
    });

    // The export dialog shows every failure itself (4xx message, 5xx "Export failed — try again").
    for (const code of ["nothing_to_export", "export_too_large", "validation_error", "not_found"]) {
      expect(quiet(qc, code), code).toBe(true);
    }
    expect(quiet(qc, "internal_error", 500)).toBe(true);
  });

  it("preview uploads the zip raw with dry_run=true and the file name, and stores nothing in the lists", async () => {
    const http = mockFetch({ "POST /plugins/import": PREVIEW });
    const qc = createTestQueryClient();
    const keys = invalidated(qc);
    const { result } = renderHookWithProviders(() => usePreviewPlugin(), { queryClient: qc });
    const file = new File(["PK"], "my plugin.zip", { type: "application/zip" });

    const out = await act(() => result.current.mutateAsync({ file }));

    expect(out).toEqual(PREVIEW);
    const req = http.requests("POST", "/plugins/import")[0];
    expect(new URLSearchParams(req?.search).get("dry_run")).toBe("true");
    expect(new URLSearchParams(req?.search).get("filename")).toBe("my plugin.zip");
    const { init, headers } = initOf(http);
    expect(init.body).toBe(file);
    expect(headers.get("content-type")).toBe("application/zip");
    expect(keys()).toEqual([]);
  });

  it("a .json bundle is sent as application/json, and any other file is refused before a request", async () => {
    const http = mockFetch({ "POST /plugins/import": PREVIEW });
    const { result } = renderHookWithProviders(() => usePreviewPlugin());

    await act(() => result.current.mutateAsync({ file: new File(["{}"], "ws.JSON") }));
    expect(initOf(http).headers.get("content-type")).toBe("application/json");

    await act(async () => {
      await expect(result.current.mutateAsync({ file: new File(["x"], "notes.txt") })).rejects.toThrow(/\.zip or \.json/);
    });
    expect(http.fetch).toHaveBeenCalledTimes(1);
  });

  it("preview and install leave every error to the import card", async () => {
    mockFetch({ "POST /plugins/import": PREVIEW });
    const qc = createTestQueryClient();
    const { result } = renderHookWithProviders(() => ({ preview: usePreviewPlugin(), install: useInstallPlugin() }), {
      queryClient: qc,
    });
    const file = new File(["{}"], "b.json");
    await act(() => result.current.preview.mutateAsync({ file }));
    for (const [code, status] of [
      ["invalid_plugin", 422],
      ["plugin_conflict", 409],
      ["validation_error", 422],
      ["payload_too_large", 413],
      ["unsupported_media_type", 415],
      ["internal_error", 500],
    ] as const) {
      expect(quiet(qc, code, status), code).toBe(true);
    }

    await act(() => result.current.install.mutateAsync({ file }));
    expect(quiet(qc, "plugin_conflict", 409)).toBe(true);
    expect(quiet(qc, "internal_error", 500)).toBe(true);
  });

  it("install uploads with dry_run=false and refetches plugins, agents, skills and the workspace counts", async () => {
    const result201 = { installed: ROW, created: { agents: 1, skills: 2, eval_cases: 3, conventions: 0, learnings: 4 } };
    const http = mockFetch({ "POST /plugins/import": result201 });
    const qc = createTestQueryClient();
    const keys = invalidated(qc);
    const { result } = renderHookWithProviders(() => useInstallPlugin(), { queryClient: qc });
    const file = new File(["PK"], "acme.zip");

    const out = await act(() => result.current.mutateAsync({ file }));

    expect(out).toEqual(result201);
    const req = http.requests("POST", "/plugins/import")[0];
    expect(new URLSearchParams(req?.search).get("dry_run")).toBe("false");
    expect(initOf(http).headers.get("content-type")).toBe("application/zip");
    for (const k of [pluginKeys.all, agentKeys.all, skillKeys.all, workspaceKeys.stats()]) {
      expect(keys()).toContain(JSON.stringify(k));
    }
  });

  it("enable and disable PATCH {enabled} and refetch plugins, agents and skills", async () => {
    const http = mockFetch({ "PATCH /plugins/:id": { ...ROW, enabled: true } });
    const qc = createTestQueryClient();
    const keys = invalidated(qc);
    const { result } = renderHookWithProviders(() => useSetPluginEnabled(), { queryClient: qc });

    const out = await act(() => result.current.mutateAsync({ id: ROW.id, enabled: true }));

    expect(out.enabled).toBe(true);
    const req = http.requests("PATCH", `/plugins/${ROW.id}`)[0];
    expect(req?.body).toEqual({ enabled: true });
    expect(initOf(http).headers.get("content-type")).toBe("application/json");
    for (const k of [pluginKeys.all, agentKeys.all, skillKeys.all]) expect(keys()).toContain(JSON.stringify(k));

    await act(() => result.current.mutateAsync({ id: ROW.id, enabled: false }));
    expect(http.requests("PATCH", `/plugins/${ROW.id}`)[1]?.body).toEqual({ enabled: false });
  });

  it("remove sends DELETE with the confirm text, refetches what the plugin owned, and leaves a mismatch to the dialog", async () => {
    const removed = { ok: true, removed: { ...COUNTS, ci_installations: 2 } };
    const http = mockFetch({ "DELETE /plugins/:id": removed });
    const qc = createTestQueryClient();
    const keys = invalidated(qc);
    const { result } = renderHookWithProviders(() => useRemovePlugin(), { queryClient: qc });

    const out = await act(() => result.current.mutateAsync({ id: ROW.id, confirm: "acme" }));

    expect(out).toEqual(removed);
    expect(http.requests("DELETE", `/plugins/${ROW.id}`)[0]?.body).toEqual({ confirm: "acme" });
    expect(initOf(http).headers.get("content-type")).toBe("application/json");
    for (const k of [pluginKeys.all, agentKeys.all, skillKeys.all, evalKeys.all, ciKeys.all, workspaceKeys.stats()]) {
      expect(keys()).toContain(JSON.stringify(k));
    }
    expect(quiet(qc, "confirmation_mismatch")).toBe(true);
    expect(quiet(qc, "not_found", 404)).toBe(false);
    expect(quiet(qc, "internal_error", 500)).toBe(false);
  });
});

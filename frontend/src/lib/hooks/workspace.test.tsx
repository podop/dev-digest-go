import { describe, it, expect, afterEach, onTestFinished, vi } from "vitest";
import {
  PluginBundle,
  Settings,
  SettingsUpdate,
  WorkspaceInfo,
  WorkspaceRenameRequest,
  WorkspaceResetRequest,
  WorkspaceResetResult,
  WorkspaceStats,
} from "@devdigest/shared";
import {
  WORKSPACE_NAME_MAX,
  normalizeWorkspaceName,
  workspaceNameProblem,
} from "@devdigest/shared/constants/workspace";
import { renderHookWithProviders, cleanup, act, waitFor, createTestQueryClient } from "@/test/render";
import { mockFetch } from "@/test/fetch-mock";
import { api } from "../api";
import { prKeys, repoKeys, settingsKeys, workspaceKeys } from "./keys";
import {
  WORKSPACE_EXPORT_FALLBACK_FILE_NAME,
  useExportWorkspace,
  useRenameWorkspace,
  useResetHistory,
  useWorkspace,
  useWorkspaceStats,
} from "./workspace";

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

const INFO = {
  workspaceId: "11111111-1111-4111-8111-111111111111",
  name: "default",
  dataDir: "/data",
  cloneDir: "/data/clones",
  repos: [],
};
const STATS = { agents: 3, skills: 2, eval_cases: 1, learnings: 5, repos: 1, runs: 40 };
const RESET = {
  deleted: {
    agent_runs: 4,
    run_traces: 4,
    reviews: 3,
    findings: 9,
    multi_agent_runs: 0,
    eval_suite_runs: 0,
    eval_runs: 2,
    ci_runs: 1,
    composed_reviews: 0,
    learnings: 5,
  },
  prs_unmarked: 2,
};

describe("workspace hooks", () => {
  it("reads the workspace and stats, and a rename replaces the cached workspace", async () => {
    const http = mockFetch({
      "GET /workspace": INFO,
      "GET /workspace/stats": STATS,
      "PUT /workspace": { ...INFO, name: "acme" },
    });
    const qc = createTestQueryClient();
    const { result } = renderHookWithProviders(
      () => ({ info: useWorkspace(), stats: useWorkspaceStats(), rename: useRenameWorkspace() }),
      { queryClient: qc },
    );

    await waitFor(() => expect(result.current.info.data).toEqual(INFO));
    await waitFor(() => expect(result.current.stats.data).toEqual(STATS));

    await act(() => result.current.rename.mutateAsync("acme"));

    expect(http.requests("PUT", "/workspace")[0]?.body).toEqual({ name: "acme" });
    expect(qc.getQueryData(workspaceKeys.info())).toEqual({ ...INFO, name: "acme" });
  });

  it("export saves the body under the server's file name, with a fallback name", async () => {
    const http = mockFetch({
      "GET /workspace/export": new Response('{"manifest":{}}', {
        status: 200,
        headers: {
          "content-type": "application/json",
          "content-disposition": 'attachment; filename="devdigest-workspace-acme-2026-10-25.json"',
        },
      }),
    });
    const created: Blob[] = [];
    // jsdom has no object URLs; install stand-ins and remove them again afterwards.
    URL.createObjectURL = (b: Blob) => {
      created.push(b);
      return "blob:test";
    };
    URL.revokeObjectURL = () => {};
    onTestFinished(() => {
      Reflect.deleteProperty(URL, "createObjectURL");
      Reflect.deleteProperty(URL, "revokeObjectURL");
    });
    const names: string[] = [];
    vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(function (this: HTMLAnchorElement) {
      names.push(this.download);
    });

    const { result } = renderHookWithProviders(() => useExportWorkspace());
    const out = await act(() => result.current.mutateAsync());
    expect(out.fileName).toBe("devdigest-workspace-acme-2026-10-25.json");
    expect(names).toEqual(["devdigest-workspace-acme-2026-10-25.json"]);
    expect(await created[0]?.text()).toBe('{"manifest":{}}');

    http.on("GET /workspace/export", new Response("{}", { status: 200 }));
    const again = await act(() => result.current.mutateAsync());
    expect(again.fileName).toBe(WORKSPACE_EXPORT_FALLBACK_FILE_NAME);
  });

  it("api.download returns the blob and name, and normalizes an error answer", async () => {
    mockFetch({
      "GET /ok": new Response("abc", { headers: { "content-disposition": "attachment; filename=x.json" } }),
      "GET /boom": new Response(JSON.stringify({ error: { code: "internal", message: "no" } }), { status: 500 }),
    });
    const ok = await api.download("/ok");
    expect(ok.fileName).toBe("x.json");
    expect(await ok.blob.text()).toBe("abc");
    await expect(api.download("/boom")).rejects.toMatchObject({ status: 500, code: "internal" });
  });

  it("a reset sends the confirm text and refetches workspace, PR, repo and spend reads", async () => {
    const http = mockFetch({ "POST /workspace/reset-history": RESET });
    const qc = createTestQueryClient();
    const spy = vi.spyOn(qc, "invalidateQueries");
    const { result } = renderHookWithProviders(() => useResetHistory(), { queryClient: qc });

    const out = await act(() => result.current.mutateAsync("acme"));

    expect(out).toEqual(RESET);
    expect(http.requests("POST", "/workspace/reset-history")[0]?.body).toEqual({ confirm: "acme" });
    const keys = spy.mock.calls.map((c) => JSON.stringify(c[0]?.queryKey));
    for (const k of [workspaceKeys.all, prKeys.all, repoKeys.all, settingsKeys.spend()]) {
      expect(keys).toContain(JSON.stringify(k));
    }
  });
});

describe("workspace contracts", () => {
  it("parse the §7 response shapes and reject extra keys on request bodies", () => {
    expect(WorkspaceInfo.parse(INFO)).toEqual(INFO);
    expect(WorkspaceStats.parse(STATS)).toEqual(STATS);
    expect(WorkspaceResetResult.parse(RESET)).toEqual(RESET);
    expect(WorkspaceRenameRequest.safeParse({ name: "a", extra: 1 }).success).toBe(false);
    expect(WorkspaceResetRequest.safeParse({ confirm: "a", extra: 1 }).success).toBe(false);
    expect(WorkspaceResetRequest.parse({ confirm: " Acme " }).confirm).toBe(" Acme ");
  });

  it("the name rule trims, counts code points, and refuses control characters", () => {
    expect(WorkspaceRenameRequest.parse({ name: "  acme · payments " }).name).toBe("acme · payments");
    expect(normalizeWorkspaceName("  x ")).toBe("x");
    expect(workspaceNameProblem("   ")).toBe("empty");
    expect(workspaceNameProblem("a\nb")).toBe("control_character");
    expect(workspaceNameProblem("a\u007fb")).toBe("control_character");
    expect(workspaceNameProblem("😀".repeat(WORKSPACE_NAME_MAX))).toBeNull();
    expect(workspaceNameProblem("😀".repeat(WORKSPACE_NAME_MAX + 1))).toBe("too_long");
    expect(WorkspaceRenameRequest.safeParse({ name: "x".repeat(81) }).success).toBe(false);
  });

  it("Settings.retention_days accepts 30..365 and null, defaults to 90, and fails as one invalid_union", () => {
    expect(Settings.parse({}).retention_days).toBe(90);
    expect(Settings.parse({ retention_days: null }).retention_days).toBeNull();
    for (const days of [30, 60, 90, 180, 365]) expect(SettingsUpdate.parse({ retention_days: days })).toEqual({ retention_days: days });
    for (const bad of [45, 0, "90"]) {
      const res = SettingsUpdate.safeParse({ retention_days: bad });
      expect(res.success).toBe(false);
      if (!res.success) {
        expect(res.error.issues).toHaveLength(1);
        expect(res.error.issues[0]).toMatchObject({ code: "invalid_union", message: "Invalid input", path: ["retention_days"] });
      }
    }
  });

  it("an old plugin bundle still parses and a workspace bundle keeps its additive fields", () => {
    const old = {
      manifest: {
        name: "p",
        version: "1",
        format: "devdigest-plugin/v1",
        exported_at: "2026-10-01T00:00:00.000Z",
        counts: { agents: 1, skills: 1, eval_cases: 0, conventions: 1 },
      },
      agents: [
        { name: "A", description: "", provider: "openai", model: "m", system_prompt: "s", enabled: true, skills: ["S"] },
      ],
      skills: [{ name: "S", description: "", type: "rubric", source: "manual", body: "b", enabled: true }],
      eval_cases: [],
      conventions: [{ rule: "r", accepted: true }],
    };
    const parsed = PluginBundle.parse(old);
    expect(parsed.manifest.kind).toBeUndefined();
    expect(parsed.learnings).toBeUndefined();

    const ws = PluginBundle.parse({
      ...old,
      manifest: {
        ...old.manifest,
        kind: "workspace",
        counts: { ...old.manifest.counts, learnings: 1, context_files: 1 },
      },
      agents: [{ ...old.agents[0], strategy: "auto", ci_fail_on: "never", repo_intel: false }],
      skills: [
        {
          ...old.skills[0],
          source: "imported_file",
          versions: [{ version: 1, body: "b", description: null, message: null, created_at: "2026-10-01T00:00:00.000Z" }],
        },
      ],
      conventions: [{ rule: "r", accepted: true, repo: "o/r", category: "naming", status: "accepted" }],
      learnings: [{ scope: "repo", kind: "fact", content: "c", confidence: null, repo: "o/r" }],
      context_files: [{ repo: "o/r", path: "docs/a.md", content: "x" }],
      settings: { theme: "dark", retention_days: null },
    });
    expect(ws.manifest.kind).toBe("workspace");
    expect(ws.skills[0]?.versions).toHaveLength(1);
    expect(ws.settings).toEqual({ theme: "dark", retention_days: null });
  });
});

import { describe, it, expect, afterEach, vi } from "vitest";
import { renderHookWithProviders, cleanup, act, waitFor, createTestQueryClient } from "@/test/render";
import { mockFetch } from "@/test/fetch-mock";
import { ApiError } from "../api";
import { isQuietError } from "../query-meta";
import {
  CiExport,
  CiExportInput,
  CiInstallation,
  CiRefreshResult,
  CiRepoEntry,
  CiRun,
  CiTarget,
} from "@devdigest/shared/contracts/eval-ci";
import { useCiExportFiles, useInstallCi, useOpenCiPr, useWorkspaceCiInstallations } from "./ci";
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

const GITLAB_REPO_ID = "3f2c1b0e-6d1a-4c55-9a53-0a1b2c3d4e5f";

describe("ci hooks of GitLab CI export", () => {
  it("the preview and the PR carry repo_id and target gitlab_ci for a GitLab repository, and 'gha' without them", async () => {
    const http = mockFetch({
      "POST /agents/:id/export-ci": { installation: null, files: [], pr_url: null, gitlab_ci: null },
    });
    const gitlab = { repo: "g/p", repo_id: GITLAB_REPO_ID, target: "gitlab_ci", triggers: ["opened"], post_as: "none" } as const;
    const github = { repo: "o/a", triggers: ["opened"], post_as: "none" } as const;

    const preview = renderHookWithProviders(() => useCiExportFiles("a1", gitlab));
    await waitFor(() => expect(preview.result.current.isSuccess).toBe(true));
    const open = renderHookWithProviders(() => useOpenCiPr("a1"));
    await act(() => open.result.current.mutateAsync({ ...gitlab, workflow: "x: 1" }));
    const ghPreview = renderHookWithProviders(() => useCiExportFiles("a2", github));
    await waitFor(() => expect(ghPreview.result.current.isSuccess).toBe(true));

    expect(http.requests("POST", "/agents/a1/export-ci")[0]?.body).toEqual({
      repo: "g/p",
      repo_id: GITLAB_REPO_ID,
      target: "gitlab_ci",
      action: "files",
      post_as: "none",
      triggers: ["opened"],
    });
    expect(http.requests("POST", "/agents/a1/export-ci")[1]?.body).toMatchObject({
      repo_id: GITLAB_REPO_ID,
      target: "gitlab_ci",
      action: "open_pr",
      workflow: "x: 1",
    });
    expect(http.requests("POST", "/agents/a2/export-ci")[0]?.body).toEqual({
      repo: "o/a",
      target: "gha",
      action: "files",
      post_as: "none",
      triggers: ["opened"],
    });
  });

  it("useInstallCi sends repo_id and target for a GitLab repository", async () => {
    const http = mockFetch({ "POST /agents/:id/export-ci": { installation: null, files: [], pr_url: null } });
    const { result } = renderHookWithProviders(() => useInstallCi());

    await act(() => result.current.mutateAsync({ agentId: "a1", repo: "g/p", repo_id: GITLAB_REPO_ID, target: "gitlab_ci" }));

    expect(http.requests("POST", "/agents/a1/export-ci")[0]?.body).toEqual({
      repo: "g/p",
      repo_id: GITLAB_REPO_ID,
      target: "gitlab_ci",
      action: "open_pr",
    });
  });
});

describe("eval-ci contract of GitLab CI (spec section 7.1)", () => {
  it("adds gitlab_ci and the optional fields while GitHub shapes stay valid", () => {
    expect(CiTarget.parse("gitlab_ci")).toBe("gitlab_ci");
    expect(CiExportInput.parse({ repo: "o/a" }).target).toBeUndefined();
    expect(CiExportInput.parse({ repo: "g/p", repo_id: GITLAB_REPO_ID, target: "gitlab_ci" }).repo_id).toBe(GITLAB_REPO_ID);
    expect(CiExportInput.safeParse({ repo: "g/p", repo_id: "not-a-uuid" }).success).toBe(false);

    const gl = {
      job_file: ".gitlab/devdigest/review-x.yml",
      root_config: "keep_existing",
      config_path: ".gitlab-ci.yml",
      include_snippet: "include:\n  - local: .gitlab/devdigest/review-x.yml\n",
      triggers_note: null,
    };
    expect(CiExport.parse({ installation: null, files: [], pr_url: null, gitlab_ci: gl }).gitlab_ci).toEqual(gl);
    expect(CiExport.parse({ installation: null, files: [], pr_url: null }).gitlab_ci).toBeUndefined();
    expect(CiExport.safeParse({ installation: null, files: [], pr_url: null, gitlab_ci: { ...gl, root_config: "x" } }).success).toBe(false);

    const inst = { id: "i", agent_id: "a", repo: "g/p", target_type: "gitlab_ci", installed_at: "t", provider: "gitlab", host: "gitlab.com" };
    expect(CiInstallation.parse(inst)).toMatchObject({ provider: "gitlab", host: "gitlab.com" });
    expect(CiInstallation.parse({ id: "i", agent_id: "a", repo: "o/a", target_type: "gha", installed_at: "t" }).provider).toBeUndefined();

    const run = {
      id: "r", ci_installation_id: null, pr_number: 3, ran_at: null, status: null, findings_count: null,
      cost_usd: null, github_url: null, source: "gitlab_ci", provider: "gitlab", host: "gitlab.com", pr_url: "https://gitlab.com/g/p/-/merge_requests/3",
    };
    expect(CiRun.parse(run).pr_url).toBe("https://gitlab.com/g/p/-/merge_requests/3");

    const entry = { repo: "g/p", installations: [], last_run: null, provider: "gitlab", host: "gitlab.com", target_type: "gitlab_ci" };
    expect(CiRepoEntry.parse(entry)).toMatchObject({ provider: "gitlab", host: "gitlab.com", target_type: "gitlab_ci" });
    expect(CiRepoEntry.parse({ repo: "o/a", installations: [], last_run: null }).host).toBeUndefined();

    const refresh = {
      ingested: 0,
      degraded: true,
      installations: [
        { installation_id: "i", repo: "g/p", host: "gitlab.com", state: "error", error_code: "gitlab_token_missing", ingested: 0, skipped: [] },
      ],
    };
    expect(CiRefreshResult.parse(refresh).installations[0]?.error_code).toBe("gitlab_token_missing");
  });
});

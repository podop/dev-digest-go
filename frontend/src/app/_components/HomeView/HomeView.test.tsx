/* HomeView — the first-run gate on `/` (AC18, FR27; AC26, FR21; AC30, FR39): the redirect for each missing
   prerequisite and for a complete setup, a custom provider counting as a model provider, a GitLab
   connection with a token counting as a code host, the error
   state with Retry (no redirect), no home content before the redirect, and the no-hard-coded-copy
   check (AC22). AppShell is a passthrough; fetch is stubbed
   and the real hooks run. */
import type React from "react";
import { describe, it, expect, afterEach, beforeEach, vi } from "vitest";
import type { CustomProvider, GitLabConnection, Repo, SecretsStatus } from "@devdigest/shared";
import { renderWithProviders, screen, cleanup, waitFor, pseudoMessages } from "@/test/render";
import { mockFetch as rawMockFetch, jsonResponse, type RouteHandler } from "@/test/fetch-mock";
import { strayText } from "@/test/pseudo";

const nav = vi.hoisted(() => ({ replace: vi.fn(), push: vi.fn() }));
vi.mock("next/navigation", () => ({ useRouter: () => nav }));
vi.mock("@/components/app-shell", () => ({
  AppShell: ({ children }: { children: React.ReactNode }) => <>{children}</>,
}));

import { HomeView } from "./HomeView";

beforeEach(() => {
  nav.replace.mockReset();
  nav.push.mockReset();
});
afterEach(cleanup);

const repo = (id: string, full_name: string): Repo => ({
  id,
  workspace_id: "w1",
  owner: "acme",
  name: full_name.split("/")[1] ?? full_name,
  full_name,
  default_branch: "main",
  clone_path: null,
  last_polled_at: null,
  created_by: null,
});
const REPOS = [repo("r1", "acme/api"), repo("r2", "acme/web")];

const customProvider = (over: Partial<CustomProvider> = {}): CustomProvider => ({
  id: "custom:synapse",
  name: "Synapse",
  protocol: "openai",
  base_url: "https://gw.example.com/v1",
  plain_http: false,
  auth_mode: "bearer",
  auth_header: null,
  structured_output: "strict",
  key_configured: false,
  extra_headers: [],
  models: [],
  created_at: "2026-10-26T10:00:00Z",
  updated_at: "2026-10-26T10:00:00Z",
  ...over,
});
const CUSTOM = "GET /settings/custom-providers";
const GITLAB = "GET /settings/gitlab-connections";

const gitlabConnection = (over: Partial<GitLabConnection> = {}): GitLabConnection => ({
  id: "c1",
  name: "gitlab.com",
  base_url: "https://gitlab.com",
  host: "gitlab.com",
  token_configured: true,
  repo_count: 0,
  created_at: "2026-10-26T10:00:00Z",
  updated_at: "2026-10-26T10:00:00Z",
  ...over,
});

/** The gate loads four requests; unless a test says otherwise there is no GitLab connection. */
const mockFetch = (routes: Record<string, RouteHandler>) => rawMockFetch({ [GITLAB]: [], ...routes });

const NONE: SecretsStatus = { openai: false, anthropic: false, openrouter: false, github: false };
const secrets = (over: Partial<SecretsStatus>): SecretsStatus => ({ ...NONE, ...over });

describe("HomeView gate", () => {
  it.each([
    ["no model key, no token, no repo", secrets({}), [], "/settings/setup?step=model"],
    ["no model key even with a token and repos", secrets({ github: true }), REPOS, "/settings/setup?step=model"],
    ["a model key but no GitHub token and no GitLab connection", secrets({ openai: true }), REPOS, "/settings/setup?step=code-host"],
    ["an anthropic key and a token but no repo", secrets({ anthropic: true, github: true }), [], "/settings/setup?step=repo"],
    // An env-provided key is reported by secrets-status exactly like a stored one (EC11).
    ["only an env-provided openrouter key and a token, with repos", secrets({ openrouter: true, github: true }), REPOS, "/repos/r1/pulls"],
    ["every prerequisite met", secrets({ openai: true, anthropic: true, openrouter: true, github: true }), REPOS, "/repos/r1/pulls"],
  ] as const)("%s → replaces the URL with %s", async (_name, status, repos, href) => {
    mockFetch({ "GET /settings/secrets-status": status, "GET /repos": [...repos], [CUSTOM]: [] });
    renderWithProviders(<HomeView />);
    await waitFor(() => expect(nav.replace).toHaveBeenCalledWith(href));
    expect(nav.replace).toHaveBeenCalledTimes(1);
    expect(nav.push).not.toHaveBeenCalled();
  });

  it("shows only the skeleton while loading and while redirecting to setup — no home content flashes", async () => {
    let release: (r: Response) => void = () => {};
    mockFetch({
      "GET /settings/secrets-status": () => new Promise<Response>((resolve) => (release = resolve)),
      "GET /repos": [],
      [CUSTOM]: [],
    });
    const { container } = renderWithProviders(<HomeView />);
    expect(container.querySelector("[aria-busy='true']")).not.toBeNull();
    expect(nav.replace).not.toHaveBeenCalled();

    release(jsonResponse(NONE));
    await waitFor(() => expect(nav.replace).toHaveBeenCalledWith("/settings/setup?step=model"));
    expect(container.querySelector("[aria-busy='true']")).not.toBeNull();
    expect(screen.queryByRole("button")).not.toBeInTheDocument();
    expect(screen.queryByText("No repositories yet")).not.toBeInTheDocument();
  });

  it("offers the first repo while the redirect to its PR list happens", async () => {
    mockFetch({ "GET /settings/secrets-status": secrets({ openai: true, github: true }), "GET /repos": REPOS, [CUSTOM]: [] });
    const { user } = renderWithProviders(<HomeView />);
    await user.click(await screen.findByRole("button", { name: "Open acme/api" }));
    expect(nav.push).toHaveBeenCalledWith("/repos/r1/pulls");
  });

  // AC26 / D21: one defined custom provider counts as a model provider — keyed or not, tested or not.
  it.each([
    ["without a key", customProvider({ key_configured: false })],
    ["with a key", customProvider({ key_configured: true })],
  ])("treats a custom provider %s as a connected model provider (AC26)", async (_name, provider) => {
    mockFetch({ "GET /settings/secrets-status": secrets({ github: true }), "GET /repos": REPOS, [CUSTOM]: [provider] });
    renderWithProviders(<HomeView />);
    await waitFor(() => expect(nav.replace).toHaveBeenCalledWith("/repos/r1/pulls"));
    expect(nav.replace).toHaveBeenCalledTimes(1);
  });

  it("with a custom provider the next unmet step is still reported (code host, then repo) (AC26)", async () => {
    mockFetch({ "GET /settings/secrets-status": NONE, "GET /repos": REPOS, [CUSTOM]: [customProvider()] });
    renderWithProviders(<HomeView />);
    await waitFor(() => expect(nav.replace).toHaveBeenCalledWith("/settings/setup?step=code-host"));
    cleanup();
    nav.replace.mockReset();

    mockFetch({ "GET /settings/secrets-status": secrets({ github: true }), "GET /repos": [], [CUSTOM]: [customProvider()] });
    renderWithProviders(<HomeView />);
    await waitFor(() => expect(nav.replace).toHaveBeenCalledWith("/settings/setup?step=repo"));
  });

  it("waits for the custom providers before redirecting — no model step flashes for a workspace that has one", async () => {
    let release: (r: Response) => void = () => {};
    mockFetch({
      "GET /settings/secrets-status": secrets({ github: true }),
      "GET /repos": REPOS,
      [CUSTOM]: () => new Promise<Response>((resolve) => (release = resolve)),
    });
    const { container } = renderWithProviders(<HomeView />);
    await waitFor(() => expect(container.querySelector("[aria-busy='true']")).not.toBeNull());
    expect(nav.replace).not.toHaveBeenCalled();

    release(jsonResponse([customProvider()]));
    await waitFor(() => expect(nav.replace).toHaveBeenCalledWith("/repos/r1/pulls"));
    expect(nav.replace).toHaveBeenCalledTimes(1);
  });

  // AC30 / FR39: a GitLab connection with a token is a code host; one without a token is not.
  it.each([
    ["with a token and no GitHub token, goes on to the repository", [gitlabConnection()], [], "/settings/setup?step=repo"],
    ["with a token and no GitHub token, opens the first repo when there is one", [gitlabConnection()], REPOS, "/repos/r1/pulls"],
    ["without a token (alone) still stops at the code-host step", [gitlabConnection({ token_configured: false })], REPOS, "/settings/setup?step=code-host"],
    [
      "without a token next to one with a token passes",
      [gitlabConnection({ id: "c2", token_configured: false }), gitlabConnection({ id: "c3", host: "git.corp.example" })],
      REPOS,
      "/repos/r1/pulls",
    ],
  ] as const)("a GitLab connection %s (AC30)", async (_name, connections, repos, href) => {
    mockFetch({
      "GET /settings/secrets-status": secrets({ openai: true }),
      "GET /repos": [...repos],
      [CUSTOM]: [],
      [GITLAB]: [...connections],
    });
    renderWithProviders(<HomeView />);
    await waitFor(() => expect(nav.replace).toHaveBeenCalledWith(href));
    expect(nav.replace).toHaveBeenCalledTimes(1);
  });

  it("waits for the GitLab connections before redirecting — no code-host step flashes for a GitLab-only workspace (AC30)", async () => {
    let release: (r: Response) => void = () => {};
    mockFetch({
      "GET /settings/secrets-status": secrets({ openai: true }),
      "GET /repos": REPOS,
      [CUSTOM]: [],
      [GITLAB]: () => new Promise<Response>((resolve) => (release = resolve)),
    });
    const { container } = renderWithProviders(<HomeView />);
    await waitFor(() => expect(container.querySelector("[aria-busy='true']")).not.toBeNull());
    expect(nav.replace).not.toHaveBeenCalled();

    release(jsonResponse([gitlabConnection()]));
    await waitFor(() => expect(nav.replace).toHaveBeenCalledWith("/repos/r1/pulls"));
    expect(nav.replace).toHaveBeenCalledTimes(1);
  });

  it.each([
    ["secrets-status", "GET /settings/secrets-status"],
    ["repos", "GET /repos"],
    ["custom providers", CUSTOM],
    ["GitLab connections", GITLAB],
  ])("shows an error with Retry and does not redirect when %s fails; Retry recovers", async (_name, failing) => {
    let broken = true;
    const ok: Record<string, RouteHandler> = {
      "GET /settings/secrets-status": secrets({ github: true }),
      "GET /repos": [REPOS[0]],
      [CUSTOM]: [customProvider()],
      [GITLAB]: [],
    };
    mockFetch({
      ...ok,
      [failing]: () => (broken ? jsonResponse({ error: { code: "internal", message: "boom" } }, 500) : ok[failing] ?? null),
    });
    const { user } = renderWithProviders(<HomeView />);
    expect(await screen.findByText("Could not check your setup")).toBeInTheDocument();
    expect(nav.replace).not.toHaveBeenCalled();
    expect(screen.queryByRole("button", { name: /Open/ })).not.toBeInTheDocument();

    broken = false;
    await user.click(screen.getByRole("button", { name: "Retry" }));
    await waitFor(() => expect(nav.replace).toHaveBeenCalledWith("/repos/r1/pulls"));
  });

  it("renders no hard-coded copy in the error and redirecting states (AC22)", async () => {
    mockFetch({ "GET /settings/secrets-status": jsonResponse({ error: { code: "x", message: "m" } }, 500), "GET /repos": [], [CUSTOM]: [] });
    const failed = renderWithProviders(<HomeView />, { messages: pseudoMessages() });
    await screen.findByRole("button", { name: "⟦home.error.retry⟧" });
    expect(strayText(failed.container)).toEqual([]);
    failed.unmount();

    mockFetch({ "GET /settings/secrets-status": secrets({ openai: true, github: true }), "GET /repos": REPOS, [CUSTOM]: [] });
    const ready = renderWithProviders(<HomeView />, { messages: pseudoMessages() });
    await screen.findByRole("button", { name: "⟦home.openRepo⟧" });
    expect(strayText(ready.container)).toEqual([]);
  });
});

import React from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { CustomProvider, GitLabConnection, Repo, SecretsStatus } from "@/lib/types";
import { renderWithProviders, screen, cleanup, waitFor, within, pseudoMessages } from "@/test/render";
import { mockFetch as rawMockFetch, jsonResponse, type RouteHandler } from "@/test/fetch-mock";
import { strayText } from "@/test/pseudo";

const nav = vi.hoisted(() => ({ replace: vi.fn(), push: vi.fn() }));
vi.mock("next/navigation", () => ({ useRouter: () => nav }));

import { SetupView } from "./SetupView";
import { setupHref } from "./constants";

afterEach(() => {
  cleanup();
  nav.replace.mockReset();
  nav.push.mockReset();
});

const CUSTOM = "GET /settings/custom-providers";
const GITLAB = "GET /settings/gitlab-connections";
const connection = (over: Partial<GitLabConnection> = {}): GitLabConnection => ({
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
/** The view loads four requests; unless a test says otherwise there is no GitLab connection. */
const mockFetch = (routes: Record<string, RouteHandler>) => rawMockFetch({ [GITLAB]: [], ...routes });
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
const NONE: SecretsStatus = { openai: false, anthropic: false, openrouter: false, github: false };
const repo = (id: string, full_name: string): Repo => ({
  id,
  workspace_id: "w1",
  owner: "acme",
  name: full_name.split("/")[1]!,
  full_name,
  default_branch: "main",
  clone_path: null,
  last_polled_at: null,
  created_by: null,
});

/** The page passes the URL's `step`; here a router.replace stub feeds the new step back in, like Next does. */
function Host({ initial }: { initial?: string }) {
  const [step, setStep] = React.useState(initial);
  React.useEffect(() => {
    nav.replace.mockImplementation((href: string) => setStep(new URL(href, "http://x").searchParams.get("step") ?? undefined));
  }, []);
  return <SetupView requestedStep={step} hrefForStep={setupHref} />;
}

const steps = () => within(screen.getByRole("list", { name: "Setup steps" })).getAllByRole("listitem");
const current = () => steps().find((li) => li.getAttribute("aria-current") === "step");
const cont = () => screen.getByRole("button", { name: "Continue" });

describe("SetupView step resolution (FR22)", () => {
  it("opens the first incomplete step and pulls a later requested step back to it (AC19, EC14)", async () => {
    mockFetch({
      "GET /settings/secrets-status": { ...NONE, openrouter: true },
      [CUSTOM]: [], "GET /repos": [],
    });
    renderWithProviders(<Host initial="repo" />);
    // Model key is there, no code host: ?step=repo shows the code-host step.
    expect(await screen.findByRole("heading", { name: "Connect a code host" })).toBeInTheDocument();
    expect(current()).toHaveTextContent("Code host");
    expect(screen.getByText("Step 2 of 3 · You can change keys later in Settings")).toBeInTheDocument();
    expect(steps()[0]).toHaveTextContent("completed");
  });

  it("falls back to the first incomplete step for a missing or invalid step, and to repo when all are complete", async () => {
    const api = mockFetch({ "GET /settings/secrets-status": NONE, [CUSTOM]: [], "GET /repos": [] });
    const first = renderWithProviders(<Host initial="bogus" />);
    expect(await screen.findByRole("heading", { name: "Connect a model provider" })).toBeInTheDocument();
    first.unmount();

    // EC11: keys that come only from the environment look the same in secrets-status — done.
    api.on("GET /settings/secrets-status", { ...NONE, openai: true, github: true });
    api.on("GET /repos", [repo("r1", "acme/api")]);
    renderWithProviders(<Host />);
    expect(await screen.findByRole("heading", { name: "Add your first repository" })).toBeInTheDocument();
    expect(screen.getByText("Step 3 of 3 · You can change keys later in Settings")).toBeInTheDocument();
  });
});

describe("SetupView ?step contract", () => {
  it("honours ?step=model when everything is done (About → Run setup again, D14) and pins missing steps in the URL", async () => {
    const api = mockFetch({
      "GET /settings/secrets-status": { ...NONE, openrouter: true, github: true },
      [CUSTOM]: [], "GET /repos": [repo("r1", "acme/api")],
    });
    const first = renderWithProviders(<Host initial="model" />);
    expect(await screen.findByRole("heading", { name: "Connect a model provider" })).toBeInTheDocument();
    expect(current()).toHaveTextContent("Model provider");
    expect(screen.getByRole("radio", { name: "OpenRouter" })).toBeChecked();
    expect(nav.replace).not.toHaveBeenCalled();
    first.unmount();

    // code-host and repo are honoured as requested; an absent step is replaced by the resolved one.
    renderWithProviders(<Host initial="code-host" />);
    expect(await screen.findByRole("heading", { name: "Connect a code host" })).toBeInTheDocument();
    cleanup();
    renderWithProviders(<Host initial="repo" />);
    expect(await screen.findByRole("heading", { name: "Add your first repository" })).toBeInTheDocument();
    expect(nav.replace).not.toHaveBeenCalled();
    cleanup();

    api.on("GET /repos", []);
    renderWithProviders(<Host />);
    expect(await screen.findByRole("heading", { name: "Add your first repository" })).toBeInTheDocument();
    expect(nav.replace).toHaveBeenCalledWith("/settings/setup?step=repo");
  });

  it("replaces a too-far step with the earliest unmet one in the URL (AC21 earlier step)", async () => {
    mockFetch({ "GET /settings/secrets-status": NONE, [CUSTOM]: [], "GET /repos": [] });
    renderWithProviders(<Host initial="repo" />);
    expect(await screen.findByRole("heading", { name: "Connect a model provider" })).toBeInTheDocument();
    expect(nav.replace).toHaveBeenCalledWith("/settings/setup?step=model");
  });
});

describe("SetupView navigation (FR26, AC19, AC20)", () => {
  it("walks model → code host → repo with Continue and returns with Back, updating the URL each time", async () => {
    let secrets = NONE;
    mockFetch({
      "GET /settings/secrets-status": () => secrets,
      [CUSTOM]: [], "GET /repos": [],
      "POST /settings/test-connection": (req) => {
        const { provider } = req.body as { provider: "openrouter" | "github" };
        secrets = { ...secrets, [provider]: true };
        return provider === "github"
          ? { provider, ok: true, message: "OK", login: "ann", scopes: ["repo"] }
          : { provider, ok: true, message: "OK", model: "m/x", model_reachable: true, latency_ms: 9 };
      },
    });
    const { user } = renderWithProviders(<Host />);

    // Step 1: stepper, Back shown but disabled (prototype), OpenRouter selected, Continue off until a test passes.
    await screen.findByRole("heading", { name: "Connect a model provider" });
    expect(current()).toHaveTextContent("Model provider");
    expect(screen.getByText("Step 1 of 3 · You can change keys later in Settings")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Back" })).toBeDisabled();
    expect(screen.getByRole("radio", { name: "OpenRouter" })).toBeChecked();
    expect(cont()).toBeDisabled();
    await user.type(screen.getByRole("textbox", { name: "OpenRouter API key" }), "sk-or-v1-abcdefgh1234");
    await user.click(screen.getByRole("button", { name: "Test connection" }));
    await waitFor(() => expect(cont()).toBeEnabled());
    await user.click(cont());
    expect(nav.replace).toHaveBeenLastCalledWith("/settings/setup?step=code-host");

    // Step 2 → Back → step 1 again (stored key: "Already connected") → Continue → step 2.
    await screen.findByRole("heading", { name: "Connect a code host" });
    await user.click(screen.getByRole("button", { name: "Back" }));
    expect(nav.replace).toHaveBeenLastCalledWith("/settings/setup?step=model");
    await screen.findByRole("heading", { name: "Connect a model provider" });
    expect(await screen.findByText("Already connected")).toBeInTheDocument();
    await user.click(cont());
    await screen.findByRole("heading", { name: "Connect a code host" });

    await user.type(screen.getByRole("textbox", { name: "Personal access token" }), "ghp_abcdefgh1234");
    await user.click(screen.getByRole("button", { name: "Test connection" }));
    expect(await screen.findByText("Connected as @ann")).toBeInTheDocument();
    await user.click(cont());
    expect(nav.replace).toHaveBeenLastCalledWith("/settings/setup?step=repo");
    expect(await screen.findByRole("heading", { name: "Add your first repository" })).toBeInTheDocument();
  });

  it("does not bounce Continue back while secrets-status still shows the old state", async () => {
    // The test passes but the secrets refetch keeps answering "no key": the view trusts its own passing test.
    mockFetch({
      "GET /settings/secrets-status": NONE,
      [CUSTOM]: [], "GET /repos": [],
      "POST /settings/test-connection": { provider: "openrouter", ok: true, message: "OK" },
    });
    const { user } = renderWithProviders(<Host />);
    await screen.findByRole("heading", { name: "Connect a model provider" });
    await user.click(screen.getByRole("button", { name: "Test connection" }));
    await waitFor(() => expect(cont()).toBeEnabled());
    await user.click(cont());
    expect(await screen.findByRole("heading", { name: "Connect a code host" })).toBeInTheDocument();
  });
});

describe("SetupView code host (FR38, AC30)", () => {
  it("replaces the old ?step=github by code-host in the URL and reads Code host in the stepper (AC30)", async () => {
    mockFetch({ "GET /settings/secrets-status": { ...NONE, openrouter: true }, [CUSTOM]: [], "GET /repos": [] });
    renderWithProviders(<Host initial="github" />);
    expect(await screen.findByRole("heading", { name: "Connect a code host" })).toBeInTheDocument();
    expect(current()).toHaveTextContent("Code host");
    expect(steps()[1]).not.toHaveTextContent("GitHub token");
    await waitFor(() => expect(nav.replace).toHaveBeenCalledWith("/settings/setup?step=code-host"));
    expect(nav.replace).toHaveBeenCalledTimes(1);
    // Resolved once: the view stays where it is.
    expect(screen.getByRole("heading", { name: "Connect a code host" })).toBeInTheDocument();
  });

  it("a GitLab connection with a token completes the step: it stays put when asked for, and does not pull ?step=repo back (AC30)", async () => {
    mockFetch({
      "GET /settings/secrets-status": { ...NONE, openrouter: true },
      [CUSTOM]: [],
      "GET /repos": [],
      [GITLAB]: [connection()],
    });
    const first = renderWithProviders(<Host initial="code-host" />);
    expect(await screen.findByRole("heading", { name: "Connect a code host" })).toBeInTheDocument();
    expect(screen.getByRole("radio", { name: "GitLab" })).toBeChecked();
    expect(screen.getByText("Already connected")).toBeInTheDocument();
    expect(cont()).toBeEnabled();
    expect(nav.replace).not.toHaveBeenCalled();
    first.unmount();

    renderWithProviders(<Host initial="repo" />);
    expect(await screen.findByRole("heading", { name: "Add your first repository" })).toBeInTheDocument();
    expect(steps()[1]).toHaveTextContent("completed");
    expect(nav.replace).not.toHaveBeenCalled();
  });

  it("a connection without a token does not complete it", async () => {
    mockFetch({
      "GET /settings/secrets-status": { ...NONE, openrouter: true },
      [CUSTOM]: [],
      "GET /repos": [],
      [GITLAB]: [connection({ token_configured: false })],
    });
    renderWithProviders(<Host initial="repo" />);
    expect(await screen.findByRole("heading", { name: "Connect a code host" })).toBeInTheDocument();
    expect(current()).toHaveTextContent("Code host");
    expect(screen.queryByText("Already connected")).toBeNull();
    expect(cont()).toBeDisabled();
    expect(nav.replace).toHaveBeenCalledWith("/settings/setup?step=code-host");
  });

  it("Save and test with GitLab creates and tests the connection; Continue then moves on to the repository", async () => {
    let connections: GitLabConnection[] = [];
    const api = mockFetch({
      "GET /settings/secrets-status": { ...NONE, openrouter: true },
      [CUSTOM]: [],
      "GET /repos": [],
      [GITLAB]: () => connections,
      "POST /settings/gitlab-connections": () => {
        connections = [connection({ id: "c9" })];
        return connections[0];
      },
      "POST /settings/test-connection": { provider: "gitlab", ok: true, message: "Connected as @ann", login: "ann", scopes: ["api"] },
    });
    const { user } = renderWithProviders(<Host initial="code-host" />);
    await screen.findByRole("heading", { name: "Connect a code host" });
    expect(cont()).toBeDisabled();

    await user.click(screen.getByRole("radio", { name: "GitLab" }));
    await user.type(screen.getByRole("textbox", { name: "Personal access token" }), "glpat-abcdefghij1234567890");
    await user.click(screen.getByRole("button", { name: "Save and test" }));
    expect(await screen.findByText("Connected as @ann")).toBeInTheDocument();
    expect(api.requests("POST", "/settings/gitlab-connections")).toHaveLength(1);
    expect(api.requests("POST", "/settings/test-connection")[0]!.body).toEqual({ provider: "gitlab", connection_id: "c9" });

    await waitFor(() => expect(cont()).toBeEnabled());
    await user.click(cont());
    expect(nav.replace).toHaveBeenLastCalledWith("/settings/setup?step=repo");
    expect(await screen.findByRole("heading", { name: "Add your first repository" })).toBeInTheDocument();
  });

  it("shows the error with Retry when only the GitLab connections fail to load", async () => {
    let fail = true;
    mockFetch({
      "GET /settings/secrets-status": { ...NONE, openrouter: true, github: true },
      "GET /repos": [],
      [CUSTOM]: [],
      [GITLAB]: () => (fail ? jsonResponse({ error: { code: "boom", message: "x" } }, 500) : []),
    });
    const { user } = renderWithProviders(<Host />);
    expect(await screen.findByText("Could not load setup")).toBeInTheDocument();
    expect(nav.replace).not.toHaveBeenCalled();
    fail = false;
    await user.click(screen.getByRole("button", { name: "Retry" }));
    expect(await screen.findByRole("heading", { name: "Add your first repository" })).toBeInTheDocument();
  });
});

describe("SetupView with a custom provider (FR21, D21, AC26)", () => {
  it("stays on the code-host step when Home sends a workspace with a custom provider and no code host there (no bounce to model)", async () => {
    mockFetch({ "GET /settings/secrets-status": NONE, "GET /repos": [], [CUSTOM]: [customProvider()] });
    renderWithProviders(<Host initial="code-host" />);
    expect(await screen.findByRole("heading", { name: "Connect a code host" })).toBeInTheDocument();
    expect(current()).toHaveTextContent("Code host");
    expect(steps()[0]).toHaveTextContent("completed");
    expect(nav.replace).not.toHaveBeenCalled();
  });

  it("Continue works on the model step without a key and moves on to the code host", async () => {
    mockFetch({ "GET /settings/secrets-status": NONE, "GET /repos": [], [CUSTOM]: [customProvider()] });
    const { user } = renderWithProviders(<Host initial="model" />);
    expect(await screen.findByRole("heading", { name: "Connect a model provider" })).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Use a custom provider" })).toHaveAttribute("href", "/settings/api-keys");
    expect(cont()).toBeEnabled();
    await user.click(cont());
    expect(nav.replace).toHaveBeenLastCalledWith("/settings/setup?step=code-host");
    expect(await screen.findByRole("heading", { name: "Connect a code host" })).toBeInTheDocument();
  });
});

describe("SetupView loading and errors", () => {
  it("shows a skeleton while loading, then an error with Retry that reloads (no step content)", async () => {
    let fail = true;
    const api = mockFetch({
      "GET /settings/secrets-status": () =>
        fail ? jsonResponse({ error: { code: "boom", message: "x" } }, 500) : { ...NONE, openrouter: true, github: true },
      [CUSTOM]: [], "GET /repos": [],
    });
    const { user } = renderWithProviders(<Host />);
    expect(screen.getByRole("status", { name: "Loading setup" })).toHaveAttribute("aria-busy", "true");

    expect(await screen.findByText("Could not load setup")).toBeInTheDocument();
    expect(screen.queryByRole("heading", { name: "Connect a model provider" })).toBeNull();
    expect(nav.replace).not.toHaveBeenCalled();

    fail = false;
    await user.click(screen.getByRole("button", { name: "Retry" }));
    expect(await screen.findByRole("heading", { name: "Add your first repository" })).toBeInTheDocument();
    expect(api.requests("GET", "/settings/secrets-status").length).toBeGreaterThanOrEqual(2);
  });

  it("shows the error with Retry when only the custom providers fail to load", async () => {
    let fail = true;
    mockFetch({
      "GET /settings/secrets-status": { ...NONE, openrouter: true, github: true },
      "GET /repos": [],
      [CUSTOM]: () => (fail ? jsonResponse({ error: { code: "boom", message: "x" } }, 500) : []),
    });
    const { user } = renderWithProviders(<Host />);
    expect(await screen.findByText("Could not load setup")).toBeInTheDocument();
    fail = false;
    await user.click(screen.getByRole("button", { name: "Retry" }));
    expect(await screen.findByRole("heading", { name: "Add your first repository" })).toBeInTheDocument();
  });
});

describe("SetupView messages (AC22)", () => {
  it("takes every visible string of the three steps from the message files (pseudo bundle)", async () => {
    mockFetch({
      "GET /settings/secrets-status": { ...NONE, anthropic: true },
      [CUSTOM]: [], "GET /repos": [],
      "POST /settings/test-connection": { provider: "github", ok: true, message: "Token ok", login: "ann", scopes: ["gist"] },
      "POST /repos": repo("r9", "acme/api"),
      "GET /repos/r9/index-state": { repoId: "r9", status: "full", lastIndexedSha: "abc", updatedAt: "2026-10-24T10:00:00Z" },
    });
    const { container, user } = renderWithProviders(<Host initial="model" />, { messages: pseudoMessages() });
    const allowed = [/^\d$/, "Token ok"];

    expect(await screen.findByRole("heading", { name: "⟦setup.model.title⟧" })).toBeInTheDocument();
    expect(strayText(container, allowed)).toEqual([]);
    expect(screen.getByText("⟦setup.alreadyConnected⟧")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "⟦setup.actions.continue⟧" }));

    // Code-host step: a classic token without `repo` shows the warning, still Continue.
    await screen.findByRole("heading", { name: "⟦setup.codeHost.title⟧" });
    await user.click(screen.getByRole("button", { name: "⟦keyField.test⟧" }));
    expect(await screen.findByText("⟦setup.codeHost.github.missingRepo⟧")).toBeInTheDocument();
    expect(strayText(container, allowed)).toEqual([]);
    await user.click(screen.getByRole("button", { name: "⟦setup.actions.continue⟧" }));

    await screen.findByRole("heading", { name: "⟦setup.repo.title⟧" });
    await user.type(screen.getByPlaceholderText("⟦addRepo.urlPlaceholder⟧"), "https://github.com/acme/api");
    await user.click(screen.getByRole("button", { name: "⟦addRepo.submit⟧" }));
    expect(await screen.findByText("⟦setup.repo.indexed⟧")).toBeInTheDocument();
    expect(screen.getByText("⟦setup.repo.indexStatus.full⟧")).toBeInTheDocument();
    expect(strayText(container, allowed)).toEqual([]);
  });
});

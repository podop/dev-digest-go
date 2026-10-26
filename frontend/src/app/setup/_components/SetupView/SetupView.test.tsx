import React from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { CustomProvider, Repo, SecretsStatus } from "@/lib/types";
import { renderWithProviders, screen, cleanup, waitFor, within, pseudoMessages } from "@/test/render";
import { mockFetch, jsonResponse } from "@/test/fetch-mock";
import { strayText } from "@/test/pseudo";

const nav = vi.hoisted(() => ({ replace: vi.fn(), push: vi.fn() }));
vi.mock("next/navigation", () => ({ useRouter: () => nav }));

import { SetupView } from "./SetupView";

afterEach(() => {
  cleanup();
  nav.replace.mockReset();
  nav.push.mockReset();
});

const CUSTOM = "GET /settings/custom-providers";
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
  return <SetupView requestedStep={step} />;
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
    // Model key is there, the token is not: ?step=repo shows step github.
    expect(await screen.findByRole("heading", { name: "Connect GitHub" })).toBeInTheDocument();
    expect(current()).toHaveTextContent("GitHub token");
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

    // github and repo are honoured as requested; an absent step is replaced by the resolved one.
    renderWithProviders(<Host initial="github" />);
    expect(await screen.findByRole("heading", { name: "Connect GitHub" })).toBeInTheDocument();
    cleanup();
    renderWithProviders(<Host initial="repo" />);
    expect(await screen.findByRole("heading", { name: "Add your first repository" })).toBeInTheDocument();
    expect(nav.replace).not.toHaveBeenCalled();
    cleanup();

    api.on("GET /repos", []);
    renderWithProviders(<Host />);
    expect(await screen.findByRole("heading", { name: "Add your first repository" })).toBeInTheDocument();
    expect(nav.replace).toHaveBeenCalledWith("/setup?step=repo");
  });

  it("replaces a too-far step with the earliest unmet one in the URL (AC21 earlier step)", async () => {
    mockFetch({ "GET /settings/secrets-status": NONE, [CUSTOM]: [], "GET /repos": [] });
    renderWithProviders(<Host initial="repo" />);
    expect(await screen.findByRole("heading", { name: "Connect a model provider" })).toBeInTheDocument();
    expect(nav.replace).toHaveBeenCalledWith("/setup?step=model");
  });
});

describe("SetupView navigation (FR26, AC19, AC20)", () => {
  it("walks model → github → repo with Continue and returns with Back, updating the URL each time", async () => {
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

    // Step 1: stepper, no Back, OpenRouter selected, Continue off until a test passes.
    await screen.findByRole("heading", { name: "Connect a model provider" });
    expect(current()).toHaveTextContent("Model provider");
    expect(screen.getByText("Step 1 of 3 · You can change keys later in Settings")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Back" })).toBeNull();
    expect(screen.getByRole("radio", { name: "OpenRouter" })).toBeChecked();
    expect(cont()).toBeDisabled();
    await user.type(screen.getByRole("textbox", { name: "OpenRouter API key" }), "sk-or-v1-abcdefgh1234");
    await user.click(screen.getByRole("button", { name: "Test connection" }));
    await waitFor(() => expect(cont()).toBeEnabled());
    await user.click(cont());
    expect(nav.replace).toHaveBeenLastCalledWith("/setup?step=github");

    // Step 2 → Back → step 1 again (stored key: "Already connected") → Continue → step 2.
    await screen.findByRole("heading", { name: "Connect GitHub" });
    await user.click(screen.getByRole("button", { name: "Back" }));
    expect(nav.replace).toHaveBeenLastCalledWith("/setup?step=model");
    await screen.findByRole("heading", { name: "Connect a model provider" });
    expect(await screen.findByText("Already connected")).toBeInTheDocument();
    await user.click(cont());
    await screen.findByRole("heading", { name: "Connect GitHub" });

    await user.type(screen.getByRole("textbox", { name: "Personal access token" }), "ghp_abcdefgh1234");
    await user.click(screen.getByRole("button", { name: "Test connection" }));
    expect(await screen.findByText("Connected as @ann")).toBeInTheDocument();
    await user.click(cont());
    expect(nav.replace).toHaveBeenLastCalledWith("/setup?step=repo");
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
    expect(await screen.findByRole("heading", { name: "Connect GitHub" })).toBeInTheDocument();
  });
});

describe("SetupView with a custom provider (FR21, D21, AC26)", () => {
  it("stays on the github step when Home sends a workspace with a custom provider and no token there (no bounce to model)", async () => {
    mockFetch({ "GET /settings/secrets-status": NONE, "GET /repos": [], [CUSTOM]: [customProvider()] });
    renderWithProviders(<Host initial="github" />);
    expect(await screen.findByRole("heading", { name: "Connect GitHub" })).toBeInTheDocument();
    expect(current()).toHaveTextContent("GitHub token");
    expect(steps()[0]).toHaveTextContent("completed");
    expect(nav.replace).not.toHaveBeenCalled();
  });

  it("Continue works on the model step without a key and moves on to github", async () => {
    mockFetch({ "GET /settings/secrets-status": NONE, "GET /repos": [], [CUSTOM]: [customProvider()] });
    const { user } = renderWithProviders(<Host initial="model" />);
    expect(await screen.findByRole("heading", { name: "Connect a model provider" })).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Use a custom provider" })).toHaveAttribute("href", "/settings/api-keys");
    expect(cont()).toBeEnabled();
    await user.click(cont());
    expect(nav.replace).toHaveBeenLastCalledWith("/setup?step=github");
    expect(await screen.findByRole("heading", { name: "Connect GitHub" })).toBeInTheDocument();
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

    // Token step: a classic token without `repo` shows the warning, still Continue.
    await screen.findByRole("heading", { name: "⟦setup.github.title⟧" });
    await user.click(screen.getByRole("button", { name: "⟦keyField.test⟧" }));
    expect(await screen.findByText("⟦setup.github.missingRepo⟧")).toBeInTheDocument();
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

/* HomeView — the first-run gate on `/` (AC18, FR27): the redirect for each missing prerequisite
   and for a complete setup, the error state with Retry (no redirect), no home content before the
   redirect, and the no-hard-coded-copy check (AC22). AppShell is a passthrough; fetch is stubbed
   and the real hooks run. */
import type React from "react";
import { describe, it, expect, afterEach, beforeEach, vi } from "vitest";
import type { Repo, SecretsStatus } from "@devdigest/shared";
import { renderWithProviders, screen, cleanup, waitFor, pseudoMessages } from "@/test/render";
import { mockFetch, jsonResponse } from "@/test/fetch-mock";
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

const NONE: SecretsStatus = { openai: false, anthropic: false, openrouter: false, github: false };
const secrets = (over: Partial<SecretsStatus>): SecretsStatus => ({ ...NONE, ...over });

describe("HomeView gate", () => {
  it.each([
    ["no model key, no token, no repo", secrets({}), [], "/setup?step=model"],
    ["no model key even with a token and repos", secrets({ github: true }), REPOS, "/setup?step=model"],
    ["a model key but no GitHub token", secrets({ openai: true }), REPOS, "/setup?step=github"],
    ["an anthropic key and a token but no repo", secrets({ anthropic: true, github: true }), [], "/setup?step=repo"],
    // An env-provided key is reported by secrets-status exactly like a stored one (EC11).
    ["only an env-provided openrouter key and a token, with repos", secrets({ openrouter: true, github: true }), REPOS, "/repos/r1/pulls"],
    ["every prerequisite met", secrets({ openai: true, anthropic: true, openrouter: true, github: true }), REPOS, "/repos/r1/pulls"],
  ] as const)("%s → replaces the URL with %s", async (_name, status, repos, href) => {
    mockFetch({ "GET /settings/secrets-status": status, "GET /repos": [...repos] });
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
    });
    const { container } = renderWithProviders(<HomeView />);
    expect(container.querySelector("[aria-busy='true']")).not.toBeNull();
    expect(nav.replace).not.toHaveBeenCalled();

    release(jsonResponse(NONE));
    await waitFor(() => expect(nav.replace).toHaveBeenCalledWith("/setup?step=model"));
    expect(container.querySelector("[aria-busy='true']")).not.toBeNull();
    expect(screen.queryByRole("button")).not.toBeInTheDocument();
    expect(screen.queryByText("No repositories yet")).not.toBeInTheDocument();
  });

  it("offers the first repo while the redirect to its PR list happens", async () => {
    mockFetch({ "GET /settings/secrets-status": secrets({ openai: true, github: true }), "GET /repos": REPOS });
    const { user } = renderWithProviders(<HomeView />);
    await user.click(await screen.findByRole("button", { name: "Open acme/api" }));
    expect(nav.push).toHaveBeenCalledWith("/repos/r1/pulls");
  });

  it.each([
    ["secrets-status", "GET /settings/secrets-status", "GET /repos"],
    ["repos", "GET /repos", "GET /settings/secrets-status"],
  ])("shows an error with Retry and does not redirect when %s fails; Retry recovers", async (_name, failing, other) => {
    let broken = true;
    const ok = failing === "GET /repos" ? [REPOS[0]] : secrets({ openai: true, github: true });
    const otherOk = other === "GET /repos" ? [REPOS[0]] : secrets({ openai: true, github: true });
    mockFetch({
      [failing]: () => (broken ? jsonResponse({ error: { code: "internal", message: "boom" } }, 500) : ok),
      [other]: otherOk,
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
    mockFetch({ "GET /settings/secrets-status": jsonResponse({ error: { code: "x", message: "m" } }, 500), "GET /repos": [] });
    const failed = renderWithProviders(<HomeView />, { messages: pseudoMessages() });
    await screen.findByRole("button", { name: "⟦home.error.retry⟧" });
    expect(strayText(failed.container)).toEqual([]);
    failed.unmount();

    mockFetch({ "GET /settings/secrets-status": secrets({ openai: true, github: true }), "GET /repos": REPOS });
    const ready = renderWithProviders(<HomeView />, { messages: pseudoMessages() });
    await screen.findByRole("button", { name: "⟦home.openRepo⟧" });
    expect(strayText(ready.container)).toEqual([]);
  });
});

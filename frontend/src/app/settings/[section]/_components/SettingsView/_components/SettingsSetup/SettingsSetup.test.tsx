/* SettingsSetup — the first-run flow inline in the settings pane: no page frame, `?step=` read with
   useSearchParams, steps navigated under /settings/setup (router.replace), no hard-coded copy. */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Repo, SecretsStatus } from "@/lib/types";
import { renderWithProviders, screen, cleanup, waitFor, pseudoMessages } from "@/test/render";
import { mockFetch } from "@/test/fetch-mock";
import { strayText } from "@/test/pseudo";

const nav = vi.hoisted(() => ({ replace: vi.fn(), push: vi.fn(), search: "" }));
vi.mock("next/navigation", () => ({
  useRouter: () => nav,
  useSearchParams: () => new URLSearchParams(nav.search),
}));

import { SettingsSetup } from "./SettingsSetup";

beforeEach(() => {
  nav.search = "";
});
afterEach(() => {
  cleanup();
  nav.replace.mockReset();
  nav.push.mockReset();
});

const DONE: SecretsStatus = { openai: false, anthropic: false, openrouter: true, github: true };
const repo = { id: "r1", full_name: "acme/api", default_branch: "main" } as Repo;
const routes = (secrets: SecretsStatus, repos: Repo[]) => ({
  "GET /settings/secrets-status": secrets,
  "GET /settings/custom-providers": [],
  "GET /settings/gitlab-connections": [],
  "GET /repos": repos,
});

describe("SettingsSetup", () => {
  it("renders the flow inline under the section title, honours ?step=repo and Finish opens the PR list", async () => {
    nav.search = "step=repo";
    mockFetch(routes(DONE, [repo]));
    const { user } = renderWithProviders(<SettingsSetup />);

    expect(screen.getByRole("heading", { name: "First-run setup" })).toBeInTheDocument();
    expect(await screen.findByRole("heading", { name: "Add your first repository" })).toBeInTheDocument();
    // Inline: no page frame (no main landmark, no brand header) and the step stays as asked.
    expect(screen.queryByRole("main")).toBeNull();
    expect(screen.queryByText("DevDigest")).toBeNull();
    expect(screen.getByText("Step 3 of 3 · You can change keys later in Settings")).toBeInTheDocument();
    expect(nav.replace).not.toHaveBeenCalled();

    await user.click(screen.getByRole("button", { name: "Finish" }));
    expect(nav.push).toHaveBeenCalledWith("/repos/r1/pulls");
  });

  it("pins the resolved step under /settings/setup and moves between steps there", async () => {
    nav.search = "step=repo";
    mockFetch(routes({ ...DONE, github: false }, []));
    renderWithProviders(<SettingsSetup />);
    expect(await screen.findByRole("heading", { name: "Connect a code host" })).toBeInTheDocument();
    await waitFor(() => expect(nav.replace).toHaveBeenCalledWith("/settings/setup?step=code-host"));
    expect(nav.replace).toHaveBeenCalledTimes(1);
  });

  it("takes every visible string from the message files (pseudo bundle)", async () => {
    nav.search = "step=repo";
    mockFetch(routes(DONE, [repo]));
    const { container } = renderWithProviders(<SettingsSetup />, { messages: pseudoMessages() });
    expect(await screen.findByRole("heading", { name: "⟦setup.repo.title⟧" })).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "⟦settingsSetup.title⟧" })).toBeInTheDocument();
    expect(strayText(container, [/^\d$/, "acme/api", "main"])).toEqual([]);
  });
});

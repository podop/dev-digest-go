/* GithubActionCard — the GitHub Action card of Settings › Integrations (FR2, AC2, EC15, NFR5).
   Rendered alone; useWorkspaceCiInstallations (and, for the dialog, the agent and repo lists)
   run for real against a stubbed fetch. */
import { describe, it, expect, afterEach, vi } from "vitest";
import type { CiRepoEntry, CiRepoInstallations } from "@devdigest/shared";
import { renderWithProviders, screen, cleanup, waitFor, within, pseudoMessages } from "@/test/render";
import { mockFetch, jsonResponse } from "@/test/fetch-mock";
import { strayText } from "@/test/pseudo";
import { AGENT } from "@/test/skill-fixtures";
import { GithubActionCard } from "./GithubActionCard";

const push = vi.fn();
vi.mock("next/navigation", () => ({ useRouter: () => ({ push }) }));

afterEach(() => {
  cleanup();
  push.mockReset();
});

const ROUTE = "GET /ci/installations";
const HOURS_AGO = (h: number) => new Date(Date.now() - h * 3600_000).toISOString();
const inst = (id: string, agent_id: string, agent_name: string) => ({ id, agent_id, agent_name, slug: null, installed_at: HOURS_AGO(48) });

const SINGLE: CiRepoEntry = {
  repo: "acme/api",
  installations: [inst("i1", "ag1", "Security Reviewer")],
  last_run: { status: "succeeded", ran_at: HOURS_AGO(2), github_url: "https://github.com/acme/api/actions/runs/7", agent_id: "ag1" },
};
const DOUBLE: CiRepoEntry = {
  repo: "acme/web",
  installations: [inst("i2", "ag1", "Security Reviewer"), inst("i3", "ag2", "Style Reviewer")],
  last_run: null,
};
const body = (...repos: CiRepoEntry[]): CiRepoInstallations => ({ repos });

describe("GithubActionCard", () => {
  it("shows '2 repos', the last run as text with a safe link, 'no runs yet', and the two Manage forms", async () => {
    mockFetch({ [ROUTE]: body(SINGLE, DOUBLE) });
    const { user } = renderWithProviders(<GithubActionCard />);

    expect(screen.getByRole("status", { name: "Loading installations" })).toBeInTheDocument();
    expect(await screen.findByText("2 repos")).toBeInTheDocument();

    const [first, second] = screen.getAllByRole("listitem");
    expect(within(first!).getByText("acme/api")).toBeInTheDocument();
    const run = within(first!).getByRole("link", { name: "succeeded 2 hours ago" });
    expect(run).toHaveAttribute("href", "https://github.com/acme/api/actions/runs/7");
    expect(run).toHaveAttribute("rel", "noopener noreferrer");
    expect(within(first!).getByRole("link", { name: "Manage acme/api" })).toHaveAttribute("href", "/agents/ag1?tab=ci");

    expect(within(second!).getByText("no runs yet")).toBeInTheDocument();
    expect(within(second!).queryByRole("link", { name: /Manage/ })).not.toBeInTheDocument();
    await user.click(within(second!).getByRole("button", { name: "Manage acme/web" }));
    expect(screen.getByRole("button", { name: "Security Reviewer" })).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Style Reviewer" }));
    expect(push).toHaveBeenCalledWith("/agents/ag2?tab=ci");
  });

  it("never renders an unsafe run URL as a link, and truncates long names behind a native tooltip (EC15)", async () => {
    const longName = `acme/${"very-long-repository-name-".repeat(6)}`;
    mockFetch({
      [ROUTE]: body(
        { ...SINGLE, repo: longName, last_run: { ...SINGLE.last_run!, status: "failed", github_url: "javascript:alert(document.cookie)" } },
      ),
    });
    renderWithProviders(<GithubActionCard />);

    const name = await screen.findByText(longName);
    expect(name).toHaveAttribute("title", longName);
    expect(screen.getByText("failed 2 hours ago")).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: /failed/ })).not.toBeInTheDocument();
    expect(document.querySelector('a[href^="javascript:"]')).toBeNull();
  });

  it("an empty list says 'Not installed in any repository yet' and Install opens the dialog; Escape returns focus to the trigger", async () => {
    mockFetch({ [ROUTE]: body(), "GET /repos": [], "GET /agents": [] });
    const { user } = renderWithProviders(<GithubActionCard />);

    expect(await screen.findByText("Not installed in any repository yet")).toBeInTheDocument();
    expect(screen.getByText("0 repos")).toBeInTheDocument();
    const trigger = screen.getByRole("button", { name: "Install in a repo" });
    await user.click(trigger);
    expect(await screen.findByRole("dialog")).toBeInTheDocument();

    await user.keyboard("{Escape}");
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    expect(trigger).toHaveFocus();
  });

  it("shows the error with Retry, and Retry loads the rows", async () => {
    let fail = true;
    const api = mockFetch({
      [ROUTE]: () => (fail ? jsonResponse({ error: { code: "internal_error", message: "Database is down" } }, 500) : body(SINGLE)),
    });
    const { user } = renderWithProviders(<GithubActionCard />);

    const alert = await screen.findByRole("alert", undefined, { timeout: 5000 });
    expect(alert).toHaveTextContent("Could not load the GitHub Action installations");
    expect(alert).toHaveTextContent("Database is down");
    expect(screen.getByRole("button", { name: "Install in a repo" })).toBeEnabled();

    fail = false;
    await user.click(screen.getByRole("button", { name: "Retry" }));
    expect(await screen.findByText("1 repo")).toBeInTheDocument();
    expect(api.requests("GET", "/ci/installations").length).toBeGreaterThanOrEqual(2);
  });

  it("refreshes the rows after an install from the dialog", async () => {
    let installed = false;
    const api = mockFetch({
      [ROUTE]: () => (installed ? body(SINGLE) : body()),
      "GET /repos": [{ id: "r1", workspace_id: "w", owner: "acme", name: "api", full_name: "acme/api", default_branch: "main", clone_path: null, last_polled_at: null, created_by: null }],
      "GET /agents": [{ ...AGENT, provider: "openrouter" }],
      "POST /agents/:id/export-ci": () => {
        installed = true;
        return jsonResponse({ installation: null, files: [], pr_url: "https://github.com/acme/api/pull/9" }, 201);
      },
    });
    const { user } = renderWithProviders(<GithubActionCard />);
    await screen.findByText("Not installed in any repository yet");

    await user.click(screen.getByRole("button", { name: "Install in a repo" }));
    await user.click(await screen.findByRole("combobox", { name: "Repository" }));
    await user.click(screen.getByRole("option", { name: "acme/api" }));
    await user.click(await screen.findByRole("checkbox", { name: "Security Reviewer" }));
    await user.click(screen.getByRole("button", { name: "Install" }));
    await screen.findByRole("link", { name: "View pull request" });
    await user.click(screen.getByRole("button", { name: "Done" }));

    expect(await screen.findByText("1 repo")).toBeInTheDocument();
    expect(screen.getByText("acme/api")).toBeInTheDocument();
    expect(api.requests("POST", "/agents/ag1/export-ci")).toHaveLength(1);
  });
});

const GITLAB_ENTRY: CiRepoEntry = {
  repo: "grp/svc",
  installations: [inst("i9", "ag1", "Security Reviewer")],
  last_run: { status: "failed", ran_at: HOURS_AGO(3), github_url: "https://gitlab.com/grp/svc/-/jobs/55", agent_id: "ag1" },
  provider: "gitlab",
  host: "gitlab.com",
  target_type: "gitlab_ci",
};
const GITHUB_SAME_NAME: CiRepoEntry = { ...SINGLE, repo: "grp/svc", provider: "github", host: "github.com", target_type: "gha" };

describe("GithubActionCard with GitLab (GitLab C FR45, AC42)", () => {
  it("is titled 'GitHub Action' without a GitLab repository and 'CI reviews' with one", async () => {
    mockFetch({ [ROUTE]: body(SINGLE), "GET /repos": [] });
    renderWithProviders(<GithubActionCard />);
    expect(await screen.findByText("1 repo")).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "GitHub Action" })).toBeInTheDocument();
    cleanup();

    mockFetch({
      [ROUTE]: body(SINGLE),
      "GET /repos": [{ id: "r2", workspace_id: "w", owner: "grp", name: "svc", full_name: "grp/svc", default_branch: "main", clone_path: null, last_polled_at: null, created_by: null, provider: "gitlab", host: "gitlab.com" }],
    });
    renderWithProviders(<GithubActionCard />);
    expect(await screen.findByRole("heading", { name: "CI reviews" })).toBeInTheDocument();
    expect(screen.queryByRole("heading", { name: "GitHub Action" })).not.toBeInTheDocument();
  });

  it("shows the host and 'GitLab CI' on GitLab rows only, keeps a same-named GitHub row apart, and links the job", async () => {
    mockFetch({ [ROUTE]: body(GITHUB_SAME_NAME, GITLAB_ENTRY) });
    renderWithProviders(<GithubActionCard />);

    expect(await screen.findByText("2 repos")).toBeInTheDocument();
    const [github, gitlab] = screen.getAllByRole("listitem");
    expect(within(github!).queryByText("GitLab CI")).not.toBeInTheDocument();
    expect(within(github!).queryByText("gitlab.com")).not.toBeInTheDocument();
    expect(within(gitlab!).getByText("grp/svc")).toBeInTheDocument();
    expect(within(gitlab!).getByText("GitLab CI")).toBeInTheDocument();
    expect(within(gitlab!).getByText("gitlab.com")).toBeInTheDocument();
    expect(within(gitlab!).getByRole("link", { name: "failed 3 hours ago" })).toHaveAttribute(
      "href",
      "https://gitlab.com/grp/svc/-/jobs/55",
    );
    expect(within(gitlab!).getByRole("link", { name: "Manage grp/svc" })).toHaveAttribute("href", "/agents/ag1?tab=ci");
  });
});

describe("GithubActionCard messages (AC22)", () => {
  it("takes every visible string from the message files (pseudo bundle), rows and error state included", async () => {
    mockFetch({ [ROUTE]: body(SINGLE, DOUBLE) });
    renderWithProviders(<GithubActionCard />, { messages: pseudoMessages() });

    await screen.findByText("acme/api");
    // Data: repo names and the relative time Intl words (not copy).
    expect(strayText(document.body, ["acme/api", "acme/web", /ago$/])).toEqual([]);
    cleanup();

    mockFetch({ [ROUTE]: () => jsonResponse({ error: { code: "internal_error", message: "Boom" } }, 500) });
    renderWithProviders(<GithubActionCard />, { messages: pseudoMessages() });
    const alert = await screen.findByRole("alert", undefined, { timeout: 5000 });
    expect(alert).toHaveTextContent("⟦integrationsGithubAction.card.loadError⟧");
    expect(strayText(document.body, ["Boom"])).toEqual([]);
  });

  it("takes the GitLab row and the CI reviews title from the message files (pseudo bundle, GitLab C AC43)", async () => {
    mockFetch({
      [ROUTE]: body(GITLAB_ENTRY),
      "GET /repos": [{ id: "r2", workspace_id: "w", owner: "grp", name: "svc", full_name: "grp/svc", default_branch: "main", clone_path: null, last_polled_at: null, created_by: null, provider: "gitlab", host: "gitlab.com" }],
    });
    renderWithProviders(<GithubActionCard />, { messages: pseudoMessages() });

    await screen.findByText("grp/svc");
    await screen.findByText("⟦integrationsGithubAction.card.titleCiReviews⟧");
    expect(screen.getByText("⟦integrationsGithubAction.card.targetGitlab⟧")).toBeInTheDocument();
    expect(strayText(document.body, ["grp/svc", "gitlab.com", /ago$/])).toEqual([]);
  });
});

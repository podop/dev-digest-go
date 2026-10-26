/* ConnectedRepos — the repository list with its loading / empty / error states, Add in place
   and the Remove entry point (FR19). Rendered alone; hooks run for real against a stubbed fetch. */
import { describe, it, expect, afterEach } from "vitest";
import type { Repo } from "@/lib/types";
import { renderWithProviders, screen, cleanup, waitFor, within, pseudoMessages } from "@/test/render";
import { mockFetch, jsonResponse } from "@/test/fetch-mock";
import { strayText } from "@/test/pseudo";
import { ConnectedRepos } from "./ConnectedRepos";

afterEach(cleanup);

function repo(id: string, fullName: string, branch = "main"): Repo {
  const [owner = "", name = ""] = fullName.split("/");
  return {
    id,
    workspace_id: "w1",
    owner,
    name,
    full_name: fullName,
    default_branch: branch,
    clone_path: null,
    last_polled_at: null,
    created_by: null,
  };
}

function glRepo(id: string, fullName: string, connectionId: string | null = "c1"): Repo {
  return {
    ...repo(id, fullName),
    provider: "gitlab",
    host: "gitlab.com",
    web_url: `https://gitlab.com/${fullName}`,
    connection_id: connectionId,
  };
}

function indexState(repoId: string) {
  return {
    repoId,
    status: "full",
    filesIndexed: 1,
    filesSkipped: 0,
    durationMs: 1,
    lastIndexedSha: "abc",
    indexerVersion: 1,
    updatedAt: new Date().toISOString(),
  };
}

describe("ConnectedRepos", () => {
  it("shows the loading status, then the counted list, and the empty text when there are none", async () => {
    let repos: Repo[] = [repo("r1", "acme/api"), repo("r2", "acme/web", "develop")];
    mockFetch({
      "GET /repos": () => repos,
      "GET /repos/:id/index-state": (req) => indexState(req.params.id!),
    });
    const { queryClient } = renderWithProviders(<ConnectedRepos />);

    expect(screen.getByText("Loading repositories…")).toBeInTheDocument();
    expect(await screen.findByRole("heading", { name: "Connected repositories (2)" })).toBeInTheDocument();
    expect(screen.getAllByRole("listitem")).toHaveLength(2);
    expect(screen.getByText(/Blocking merges is branch protection/)).toBeInTheDocument();

    repos = [];
    await queryClient.invalidateQueries({ queryKey: ["repos"] });
    expect(await screen.findByText("No repositories connected yet.")).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "Connected repositories (0)" })).toBeInTheDocument();
    expect(screen.queryByRole("list")).not.toBeInTheDocument();
  });

  it("shows an error with Retry that reloads the list", async () => {
    let fail = true;
    const api = mockFetch({
      "GET /repos": () => (fail ? jsonResponse({ error: { code: "internal", message: "down" } }, 500) : [repo("r1", "acme/api")]),
      "GET /repos/r1/index-state": indexState("r1"),
    });
    const { user } = renderWithProviders(<ConnectedRepos />);

    expect(await screen.findByText("Could not load the repositories")).toBeInTheDocument();
    expect(screen.queryByRole("list")).not.toBeInTheDocument();

    fail = false;
    await user.click(screen.getByRole("button", { name: "Retry" }));
    expect(await screen.findByText("acme/api")).toBeInTheDocument();
    expect(screen.queryByText("Could not load the repositories")).not.toBeInTheDocument();
    expect(api.requests("GET", "/repos")).toHaveLength(2);
  });

  it("opens Add in place (hiding its button), cancels back, and adds a repository into the list", async () => {
    let repos: Repo[] = [repo("r1", "acme/api")];
    const api = mockFetch({
      "GET /repos": () => repos,
      "GET /repos/:id/index-state": (req) => indexState(req.params.id!),
      "POST /repos": () => {
        repos = [...repos, repo("r2", "acme/new")];
        return repos[1];
      },
    });
    const { user } = renderWithProviders(<ConnectedRepos />);
    await screen.findByRole("heading", { name: "Connected repositories (1)" });

    await user.click(screen.getByRole("button", { name: "Add repository" }));
    expect(screen.getByRole("textbox", { name: "Repository URL" })).toBeInTheDocument();
    expect(screen.getByText("acme/api")).toBeInTheDocument(); // the list stays visible
    await user.click(screen.getByRole("button", { name: "Cancel" }));
    expect(screen.queryByRole("textbox", { name: "Repository URL" })).not.toBeInTheDocument();
    expect(api.requests("POST")).toHaveLength(0);

    await user.click(screen.getByRole("button", { name: "Add repository" }));
    await user.type(screen.getByRole("textbox", { name: "Repository URL" }), "https://github.com/acme/new{Enter}");
    expect(await screen.findByRole("heading", { name: "Connected repositories (2)" })).toBeInTheDocument();
    expect(screen.queryByRole("textbox", { name: "Repository URL" })).not.toBeInTheDocument();
    expect(screen.getByText("acme/new")).toBeInTheDocument();
  });

  it("opens the remove dialog for the clicked row and closes it again", async () => {
    mockFetch({
      "GET /repos": [repo("r1", "acme/api"), repo("r2", "acme/web")],
      "GET /repos/:id/index-state": (req) => indexState(req.params.id!),
    });
    const { user } = renderWithProviders(<ConnectedRepos />);

    await user.click(await screen.findByRole("button", { name: "Remove acme/web" }));
    expect(within(screen.getByRole("dialog")).getByText("Remove acme/web?")).toBeInTheDocument();
    await user.click(within(screen.getByRole("dialog")).getByRole("button", { name: "Cancel" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
  });
});

describe("ConnectedRepos GitLab repositories (FR37, AC29)", () => {
  it("lists only the GitHub repository and counts it; a GitLab one (also not connected) stays out", async () => {
    mockFetch({
      "GET /repos": [repo("r1", "acme/api"), glRepo("r2", "group/sub/proj"), glRepo("r3", "other/proj", null)],
      "GET /repos/:id/index-state": (req) => indexState(req.params.id!),
    });
    renderWithProviders(<ConnectedRepos />);

    expect(await screen.findByRole("heading", { name: "Connected repositories (1)" })).toBeInTheDocument();
    expect(screen.getAllByRole("listitem")).toHaveLength(1);
    expect(screen.getByText("acme/api")).toBeInTheDocument();
    expect(screen.queryByText(/group\/sub\/proj/)).not.toBeInTheDocument();
    expect(screen.queryByText(/other\/proj/)).not.toBeInTheDocument();
    expect(screen.queryByText(/gitlab\.com/)).not.toBeInTheDocument();
  });

  it("shows the empty text when the workspace has only GitLab repositories", async () => {
    mockFetch({ "GET /repos": [glRepo("r2", "group/proj")] });
    renderWithProviders(<ConnectedRepos />);

    expect(await screen.findByText("No repositories connected yet.")).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "Connected repositories (0)" })).toBeInTheDocument();
    expect(screen.queryByRole("list")).not.toBeInTheDocument();
  });
});

describe("ConnectedRepos messages (AC22)", () => {
  it("takes every visible string from the message files (pseudo bundle) in each state", async () => {
    let state: "ok" | "error" | "empty" = "ok";
    mockFetch({
      "GET /repos": () =>
        state === "error"
          ? jsonResponse({ error: { code: "internal", message: "down" } }, 500)
          : state === "empty"
            ? []
            : [repo("r1", "acme/api")],
      "GET /repos/r1/index-state": indexState("r1"),
    });
    const { container, user, queryClient } = renderWithProviders(<ConnectedRepos />, { messages: pseudoMessages() });
    const allowed = ["acme/api", "main"];

    expect(screen.getByText("⟦githubIntegration.repos.loading⟧")).toBeInTheDocument();
    expect(await screen.findByText("⟦githubIntegration.repos.indexed.justNow⟧")).toBeInTheDocument();
    expect(strayText(container, allowed)).toEqual([]);

    await user.click(screen.getByRole("button", { name: "⟦githubIntegration.repos.add⟧" }));
    expect(strayText(container, allowed)).toEqual([]);
    await user.click(screen.getByRole("button", { name: "⟦addRepo.cancel⟧" }));

    state = "empty";
    await queryClient.invalidateQueries({ queryKey: ["repos"] });
    expect(await screen.findByText("⟦githubIntegration.repos.empty⟧")).toBeInTheDocument();
    expect(strayText(container)).toEqual([]);

    state = "error";
    await queryClient.invalidateQueries({ queryKey: ["repos"] });
    expect(await screen.findByText("⟦githubIntegration.repos.loadError⟧")).toBeInTheDocument();
    expect(strayText(container)).toEqual([]);
  });
});

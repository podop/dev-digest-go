/* SettingsGithub — token test (AC16), scopes and the missing-repo rule (FR17, A11), the comment
   identity (FR18), the repository list, add and remove flows (AC17, EC13) and the
   no-hard-coded-copy check (AC22). Hooks run for real against a stubbed fetch. */
import { describe, it, expect, afterEach } from "vitest";
import type { ConnTestResult, Repo } from "@/lib/types";
import { renderWithProviders, screen, cleanup, waitFor, within, pseudoMessages } from "@/test/render";
import { mockFetch } from "@/test/fetch-mock";
import { strayText } from "@/test/pseudo";
import { SettingsGithub } from "./SettingsGithub";

afterEach(cleanup);

const TEST = "/settings/test-connection";
const STATUS_ON = { openrouter: false, openai: false, anthropic: false, github: true };

function result(over: Partial<ConnTestResult> = {}): ConnTestResult {
  return { provider: "github", ok: true, message: "Connected as @ann", login: "ann", scopes: ["repo"], ...over };
}

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

function indexState(repoId: string, sha: string, updatedAt: string) {
  return {
    repoId,
    status: "full",
    filesIndexed: 10,
    filesSkipped: 0,
    durationMs: 5,
    lastIndexedSha: sha,
    indexerVersion: 1,
    updatedAt,
  };
}

const minutesAgo = (n: number) => new Date(Date.now() - n * 60_000).toISOString();

describe("SettingsGithub token", () => {
  it("tests the stored token once on open and shows login, scopes, warning and identity", { timeout: 30_000 }, async () => {
    let next: ConnTestResult = result();
    let release: () => void = () => {};
    const gate = new Promise<void>((res) => (release = res));
    const api = mockFetch({
      "GET /settings/secrets-status": STATUS_ON,
      "GET /repos": [],
      [`POST ${TEST}`]: async () => {
        await gate;
        return next;
      },
    });
    const { user } = renderWithProviders(<SettingsGithub />);

    // AC16: exactly one keyless test on open; before it answers, the identity is generic.
    await waitFor(() => expect(api.requests("POST", TEST)).toHaveLength(1));
    expect(api.requests("POST", TEST)[0]!.body).toEqual({ provider: "github" });
    expect(screen.getByText("Comments are posted as the token's owner.")).toBeInTheDocument();
    release();

    expect(await screen.findByText("Connected as @ann")).toBeInTheDocument();
    expect(screen.getByText("Scopes: repo")).toBeInTheDocument();
    expect(screen.getByText("Comments are posted as @ann.")).toBeInTheDocument();
    expect(screen.queryByText(/no repo scope/)).not.toBeInTheDocument();
    expect(api.requests("POST", TEST)).toHaveLength(1);

    const run = async () => {
      await user.click(screen.getByRole("button", { name: "Test connection" }));
      await waitFor(() => expect(screen.getByRole("button", { name: "Test connection" })).toBeEnabled());
    };

    // A classic token without repo: scopes joined with " · " and the non-blocking warning.
    next = result({ scopes: ["read:org", "gist"] });
    await run();
    expect(await screen.findByText("Scopes: read:org · gist")).toBeInTheDocument();
    expect(screen.getByText(/no repo scope/)).toBeInTheDocument();

    // Fine-grained token: scopes not reported, no warning.
    next = result({ scopes: null });
    await run();
    expect(await screen.findByText("Fine-grained token — scopes are not reported by GitHub")).toBeInTheDocument();
    expect(screen.queryByText(/no repo scope/)).not.toBeInTheDocument();

    // Scopes reported but empty: still a classic token, so the warning applies.
    next = result({ scopes: [] });
    await run();
    expect(await screen.findByText("No scopes granted")).toBeInTheDocument();
    expect(screen.getByText(/no repo scope/)).toBeInTheDocument();

    // A failed test drops the extras and the identity falls back to the generic text.
    next = { provider: "github", ok: false, message: "Bad credentials" };
    await run();
    expect(await screen.findByText("Bad credentials")).toBeInTheDocument();
    expect(screen.queryByText(/^Scopes:/)).not.toBeInTheDocument();
    expect(screen.queryByText(/no repo scope/)).not.toBeInTheDocument();
    expect(screen.getByText("Comments are posted as the token's owner.")).toBeInTheDocument();
  });

  it("does not test when no token is stored", async () => {
    const api = mockFetch({
      "GET /settings/secrets-status": { ...STATUS_ON, github: false },
      "GET /repos": [],
    });
    renderWithProviders(<SettingsGithub />);
    expect(await screen.findByText("No repositories connected yet.")).toBeInTheDocument();
    expect(api.requests("POST", TEST)).toHaveLength(0);
  });
});

describe("SettingsGithub repositories", () => {
  it("lists branch and index state, adds a repository in place and removes one after confirming", { timeout: 30_000 }, async () => {
    let repos = [repo("r1", "acme/api", "main"), repo("r2", "acme/web", "develop")];
    const api = mockFetch({
      "GET /settings/secrets-status": { ...STATUS_ON, github: false },
      "GET /repos": () => repos,
      "GET /repos/r1/index-state": indexState("r1", "abc123", minutesAgo(2)),
      "GET /repos/r2/index-state": indexState("r2", "", "1970-01-01T00:00:00Z"),
      "GET /repos/r3/index-state": indexState("r3", "def456", minutesAgo(0)),
      "POST /repos": () => {
        repos = [...repos, repo("r3", "acme/new")];
        return repos[2];
      },
      "DELETE /repos/:id": (req) => {
        repos = repos.filter((r) => r.id !== req.params.id);
        return { deleted: req.params.id };
      },
    });
    const { user } = renderWithProviders(<SettingsGithub />);

    // AC17: branch plus "indexed 2 min ago" / "not indexed yet".
    expect(await screen.findByRole("heading", { name: "Connected repositories (2)" })).toBeInTheDocument();
    const rows = screen.getAllByRole("listitem");
    expect(within(rows[0]!).getByText("acme/api")).toBeInTheDocument();
    expect(within(rows[0]!).getByText("main")).toBeInTheDocument();
    expect(await within(rows[0]!).findByText("indexed 2 min ago")).toBeInTheDocument();
    expect(within(rows[1]!).getByText("develop")).toBeInTheDocument();
    expect(await within(rows[1]!).findByText("not indexed yet")).toBeInTheDocument();
    expect(screen.getByText(/Blocking merges is branch protection/)).toBeInTheDocument();

    // Add repository opens the form in place; a successful add refreshes the list and closes it.
    await user.click(screen.getByRole("button", { name: "Add repository" }));
    await user.type(screen.getByRole("textbox", { name: "Repository URL" }), "https://github.com/acme/new{Enter}");
    expect(await screen.findByRole("heading", { name: "Connected repositories (3)" })).toBeInTheDocument();
    expect(screen.queryByRole("textbox", { name: "Repository URL" })).not.toBeInTheDocument();
    expect(api.requests("POST", "/repos")[0]!.body).toEqual({ url: "https://github.com/acme/new" });
    expect(await screen.findByText("indexed just now")).toBeInTheDocument();

    // Remove opens a dialog naming the repo and trapping focus; Cancel sends nothing.
    await user.click(screen.getByRole("button", { name: "Remove acme/api" }));
    const dialog = screen.getByRole("dialog");
    expect(within(dialog).getByText("Remove acme/api?")).toBeInTheDocument();
    for (let i = 0; i < 4; i++) {
      await user.tab();
      expect(dialog.contains(document.activeElement)).toBe(true);
    }
    await user.click(within(dialog).getByRole("button", { name: "Cancel" }));
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    expect(api.requests("DELETE")).toHaveLength(0);

    // Confirm (double click) sends one DELETE and the row disappears.
    await user.click(screen.getByRole("button", { name: "Remove acme/api" }));
    await user.dblClick(within(screen.getByRole("dialog")).getByRole("button", { name: /^Remove/ }));
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    expect(api.requests("DELETE")).toHaveLength(1);
    expect(api.requests("DELETE")[0]!.path).toBe("/repos/r1");
    expect(await screen.findByRole("heading", { name: "Connected repositories (2)" })).toBeInTheDocument();
    expect(screen.queryByText("acme/api")).not.toBeInTheDocument();

    // EC13: removing the last repositories leaves the empty state (the home gate then opens /setup).
    for (const name of ["acme/web", "acme/new"]) {
      await user.click(screen.getByRole("button", { name: `Remove ${name}` }));
      await user.click(within(screen.getByRole("dialog")).getByRole("button", { name: /^Remove/ }));
      await waitFor(() => expect(screen.queryByText(name)).not.toBeInTheDocument());
    }
    expect(await screen.findByText("No repositories connected yet.")).toBeInTheDocument();
  });

  it("shows the API message in the dialog when the removal fails and keeps the row", async () => {
    mockFetch({
      "GET /settings/secrets-status": { ...STATUS_ON, github: false },
      "GET /repos": [repo("r1", "acme/api")],
      "GET /repos/r1/index-state": indexState("r1", "abc", minutesAgo(5)),
      "DELETE /repos/r1": new Response(JSON.stringify({ error: { code: "internal", message: "Disk busy" } }), {
        status: 500,
        headers: { "content-type": "application/json" },
      }),
    });
    const { user } = renderWithProviders(<SettingsGithub />);
    await user.click(await screen.findByRole("button", { name: "Remove acme/api" }));
    await user.click(within(screen.getByRole("dialog")).getByRole("button", { name: /^Remove/ }));
    expect(await screen.findByRole("alert")).toHaveTextContent("Disk busy");
    expect(screen.getByRole("dialog")).toBeInTheDocument();
    expect(screen.getByText("acme/api", { selector: "li span" })).toBeInTheDocument();
  });
});

describe("SettingsGithub messages (AC22)", () => {
  it("takes every visible string from the message files (pseudo bundle)", { timeout: 30_000 }, async () => {
    mockFetch({
      "GET /settings/secrets-status": STATUS_ON,
      [`POST ${TEST}`]: result({ scopes: ["read:org"] }),
      "GET /repos": [repo("r1", "acme/api"), repo("r2", "acme/web", "develop")],
      "GET /repos/r1/index-state": indexState("r1", "abc", minutesAgo(2)),
      "GET /repos/r2/index-state": indexState("r2", "", "1970-01-01T00:00:00Z"),
    });
    const { container, user } = renderWithProviders(<SettingsGithub />, { messages: pseudoMessages() });
    const allowed = ["Connected as @ann", "acme/api", "acme/web", "main", "develop"];

    expect(await screen.findByText("⟦githubIntegration.scopes.missingRepo⟧")).toBeInTheDocument();
    expect(await screen.findByText("⟦githubIntegration.repos.indexed.minutes⟧")).toBeInTheDocument();
    expect(await screen.findByText("⟦githubIntegration.repos.notIndexed⟧")).toBeInTheDocument();
    expect(screen.getByText("⟦githubIntegration.scopes.list⟧")).toBeInTheDocument();
    expect(screen.getByText("⟦githubIntegration.identity.login⟧")).toBeInTheDocument();
    expect(strayText(container, allowed)).toEqual([]);

    await user.click(screen.getByRole("button", { name: "⟦githubIntegration.repos.add⟧" }));
    expect(strayText(container, allowed)).toEqual([]);
    await user.click(screen.getAllByRole("button", { name: "⟦githubIntegration.repos.removeLabel⟧" })[0]!);
    expect(strayText(container, allowed)).toEqual([]);
    expect(within(screen.getByRole("dialog")).getByText("⟦githubIntegration.removeDialog.title⟧")).toBeInTheDocument();
  });
});

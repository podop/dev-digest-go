/* SettingsGitlab — the connection list and its first-5 auto test (AC27), the delete flow (AC42), the GitLab
   repositories (FR37, AC29 GitLab half) and the no-hard-coded-copy check (AC35). Hooks run for real against a
   stubbed fetch. */
import { describe, it, expect, afterEach, vi } from "vitest";
import type { ConnTestResult, GitLabConnection, Repo } from "@/lib/types";
import { renderWithProviders, screen, cleanup, waitFor, within, pseudoMessages } from "@/test/render";
import { mockFetch, jsonResponse } from "@/test/fetch-mock";
import { strayText } from "@/test/pseudo";
import { SettingsGitlab } from "./SettingsGitlab";

afterEach(cleanup);
vi.setConfig({ testTimeout: 30_000 });

const BASE = "/settings/gitlab-connections";
const TEST = "/settings/test-connection";

function conn(n: number, over: Partial<GitLabConnection> = {}): GitLabConnection {
  return {
    id: `c${n}`,
    name: `Conn ${n}`,
    base_url: `https://gl${n}.example.com`,
    host: `gl${n}.example.com`,
    token_configured: true,
    repo_count: n,
    created_at: "2026-10-26T00:00:00Z",
    updated_at: "2026-10-26T00:00:00Z",
    ...over,
  };
}

function ok(over: Partial<ConnTestResult> = {}): ConnTestResult {
  return {
    provider: "gitlab",
    ok: true,
    message: "Connected as @ann",
    login: "ann",
    scopes: ["read_api", "read_repository"],
    expires_at: "2026-11-02",
    instance_version: "17.5.1-ee",
    token_access: "read_only",
    warnings: [
      { code: "read_only_token", message: "Read-only token: sync works; posting to merge requests and GitLab CI export will fail" },
      { code: "token_expiring", message: "Token expires on 2026-11-02 (in 7 days)" },
    ],
    ...over,
  };
}

function repo(id: string, fullName: string, over: Partial<Repo> = {}): Repo {
  const parts = fullName.split("/");
  return {
    id,
    workspace_id: "w1",
    owner: parts.slice(0, -1).join("/"),
    name: parts.at(-1) ?? "",
    full_name: fullName,
    default_branch: "main",
    clone_path: null,
    last_polled_at: null,
    created_by: null,
    ...over,
  };
}

const STATE = (id: string) => ({
  repoId: id,
  status: "full",
  filesIndexed: 1,
  filesSkipped: 0,
  durationMs: 1,
  lastIndexedSha: "",
  indexerVersion: 1,
  updatedAt: new Date().toISOString(),
});

describe("SettingsGitlab connections", () => {
  it("lists the rows, tests the first 5 tokens one after another and shows the result with its warnings (AC27)", async () => {
    const list = Array.from({ length: 7 }, (_, i) => conn(i + 1, i === 0 ? { name: "Ann's GitLab", repo_count: 3 } : {}));
    let active = 0;
    let overlap = false;
    const api = mockFetch({
      [`GET ${BASE}`]: list,
      "GET /repos": [],
      [`POST ${TEST}`]: async (req) => {
        active++;
        if (active > 1) overlap = true;
        await new Promise((r) => setTimeout(r, 5));
        active--;
        return req.body && (req.body as { connection_id: string }).connection_id === "c1" ? ok() : ok({ login: "bob", warnings: [] });
      },
    });
    renderWithProviders(<SettingsGitlab />);

    const rows = await screen.findAllByRole("listitem");
    expect(within(rows[0]!).getByText("Ann's GitLab")).toBeInTheDocument();
    expect(within(rows[0]!).getByText("https://gl1.example.com")).toBeInTheDocument();
    expect(within(rows[0]!).getByText("Token saved")).toBeInTheDocument();
    expect(within(rows[0]!).getByText("3 repositories")).toBeInTheDocument();
    expect(within(rows[1]!).getByText("2 repositories")).toBeInTheDocument();

    await waitFor(() => expect(api.requests("POST", TEST)).toHaveLength(5));
    await waitFor(() => expect(screen.getAllByText("Connected as @bob")).toHaveLength(4));
    expect(api.requests("POST", TEST).map((r) => r.body)).toEqual(
      ["c1", "c2", "c3", "c4", "c5"].map((connection_id) => ({ provider: "gitlab", connection_id })),
    );
    expect(overlap).toBe(false);

    const first = rows[0]!;
    expect(within(first).getByText("Connected as @ann")).toBeInTheDocument();
    expect(within(first).getByText("Scopes: read_api · read_repository")).toBeInTheDocument();
    expect(within(first).getByText("Expires 2026-11-02")).toBeInTheDocument();
    expect(within(first).getByText("GitLab 17.5.1-ee")).toBeInTheDocument();
    expect(within(first).getByText("Token access: read-only")).toBeInTheDocument();
    expect(within(first).getAllByRole("status").map((el) => el.textContent)).toEqual([
      "Connected as @ann",
      "Read-only token: sync works; posting to merge requests and GitLab CI export will fail",
      "Token expires on 2026-11-02 (in 7 days)",
    ]);
    // The 6th and 7th were not tested on open.
    expect(within(rows[5]!).queryByText(/Connected as/)).not.toBeInTheDocument();
    expect(api.requests("POST", TEST)).toHaveLength(5);
  });

  it("shows the empty state, a failed test as a Failed badge with its message, and a manual test (AC27)", async () => {
    const api = mockFetch({
      [`GET ${BASE}`]: [conn(1, { token_configured: false }), conn(2)],
      "GET /repos": [],
      [`POST ${TEST}`]: { provider: "gitlab", ok: false, message: "GitLab rejected the token (401): it is wrong, expired or revoked" },
    });
    const { user } = renderWithProviders(<SettingsGitlab />);

    const failed = await screen.findByText("GitLab rejected the token (401): it is wrong, expired or revoked");
    expect(failed).toHaveAttribute("role", "alert");
    const rows = screen.getAllByRole("listitem");
    expect(within(rows[0]!).getByText("No token")).toBeInTheDocument();
    expect(within(rows[1]!).getByText("Failed")).toBeInTheDocument();
    // Only the connection with a token was tested on open.
    expect(api.requests("POST", TEST)).toHaveLength(1);

    await user.click(screen.getByRole("button", { name: "Test Conn 2" }));
    await waitFor(() => expect(api.requests("POST", TEST)).toHaveLength(2));
    await waitFor(() => expect(screen.getByRole("button", { name: "Test Conn 2" })).toBeEnabled());
  });

  it("shows the empty state and a list error with Retry", async () => {
    let body: Response = jsonResponse({ error: { code: "internal_error", message: "boom" } }, 500);
    const api = mockFetch({ [`GET ${BASE}`]: () => body, "GET /repos": [] });
    const { user } = renderWithProviders(<SettingsGitlab />);

    expect(await screen.findByText("Could not load the GitLab connections")).toBeInTheDocument();
    body = jsonResponse([]);
    await user.click(screen.getByRole("button", { name: "Retry" }));
    expect(await screen.findByText("No GitLab connections yet")).toBeInTheDocument();
    expect(api.requests("POST", TEST)).toHaveLength(0);
  });
});

describe("SettingsGitlab delete", () => {
  it("names the connection and the repositories that become not connected; confirming sends DELETE and drops the row (AC42)", async () => {
    let rows: GitLabConnection[] = [conn(1, { name: "Corp", host: "git.corp.example", repo_count: 3, token_configured: false }), conn(2, { repo_count: 0, token_configured: false })];
    const api = mockFetch({
      [`GET ${BASE}`]: () => rows,
      "GET /repos": [],
      [`DELETE ${BASE}/:id`]: (req) => {
        rows = rows.filter((r) => r.id !== req.params.id);
        return { deleted: req.params.id, disconnected_repos: 3 };
      },
    });
    const { user } = renderWithProviders(<SettingsGitlab />);

    await user.click(await screen.findByRole("button", { name: "Delete Corp" }));
    let dialog = screen.getByRole("dialog");
    expect(within(dialog).getByText("Delete Corp?")).toBeInTheDocument();
    expect(within(dialog).getByText(/^3 repositories use git\.corp\.example and become not connected/)).toBeInTheDocument();
    await user.click(within(dialog).getByRole("button", { name: "Delete" }));

    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    expect(api.requests("DELETE", `${BASE}/c1`)).toHaveLength(1);
    await waitFor(() => expect(screen.queryByText("Corp")).not.toBeInTheDocument());

    // No repositories: only the token removal is mentioned.
    await user.click(screen.getByRole("button", { name: "Delete Conn 2" }));
    dialog = screen.getByRole("dialog");
    expect(within(dialog).getByText("Its token is removed.")).toBeInTheDocument();
    expect(within(dialog).queryByText(/not connected/)).not.toBeInTheDocument();
  });

  it("shows the server error in the dialog and keeps the row (AC42)", async () => {
    mockFetch({
      [`GET ${BASE}`]: [conn(1, { name: "Corp", token_configured: false })],
      "GET /repos": [],
      [`DELETE ${BASE}/c1`]: jsonResponse({ error: { code: "internal_error", message: "Database is down" } }, 500),
    });
    const { user } = renderWithProviders(<SettingsGitlab />);

    await user.click(await screen.findByRole("button", { name: "Delete Corp" }));
    const dialog = screen.getByRole("dialog");
    await user.click(within(dialog).getByRole("button", { name: "Delete" }));

    expect(await within(dialog).findByRole("alert")).toHaveTextContent("Database is down");
    await user.click(within(dialog).getByRole("button", { name: "Cancel" }));
    expect(screen.getByText("Corp")).toBeInTheDocument();
  });
});

describe("SettingsGitlab repositories", () => {
  it("lists only GitLab repositories with host, flags a not-connected one and removes with the GitLab copy (FR37, AC29, AC42)", async () => {
    const gl = repo("r1", "acme/platform/payments", { provider: "gitlab", host: "gitlab.com", connection_id: "c1" });
    const orphan = repo("r2", "ops/infra", { provider: "gitlab", host: "git.corp.example", connection_id: null });
    const gh = repo("r3", "acme/web", { provider: "github", host: "github.com" });
    let repos = [gl, orphan, gh];
    const api = mockFetch({
      [`GET ${BASE}`]: [],
      "GET /repos": () => repos,
      "GET /repos/:id/index-state": (req) => STATE(req.params.id!),
      "DELETE /repos/:id": (req) => {
        repos = repos.filter((r) => r.id !== req.params.id);
        return { ok: true };
      },
    });
    const { user } = renderWithProviders(<SettingsGitlab />);

    expect(await screen.findByRole("heading", { name: "GitLab repositories (2)" })).toBeInTheDocument();
    expect(screen.queryByText("acme/web")).not.toBeInTheDocument();
    const rows = screen.getAllByRole("listitem");
    expect(within(rows[0]!).getByText("acme/platform/payments")).toBeInTheDocument();
    expect(within(rows[0]!).getByText("gitlab.com")).toBeInTheDocument();
    expect(within(rows[0]!).queryByText("Not connected")).not.toBeInTheDocument();
    expect(within(rows[1]!).getByText("Not connected")).toBeInTheDocument();
    expect(within(rows[1]!).getByText("Add a GitLab connection for git.corp.example to sync this repository again.")).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "Remove ops/infra" }));
    const dialog = screen.getByRole("dialog");
    expect(within(dialog).getByText("Remove ops/infra · git.corp.example?")).toBeInTheDocument();
    expect(within(dialog).getByText(/Nothing changes on GitLab/)).toBeInTheDocument();
    await user.click(within(dialog).getByRole("button", { name: "Remove" }));
    await waitFor(() => expect(api.requests("DELETE", "/repos/r2")).toHaveLength(1));
    expect(await screen.findByRole("heading", { name: "GitLab repositories (1)" })).toBeInTheDocument();
  });

  it("shows the empty state", async () => {
    mockFetch({ [`GET ${BASE}`]: [], "GET /repos": [repo("r3", "acme/web", { provider: "github", host: "github.com" })] });
    renderWithProviders(<SettingsGitlab />);
    expect(await screen.findByText("No GitLab repositories yet")).toBeInTheDocument();
  });
});

describe("SettingsGitlab copy", () => {
  it("renders no hard-coded copy (AC35)", async () => {
    mockFetch({
      [`GET ${BASE}`]: [conn(1, { token_configured: false }), conn(2)],
      "GET /repos": [
        repo("r1", "acme/payments", { provider: "gitlab", host: "gitlab.com", connection_id: "c2" }),
        repo("r2", "ops/infra", { provider: "gitlab", host: "git.corp.example", connection_id: null }),
      ],
      "GET /repos/:id/index-state": (req) => STATE(req.params.id!),
      [`POST ${TEST}`]: ok({ warnings: [] }),
    });
    const { container } = renderWithProviders(<SettingsGitlab />, { messages: pseudoMessages() });

    await screen.findByText("ops/infra");
    await waitFor(() => expect(container.querySelector("[role=status]")).not.toBeNull());
    // Data is not copy: names, URLs, hosts, branches and repository names.
    const data = [/^Conn \d$/, /^https:\/\/gl\d\.example\.com$/, "acme/payments", "ops/infra", "gitlab.com", "git.corp.example", "main"];
    expect(strayText(container, data)).toEqual([]);
  });
});

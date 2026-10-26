/* CodeHostStep — setup step 2 (FR38, AC30): a GitHub token (Settings A FR24, unchanged) or a GitLab
   connection; fetch is stubbed and the real hooks run. */
import { afterEach, describe, expect, it, vi } from "vitest";
import type { ConnTestResult, GitLabConnection, SecretsStatus } from "@/lib/types";
import { renderWithProviders, screen, cleanup, waitFor, pseudoMessages } from "@/test/render";
import { mockFetch, jsonResponse } from "@/test/fetch-mock";
import { strayText } from "@/test/pseudo";
import { CodeHostStep } from "./CodeHostStep";

afterEach(cleanup);

const NONE: SecretsStatus = { openai: false, anthropic: false, openrouter: false, github: false };
const CONNS = "/settings/gitlab-connections";
const cont = () => screen.getByRole("button", { name: "Continue" });
const runTest = (user: ReturnType<typeof renderWithProviders>["user"]) =>
  user.click(screen.getByRole("button", { name: "Test connection" }));

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

function mockGithub(over: Partial<ConnTestResult>) {
  mockFetch({ "POST /settings/test-connection": { provider: "github", ok: true, message: "OK", ...over } });
}

const gitlabResult = (over: Partial<ConnTestResult> = {}): ConnTestResult => ({
  provider: "gitlab",
  ok: true,
  message: "Connected as @ann",
  login: "ann",
  scopes: ["api", "read_repository"],
  expires_at: "2027-01-31",
  instance_version: "17.5.1-ee",
  token_access: "full",
  warnings: [],
  ...over,
});

describe("CodeHostStep — GitHub (FR24 kept by FR38)", () => {
  it("opens on GitHub; shows the login and scopes after a passing test, then enables Continue and Back (AC20)", async () => {
    mockGithub({ login: "ann", scopes: ["repo", "read:org"] });
    const onBack = vi.fn();
    const onContinue = vi.fn();
    const onTested = vi.fn();
    const { user } = renderWithProviders(
      <CodeHostStep secrets={NONE} gitlabConnections={[]} onTested={onTested} onBack={onBack} onContinue={onContinue} />,
    );
    expect(screen.getByRole("heading", { name: "Connect a code host" })).toBeInTheDocument();
    expect(screen.getByRole("radio", { name: "GitHub" })).toBeChecked();
    expect(screen.queryByText("Already connected")).toBeNull();
    expect(cont()).toBeDisabled();

    await runTest(user);
    expect(await screen.findByText("Connected as @ann")).toBeInTheDocument();
    expect(screen.getByText("Scopes: repo · read:org")).toBeInTheDocument();
    expect(screen.queryByText(/no repo scope/)).toBeNull();
    expect(cont()).toBeEnabled();
    expect(onTested).toHaveBeenCalledWith("github");

    await user.click(cont());
    await user.click(screen.getByRole("button", { name: "Back" }));
    expect(onContinue).toHaveBeenCalledTimes(1);
    expect(onBack).toHaveBeenCalledTimes(1);
  });

  it("warns about a classic token without repo but does not block Continue; notes fine-grained and empty scope lists (FR24)", async () => {
    mockGithub({ login: "ann", scopes: ["gist"] });
    const { user, unmount } = renderWithProviders(<CodeHostStep secrets={NONE} gitlabConnections={[]} onTested={vi.fn()} />);
    await runTest(user);
    expect(await screen.findByText(/This token has no repo scope/)).toBeInTheDocument();
    expect(cont()).toBeEnabled();
    unmount();

    mockGithub({ login: "ann", scopes: null });
    const fine = renderWithProviders(<CodeHostStep secrets={NONE} gitlabConnections={[]} onTested={vi.fn()} />);
    await runTest(fine.user);
    expect(await screen.findByText("Fine-grained token — scopes are not reported by GitHub")).toBeInTheDocument();
    expect(screen.queryByText(/no repo scope/)).toBeNull();
    fine.unmount();

    mockGithub({ login: "bob", scopes: [] });
    const none = renderWithProviders(<CodeHostStep secrets={NONE} gitlabConnections={[]} onTested={vi.fn()} />);
    await runTest(none.user);
    expect(await screen.findByText("Scopes: none")).toBeInTheDocument();
  });

  it("accepts a stored token, and blocks again after a failed test", async () => {
    mockFetch({ "POST /settings/test-connection": { provider: "github", ok: false, message: "Bad credentials" } });
    const { user } = renderWithProviders(
      <CodeHostStep secrets={{ ...NONE, github: true }} gitlabConnections={[]} onTested={vi.fn()} />,
    );
    expect(screen.getByText("Already connected")).toBeInTheDocument();
    expect(cont()).toBeEnabled();

    await runTest(user);
    expect(await screen.findByText("Bad credentials")).toBeInTheDocument();
    await waitFor(() => expect(cont()).toBeDisabled());
  });
});

describe("CodeHostStep — GitLab (FR38, AC30)", () => {
  it("Save and test creates the connection, tests it, shows the result and enables Continue", async () => {
    const api = mockFetch({
      [`POST ${CONNS}`]: connection({ id: "c9" }),
      "POST /settings/test-connection": gitlabResult({
        warnings: [{ code: "read_only_token", message: "Read-only token: sync works; posting to merge requests and GitLab CI export will fail" }],
      }),
    });
    const onTested = vi.fn();
    const { user } = renderWithProviders(<CodeHostStep secrets={NONE} gitlabConnections={[]} onTested={onTested} />);
    await user.click(screen.getByRole("radio", { name: "GitLab" }));

    expect(screen.getByRole("textbox", { name: "GitLab base URL" })).toHaveValue("https://gitlab.com");
    expect(screen.getByRole("link", { name: "Create a token" })).toHaveAttribute(
      "href",
      "https://gitlab.com/-/user_settings/personal_access_tokens?name=DevDigest&scopes=api,read_repository",
    );
    expect(cont()).toBeDisabled();
    expect(screen.queryByText("Already connected")).toBeNull();

    await user.type(screen.getByRole("textbox", { name: "Personal access token" }), "glpat-abcdefghij1234567890");
    await user.click(screen.getByRole("button", { name: "Save and test" }));

    expect(await screen.findByText("Connected as @ann")).toBeInTheDocument();
    expect(screen.getByText("Scopes: api · read_repository")).toBeInTheDocument();
    expect(screen.getByText("Expires 2027-01-31")).toBeInTheDocument();
    expect(screen.getByText("GitLab 17.5.1-ee")).toBeInTheDocument();
    expect(screen.getByText(/^Read-only token: sync works/)).toBeInTheDocument();
    expect(cont()).toBeEnabled();
    expect(onTested).toHaveBeenCalledWith("gitlab");

    // POST first (the token goes out once, write-only), then the test by connection id without the key.
    expect(api.requests("POST", CONNS).map((r) => r.body)).toEqual([
      { base_url: "https://gitlab.com", token: "glpat-abcdefghij1234567890" },
    ]);
    expect(api.requests("POST", "/settings/test-connection").map((r) => r.body)).toEqual([
      { provider: "gitlab", connection_id: "c9" },
    ]);
    expect(api.requests("PUT", `${CONNS}/c9`)).toHaveLength(0);
  });

  it("updates the connection of a host that already has one, and re-tests a saved token without saving", async () => {
    const api = mockFetch({
      [`PUT ${CONNS}/:id`]: connection(),
      "POST /settings/test-connection": gitlabResult({ scopes: null, expires_at: null, instance_version: null }),
    });
    const { user } = renderWithProviders(
      <CodeHostStep secrets={NONE} gitlabConnections={[connection()]} onTested={vi.fn()} />,
    );
    // Only GitLab is connected: the step opens on it, with the stored state.
    expect(screen.getByRole("radio", { name: "GitLab" })).toBeChecked();
    expect(screen.getByText("Already connected")).toBeInTheDocument();
    expect(cont()).toBeEnabled();
    expect(screen.getByPlaceholderText("Token saved — paste a new token to replace it")).toBeInTheDocument();

    // A blank token re-tests the saved one: no save.
    await user.click(screen.getByRole("button", { name: "Save and test" }));
    expect(await screen.findByText("Scopes not reported")).toBeInTheDocument();
    expect(screen.getByText("No expiry date")).toBeInTheDocument();
    expect(screen.getByText("Version unknown")).toBeInTheDocument();
    expect(api.requests("PUT", `${CONNS}/c1`)).toHaveLength(0);
    expect(api.requests("POST", CONNS)).toHaveLength(0);

    // A new token for the same host (typed in another case) replaces it via PUT.
    const url = screen.getByRole("textbox", { name: "GitLab base URL" });
    await user.clear(url);
    await user.type(url, "https://GitLab.com");
    await user.type(screen.getByRole("textbox", { name: "Personal access token" }), "glpat-newtoken1234567890ab");
    await user.click(screen.getByRole("button", { name: "Save and test" }));
    await waitFor(() => expect(api.requests("PUT", `${CONNS}/c1`)).toHaveLength(1));
    expect(api.requests("PUT", `${CONNS}/c1`)[0]!.body).toEqual({ base_url: "https://GitLab.com", token: "glpat-newtoken1234567890ab" });
    expect(api.requests("POST", CONNS)).toHaveLength(0);
  });

  it("shows save errors once, runs no test, and needs a token for a new connection", async () => {
    const api = mockFetch({
      [`POST ${CONNS}`]: jsonResponse(
        { error: { code: "gitlab_connection_exists", message: "A GitLab connection for gitlab.com already exists" } },
        409,
      ),
    });
    const { user } = renderWithProviders(<CodeHostStep secrets={NONE} gitlabConnections={[]} onTested={vi.fn()} />);
    await user.click(screen.getByRole("radio", { name: "GitLab" }));

    await user.click(screen.getByRole("button", { name: "Save and test" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("Paste a personal access token");
    expect(api.requests("POST", CONNS)).toHaveLength(0);

    await user.type(screen.getByRole("textbox", { name: "Personal access token" }), "glpat-abcdefghij1234567890");
    await user.click(screen.getByRole("button", { name: "Save and test" }));
    expect(await screen.findByText("A GitLab connection for gitlab.com already exists")).toBeInTheDocument();
    expect(screen.getByRole("textbox", { name: "GitLab base URL" })).toHaveAttribute("aria-invalid", "true");
    expect(screen.getAllByRole("alert")).toHaveLength(1);
    expect(api.requests("POST", "/settings/test-connection")).toHaveLength(0);
    expect(cont()).toBeDisabled();
  });

  it("a failed test shows the error and blocks Continue; a stored GitLab token is blocked again too", async () => {
    mockFetch({
      "POST /settings/test-connection": gitlabResult({
        ok: false,
        message: "GitLab rejected the token (401): it is wrong, expired or revoked",
        login: null,
        scopes: null,
        expires_at: null,
        instance_version: null,
        token_access: null,
      }),
    });
    const onTested = vi.fn();
    const { user } = renderWithProviders(
      <CodeHostStep secrets={NONE} gitlabConnections={[connection()]} onTested={onTested} />,
    );
    expect(cont()).toBeEnabled();
    await user.click(screen.getByRole("button", { name: "Save and test" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("GitLab rejected the token (401)");
    await waitFor(() => expect(cont()).toBeDisabled());
    expect(onTested).not.toHaveBeenCalled();
  });

  it("keeps each host's last result when switching, and a GitHub token still lets Continue through", async () => {
    mockFetch({ "POST /settings/test-connection": { provider: "github", ok: true, message: "OK", login: "ann", scopes: ["repo"] } });
    const { user } = renderWithProviders(<CodeHostStep secrets={NONE} gitlabConnections={[]} onTested={vi.fn()} />);
    await runTest(user);
    expect(await screen.findByText("Connected as @ann")).toBeInTheDocument();

    await user.click(screen.getByRole("radio", { name: "GitLab" }));
    expect(screen.queryByText("Connected as @ann")).toBeNull();
    expect(cont()).toBeEnabled();
    await user.click(screen.getByRole("radio", { name: "GitHub" }));
    expect(screen.getByText("Connected as @ann")).toBeInTheDocument();
  });

  it("takes every visible string from the message files (pseudo bundle)", async () => {
    mockFetch({
      [`POST ${CONNS}`]: connection({ id: "c9" }),
      "POST /settings/test-connection": gitlabResult({
        warnings: [{ code: "version_unknown", message: "Version warning from the server" }],
      }),
    });
    const { container, user } = renderWithProviders(
      <CodeHostStep secrets={NONE} gitlabConnections={[]} onTested={vi.fn()} />,
      { messages: pseudoMessages() },
    );
    // Server data shown as is: the warning messages of the test answer.
    const allowed = ["Version warning from the server"];
    expect(strayText(container, allowed)).toEqual([]);
    await user.click(screen.getByRole("radio", { name: "⟦setup.codeHost.hosts.gitlab⟧" }));
    await user.type(screen.getByRole("textbox", { name: "⟦setup.codeHost.gitlab.tokenLabel⟧" }), "glpat-abcdefghij1234567890");
    await user.click(screen.getByRole("button", { name: "⟦setup.codeHost.gitlab.saveAndTest⟧" }));
    expect(await screen.findByText("⟦setup.codeHost.connectedAs⟧")).toBeInTheDocument();
    expect(screen.getByText("⟦setup.codeHost.gitlab.scopes⟧")).toBeInTheDocument();
    expect(strayText(container, allowed)).toEqual([]);
  });
});

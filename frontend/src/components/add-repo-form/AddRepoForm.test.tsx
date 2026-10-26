import { describe, it, expect, afterEach, vi } from "vitest";
import { renderWithProviders, screen, cleanup, waitFor, pseudoMessages } from "@/test/render";
import { mockFetch, jsonResponse } from "@/test/fetch-mock";
import { strayText } from "@/test/pseudo";
import { REPO_URL_INVALID_MESSAGE } from "@devdigest/shared";
import { AddRepoForm } from "./AddRepoForm";

afterEach(cleanup);

const URL_PLACEHOLDER = "https://github.com/owner/repo or https://gitlab.com/group/project";

describe("AddRepoForm", () => {
  it("posts the trimmed URL on submit (button or Enter), calls onAdded with the repo and clears the field", async () => {
    const api = mockFetch({ "POST /repos": { id: "r9", full_name: "acme/api" } });
    const onAdded = vi.fn();
    const { user } = renderWithProviders(<AddRepoForm onAdded={onAdded} />);
    const input = screen.getByRole("textbox", { name: "Repository URL" });
    const submit = screen.getByRole("button", { name: "Add repository" });
    expect(submit).toBeDisabled();

    await user.type(input, "  https://github.com/acme/api {Enter}");
    await waitFor(() => expect(onAdded).toHaveBeenCalledTimes(1));
    expect(onAdded).toHaveBeenCalledWith(expect.objectContaining({ id: "r9", full_name: "acme/api" }));
    expect(api.requests("POST", "/repos")[0]!.body).toEqual({ url: "https://github.com/acme/api" });
    expect(input).toHaveValue("");
    expect(screen.getByRole("button", { name: "Add repository" })).toBeDisabled();
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  });

  it("shows the API message, keeps the URL, refocuses the field and does not call onAdded (EC15)", async () => {
    mockFetch({ "POST /repos": jsonResponse({ error: { code: "conflict", message: "Repository already added" } }, 409) });
    const onAdded = vi.fn();
    const { user } = renderWithProviders(<AddRepoForm onAdded={onAdded} />);
    const input = screen.getByRole("textbox", { name: "Repository URL" });
    expect(input).toHaveFocus(); // focused on open

    await user.type(input, "https://github.com/acme/api");
    await user.click(screen.getByRole("button", { name: "Add repository" }));

    expect(await screen.findByRole("alert")).toHaveTextContent("Repository already added");
    expect(input).toHaveValue("https://github.com/acme/api");
    expect(input).toHaveAttribute("aria-invalid", "true");
    expect(input).toHaveFocus(); // back on the field so the URL can be fixed
    expect(onAdded).not.toHaveBeenCalled();
  });

  it("shows the engine-unreachable message when the request cannot be sent", async () => {
    vi.stubGlobal("fetch", () => Promise.reject(new TypeError("network down")));
    const { user } = renderWithProviders(<AddRepoForm onAdded={vi.fn()} />);
    await user.type(screen.getByPlaceholderText(URL_PLACEHOLDER), "https://github.com/x/y");
    await user.click(screen.getByRole("button", { name: "Add repository" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("Cannot reach the DevDigest engine");
  });

  it("shows Cancel only when onCancel is given, and Cancel neither submits nor adds", async () => {
    const api = mockFetch({ "POST /repos": { id: "r1" } });
    const first = renderWithProviders(<AddRepoForm onAdded={vi.fn()} />);
    expect(screen.queryByRole("button", { name: "Cancel" })).not.toBeInTheDocument();
    first.unmount();

    const onCancel = vi.fn();
    const { user } = renderWithProviders(<AddRepoForm onAdded={vi.fn()} onCancel={onCancel} />);
    await user.type(screen.getByRole("textbox", { name: "Repository URL" }), "https://github.com/a/b");
    await user.click(screen.getByRole("button", { name: "Cancel" }));
    expect(onCancel).toHaveBeenCalledTimes(1);
    expect(api.requests("POST", "/repos")).toHaveLength(0);
  });

  it("disables the button while the add runs, so a double click sends one request", async () => {
    let release: (r: Response) => void = () => {};
    const api = mockFetch({ "POST /repos": () => new Promise<Response>((res) => (release = res)) });
    const { user } = renderWithProviders(<AddRepoForm onAdded={vi.fn()} />);
    await user.type(screen.getByRole("textbox", { name: "Repository URL" }), "https://github.com/a/b");
    await user.dblClick(screen.getByRole("button", { name: "Add repository" }));
    expect(await screen.findByRole("button", { name: "Cloning…" })).toBeDisabled();
    expect(api.requests("POST", "/repos")).toHaveLength(1);
    release(jsonResponse({ id: "r1" }));
    await waitFor(() => expect(screen.getByRole("button", { name: "Add repository" })).toBeInTheDocument());
  });

  it("posts a GitLab URL (https with subgroups, and git@) as typed, trimmed", async () => {
    const api = mockFetch({ "POST /repos": { id: "r2", full_name: "acme/platform/payments", provider: "gitlab" } });
    const onAdded = vi.fn();
    const { user } = renderWithProviders(<AddRepoForm onAdded={onAdded} />);
    const input = screen.getByRole("textbox", { name: "Repository URL" });
    await user.type(input, " https://gitlab.com/acme/platform/payments.git {Enter}");
    await waitFor(() => expect(onAdded).toHaveBeenCalledTimes(1));
    await user.type(input, "git@git.corp.example:acme/web.git{Enter}");
    await waitFor(() => expect(onAdded).toHaveBeenCalledTimes(2));
    expect(api.requests("POST", "/repos").map((r) => r.body)).toEqual([
      { url: "https://gitlab.com/acme/platform/payments.git" },
      { url: "git@git.corp.example:acme/web.git" },
    ]);
  });

  it("says where the repository appears when it belongs to another section than the form's (FR37, FR40)", async () => {
    const answers = [
      { id: "r2", provider: "gitlab", host: "git.corp.example:8443", full_name: "acme/web" },
      { id: "r3", provider: "gitlab", host: "gitlab.com", full_name: "acme/web" },
      { id: "r4", provider: "github", host: "github.com", full_name: "acme/api" },
    ];
    mockFetch({ "POST /repos": () => answers.shift() });
    const onAdded = vi.fn();
    // GitHub section (default): a GitLab add is announced, a GitHub add is not.
    const first = renderWithProviders(<AddRepoForm onAdded={onAdded} />);
    await first.user.type(screen.getByRole("textbox", { name: "Repository URL" }), "https://git.corp.example:8443/acme/web{Enter}");
    expect(await screen.findByText("Added to GitLab · git.corp.example:8443")).toBeInTheDocument();
    first.unmount();
    cleanup();

    // GitLab section: its own add is silent, a GitHub add is announced.
    const second = renderWithProviders(<AddRepoForm onAdded={onAdded} section="gitlab" />);
    const input = screen.getByRole("textbox", { name: "Repository URL" });
    await second.user.type(input, "https://gitlab.com/acme/web{Enter}");
    await waitFor(() => expect(onAdded).toHaveBeenCalledTimes(2));
    expect(screen.queryByText(/^Added to/)).not.toBeInTheDocument();
    await second.user.type(input, "https://github.com/acme/api{Enter}");
    expect(await screen.findByText("Added to GitHub · github.com")).toBeInTheDocument();
  });

  it("rejects a URL parseRepoUrl refuses with the FR12 message, without a request, and keeps the text", async () => {
    const api = mockFetch({ "POST /repos": { id: "r1" } });
    const onAdded = vi.fn();
    const { user } = renderWithProviders(<AddRepoForm onAdded={onAdded} />);
    const input = screen.getByRole("textbox", { name: "Repository URL" });
    await user.type(input, "http://gitlab.com/acme/web{Enter}");

    expect(await screen.findByRole("alert")).toHaveTextContent(REPO_URL_INVALID_MESSAGE);
    expect(input).toHaveValue("http://gitlab.com/acme/web");
    expect(input).toHaveAttribute("aria-invalid", "true");
    expect(input).toHaveFocus();
    expect(api.requests("POST", "/repos")).toHaveLength(0);
    expect(onAdded).not.toHaveBeenCalled();
  });

  it("repo_host_not_connected shows the host and a link to the GitLab section, once (AC31)", async () => {
    const api = mockFetch({
      "POST /repos": jsonResponse(
        {
          error: {
            code: "repo_host_not_connected",
            message: "No GitLab connection for git.corp.example:8443. Add one in Settings → GitLab Integration.",
            details: { host: "git.corp.example:8443" },
          },
        },
        422,
      ),
    });
    const { user } = renderWithProviders(<AddRepoForm onAdded={vi.fn()} />);
    await user.type(screen.getByRole("textbox", { name: "Repository URL" }), "https://git.corp.example:8443/acme/web");
    await user.click(screen.getByRole("button", { name: "Add repository" }));

    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent("No GitLab connection for git.corp.example:8443.");
    expect(screen.getAllByRole("alert")).toHaveLength(1);
    expect(screen.getByRole("link", { name: "Add a GitLab connection" })).toHaveAttribute("href", "/settings/gitlab");
    expect(api.requests("POST", "/repos")).toHaveLength(1);
  });

  it.each([
    [422, "gitlab_project_not_found", "Project acme/web was not found on gitlab.com, or the token cannot see it"],
    [422, "gitlab_project_not_found", "GitLab reports that acme/web has moved; paste the project's current URL"],
    [422, "gitlab_permission_denied", "GitLab rejected the token of connection Corp"],
    [409, "gitlab_path_conflict", "acme/web in this workspace is a different GitLab project that was renamed or moved; sync it first"],
    [502, "gitlab_rate_limited", "GitLab is rate limiting requests, try again later"],
    [502, "gitlab_unavailable", "GitLab is unavailable, try again later"],
  ])("shows the %i %s message once, with no link", async (status, code, message) => {
    mockFetch({ "POST /repos": jsonResponse({ error: { code, message, details: { repo_id: "r7" } } }, status) });
    const { user } = renderWithProviders(<AddRepoForm onAdded={vi.fn()} />);
    await user.type(screen.getByRole("textbox", { name: "Repository URL" }), "https://gitlab.com/acme/web");
    await user.click(screen.getByRole("button", { name: "Add repository" }));

    expect(await screen.findByRole("alert")).toHaveTextContent(message);
    expect(screen.getAllByRole("alert")).toHaveLength(1);
    expect(screen.queryByRole("link")).not.toBeInTheDocument();
  });

  it("shows a generic text for a plain 5xx instead of the raw server message", async () => {
    mockFetch({ "POST /repos": jsonResponse({ error: { code: "internal", message: "pq: connection refused" } }, 500) });
    const { user } = renderWithProviders(<AddRepoForm onAdded={vi.fn()} />);
    await user.type(screen.getByRole("textbox", { name: "Repository URL" }), "https://github.com/acme/api");
    await user.click(screen.getByRole("button", { name: "Add repository" }));

    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent("The server could not add the repository. Try again in a moment.");
    expect(alert).not.toHaveTextContent("pq:");
  });

  it("takes every visible string from the message files (pseudo bundle, AC22)", async () => {
    const api = mockFetch({
      "POST /repos": jsonResponse(
        { error: { code: "repo_host_not_connected", message: "No GitLab connection", details: { host: "gitlab.example" } } },
        422,
      ),
    });
    const { container, user } = renderWithProviders(<AddRepoForm onAdded={vi.fn()} onCancel={vi.fn()} />, {
      messages: pseudoMessages(),
    });
    expect(strayText(container)).toEqual([]);
    expect(screen.getByRole("button", { name: "⟦addRepo.cancel⟧" })).toBeInTheDocument();
    expect(screen.getByRole("textbox", { name: "⟦addRepo.urlLabel⟧" })).toBeInTheDocument();
    expect(screen.getByText("⟦addRepo.urlHint⟧")).toBeInTheDocument();

    // A URL the parser refuses: the FR12 text is shared-contract data, everything else is a marker.
    const input = screen.getByPlaceholderText("⟦addRepo.urlPlaceholder⟧");
    await user.type(input, "x");
    await user.click(screen.getByRole("button", { name: "⟦addRepo.submit⟧" }));
    expect(await screen.findByText(REPO_URL_INVALID_MESSAGE)).toBeInTheDocument();
    expect(strayText(container, [REPO_URL_INVALID_MESSAGE])).toEqual([]);

    // Not connected: host line and link both come from the message files.
    await user.clear(input);
    await user.type(input, "https://gitlab.example/acme/web");
    await user.click(screen.getByRole("button", { name: "⟦addRepo.submit⟧" }));
    expect(await screen.findByRole("link", { name: "⟦addRepo.addConnection⟧" })).toBeInTheDocument();
    expect(screen.getByRole("alert")).toHaveTextContent("⟦addRepo.hostNotConnected⟧");
    expect(strayText(container)).toEqual([]);
    expect(api.requests("POST", "/repos")).toHaveLength(1);
  });
});

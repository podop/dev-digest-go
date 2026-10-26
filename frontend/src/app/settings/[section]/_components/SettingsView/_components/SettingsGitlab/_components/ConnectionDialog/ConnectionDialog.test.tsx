/* ConnectionDialog — add / edit flow (AC28, FR35): defaults, masked write-only token, "Create a token" link, Save
   vs Save and test, 422 / 409 at their field, "Remove token", focus (NFR12) and hard-coded copy (AC35). */
import { describe, it, expect, afterEach, vi } from "vitest";
import type { GitLabConnection } from "@/lib/types";
import { renderWithProviders, screen, cleanup, waitFor, pseudoMessages } from "@/test/render";
import { mockFetch, jsonResponse } from "@/test/fetch-mock";
import { strayText } from "@/test/pseudo";
import { ConnectionDialog } from "./ConnectionDialog";

afterEach(cleanup);
vi.setConfig({ testTimeout: 30_000 });

const BASE = "/settings/gitlab-connections";
const TEST = "/settings/test-connection";
const SAVED: GitLabConnection = {
  id: "c1",
  name: "gitlab.com",
  base_url: "https://gitlab.com",
  host: "gitlab.com",
  token_configured: true,
  repo_count: 0,
  created_at: "2026-10-26T00:00:00Z",
  updated_at: "2026-10-26T00:00:00Z",
};
const TEST_OK = { provider: "gitlab", ok: true, message: "Connected as @ann", login: "ann", scopes: ["api"], expires_at: null, instance_version: "17.5.1", token_access: "full", warnings: [] };

const url = () => screen.getByRole("textbox", { name: "Base URL" });
const name = () => screen.getByRole("textbox", { name: "Name" });
const token = () => screen.getByRole("textbox", { name: "Token" });
const saveBtn = () => screen.getByRole("button", { name: "Save" });

/** A 422 in the server's shape (httpx.NewValidationError). */
function validation(path: string[], message: string) {
  const instancePath = `/${path.join("/")}`;
  const details = [{ keyword: "custom", instancePath, schemaPath: `#${instancePath}/custom`, message, params: { issue: { code: "custom", path, message } } }];
  return jsonResponse({ error: { code: "validation_error", message: "Request validation failed", details } }, 422);
}

describe("ConnectionDialog add", () => {
  it("defaults the base URL, masks the token, links the token page, sends POST on Save and closes (AC28)", async () => {
    const api = mockFetch({ [`POST ${BASE}`]: SAVED });
    const onClose = vi.fn();
    const { user } = renderWithProviders(<ConnectionDialog onClose={onClose} onTested={vi.fn()} />);

    expect(url()).toHaveValue("https://gitlab.com");
    expect(name()).toHaveAttribute("placeholder", "gitlab.com");
    expect(token()).toHaveAttribute("placeholder", "Paste a token");
    expect(screen.getByRole("link", { name: /Create a token/ })).toHaveAttribute(
      "href",
      "https://gitlab.com/-/user_settings/personal_access_tokens?name=DevDigest&scopes=api,read_repository",
    );
    expect(screen.getByRole("link", { name: /Create a token/ })).toHaveAttribute("target", "_blank");

    await user.clear(url());
    await user.type(url(), "https://git.corp.example:8443/gitlab/");
    expect(name()).toHaveAttribute("placeholder", "git.corp.example:8443");
    expect(screen.getByRole("link", { name: /Create a token/ })).toHaveAttribute(
      "href",
      "https://git.corp.example:8443/gitlab/-/user_settings/personal_access_tokens?name=DevDigest&scopes=api,read_repository",
    );
    await user.type(token(), "glpat-abcdefghij1234567890");
    expect(token()).toHaveValue(`${"•".repeat(22)}7890`);
    await user.click(saveBtn());

    await waitFor(() => expect(onClose).toHaveBeenCalledTimes(1));
    expect(api.requests("POST", BASE)).toHaveLength(1);
    expect(api.requests("POST", BASE)[0]!.body).toEqual({
      base_url: "https://git.corp.example:8443/gitlab/",
      token: "glpat-abcdefghij1234567890",
    });
    expect(api.requests("POST", TEST)).toHaveLength(0);
  });

  it("Save and test sends POST then a test, shows the result and keeps the dialog; a second Save is a PUT (AC28)", async () => {
    const api = mockFetch({
      [`POST ${BASE}`]: SAVED,
      [`PUT ${BASE}/c1`]: SAVED,
      [`POST ${TEST}`]: { ...TEST_OK, warnings: [{ code: "version_unknown", message: "GitLab version could not be read" }], instance_version: null },
    });
    const onClose = vi.fn();
    const onTested = vi.fn();
    const { user } = renderWithProviders(<ConnectionDialog onClose={onClose} onTested={onTested} />);

    await user.type(name(), "Main");
    await user.click(screen.getByRole("button", { name: "Save and test" }));

    expect(await screen.findByText("Connected as @ann")).toBeInTheDocument();
    expect(screen.getByText("GitLab version could not be read")).toBeInTheDocument();
    expect(screen.getByText("Version unknown")).toBeInTheDocument();
    expect(api.requests("POST", BASE)[0]!.body).toEqual({ name: "Main", base_url: "https://gitlab.com" });
    expect(api.requests("POST", TEST)[0]!.body).toEqual({ provider: "gitlab", connection_id: "c1" });
    expect(onTested).toHaveBeenCalledWith("c1", expect.objectContaining({ ok: true }));
    expect(onClose).not.toHaveBeenCalled();
    expect(screen.getByText("Edit gitlab.com")).toBeInTheDocument();

    await user.click(saveBtn());
    await waitFor(() => expect(onClose).toHaveBeenCalledTimes(1));
    expect(api.requests("POST", BASE)).toHaveLength(1);
    expect(api.requests("PUT", `${BASE}/c1`)).toHaveLength(1);
  });

  it("shows a 422 at the base URL field (linked with aria-describedby) and a duplicate host there too (AC28)", async () => {
    let answer: Response = validation(["base_url"], "Use the instance URL, without /api/v4");
    mockFetch({ [`POST ${BASE}`]: () => answer });
    const onClose = vi.fn();
    const { user } = renderWithProviders(<ConnectionDialog onClose={onClose} onTested={vi.fn()} />);

    await user.clear(url());
    await user.type(url(), "https://gitlab.com/api/v4");
    await user.click(saveBtn());

    const msg = await screen.findByText("Use the instance URL, without /api/v4");
    expect(url()).toHaveAttribute("aria-invalid", "true");
    expect(url().getAttribute("aria-describedby")).toContain(msg.id);
    expect(onClose).not.toHaveBeenCalled();

    answer = jsonResponse({ error: { code: "gitlab_connection_exists", message: "A connection for gitlab.com already exists" } }, 409);
    await user.click(saveBtn());
    const dup = await screen.findByText("A connection for gitlab.com already exists");
    expect(url().getAttribute("aria-describedby")).toContain(dup.id);
    expect(screen.queryByText("Use the instance URL, without /api/v4")).not.toBeInTheDocument();
  });

  it("closes on Escape and uses type=button on its own buttons (NFR12)", async () => {
    mockFetch({});
    const onClose = vi.fn();
    const { user } = renderWithProviders(<ConnectionDialog onClose={onClose} onTested={vi.fn()} />);
    for (const label of ["Cancel", "Save and test", "Save", "Show token"]) {
      expect(screen.getByRole("button", { name: label })).toHaveAttribute("type", "button");
    }
    await user.keyboard("{Escape}");
    expect(onClose).toHaveBeenCalledTimes(1);
  });
});

describe("ConnectionDialog edit", () => {
  it("never sends the stored token back; Remove token sends token:null; a typed token replaces (AC28)", async () => {
    const api = mockFetch({ [`PUT ${BASE}/c1`]: { ...SAVED, token_configured: false } });
    const onClose = vi.fn();
    const { user } = renderWithProviders(<ConnectionDialog connection={SAVED} onClose={onClose} onTested={vi.fn()} />);

    expect(screen.getByText("Edit gitlab.com")).toBeInTheDocument();
    expect(token()).toHaveValue("");
    expect(token()).toHaveAttribute("placeholder", "Token saved — paste a new token to replace it");

    await user.click(screen.getByRole("checkbox", { name: "Remove token" }));
    await user.click(saveBtn());
    await waitFor(() => expect(onClose).toHaveBeenCalledTimes(1));
    expect(api.requests("PUT", `${BASE}/c1`)[0]!.body).toEqual({ name: "gitlab.com", token: null });
  });

  it("sends a typed token and a changed base URL only (AC28)", async () => {
    const api = mockFetch({ [`PUT ${BASE}/c1`]: SAVED });
    const { user } = renderWithProviders(<ConnectionDialog connection={SAVED} onClose={vi.fn()} onTested={vi.fn()} />);

    await user.type(token(), "glpat-newtoken12345678901");
    expect(screen.getByRole("checkbox", { name: "Remove token" })).not.toBeChecked();
    await user.clear(url());
    await user.type(url(), "https://gitlab.com/");
    await user.click(saveBtn());
    await waitFor(() => expect(api.requests("PUT", `${BASE}/c1`)).toHaveLength(1));
    expect(api.requests("PUT", `${BASE}/c1`)[0]!.body).toEqual({
      name: "gitlab.com",
      base_url: "https://gitlab.com/",
      token: "glpat-newtoken12345678901",
    });
  });
});

describe("ConnectionDialog copy", () => {
  it("renders no hard-coded copy (AC35)", () => {
    mockFetch({});
    renderWithProviders(<ConnectionDialog connection={SAVED} onClose={vi.fn()} onTested={vi.fn()} />, { messages: pseudoMessages() });
    expect(strayText(screen.getByRole("dialog"))).toEqual([]);
  });
});

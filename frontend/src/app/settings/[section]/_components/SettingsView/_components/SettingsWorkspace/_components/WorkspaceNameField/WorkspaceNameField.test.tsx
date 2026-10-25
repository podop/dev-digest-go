import { describe, it, expect, afterEach } from "vitest";
import { renderWithProviders, screen, cleanup, waitFor, pseudoMessages } from "@/test/render";
import { mockFetch, jsonResponse } from "@/test/fetch-mock";
import { strayText } from "@/test/pseudo";
import { WorkspaceNameField } from "./WorkspaceNameField";

afterEach(cleanup);

const info = (name: string) => ({
  workspaceId: "11111111-1111-4111-8111-111111111111",
  name,
  dataDir: "/data",
  cloneDir: "/data/clones",
  repos: [],
});

/** GET /workspace serves `state.name`; PUT /workspace stores it, like the backend. */
function mockWorkspace(name = "default") {
  const state = { name };
  return mockFetch({
    "GET /workspace": () => info(state.name),
    "PUT /workspace": (req) => {
      state.name = (req.body as { name: string }).name;
      return info(state.name);
    },
  });
}

const input = () => screen.getByRole("textbox", { name: "Workspace name" });
const loaded = async () => {
  await waitFor(() => expect(input()).toBeEnabled());
  await waitFor(() => expect(input()).toHaveValue("default"));
};

describe("WorkspaceNameField", () => {
  it("saves the trimmed name on Enter and confirms with Saved (AC4)", async () => {
    const api = mockWorkspace();
    const { user } = renderWithProviders(<WorkspaceNameField />);
    await loaded();
    expect(screen.queryByText("Saved")).not.toBeInTheDocument();

    await user.clear(input());
    await user.type(input(), "  acme payments  {Enter}");

    expect(await screen.findByText("Saved")).toBeInTheDocument();
    expect(api.requests("PUT", "/workspace")).toHaveLength(1);
    expect(api.requests("PUT", "/workspace")[0]!.body).toEqual({ name: "acme payments" });
    await waitFor(() => expect(input()).toHaveValue("acme payments"));

    // Editing again hides the confirmation.
    await user.type(input(), "x");
    expect(screen.queryByText("Saved")).not.toBeInTheDocument();
  });

  it("saves on blur, and sends nothing when the value did not change (AC4)", async () => {
    const api = mockWorkspace();
    const { user } = renderWithProviders(<WorkspaceNameField />);
    await loaded();

    await user.click(input());
    await user.keyboard("{Enter}");
    await user.tab();
    expect(api.requests("PUT", "/workspace")).toHaveLength(0);

    await user.type(input(), "-2");
    await user.tab();
    expect(await screen.findByText("Saved")).toBeInTheDocument();
    expect(api.requests("PUT", "/workspace")).toHaveLength(1);
    expect(api.requests("PUT", "/workspace")[0]!.body).toEqual({ name: "default-2" });
  });

  it("blocks empty, 81-character and control-character names inline with no request (AC4, EC1, EC2)", async () => {
    const api = mockWorkspace();
    const { user } = renderWithProviders(<WorkspaceNameField />);
    await loaded();

    await user.clear(input());
    await user.type(input(), "   ");
    expect(screen.getByRole("alert")).toHaveTextContent("Enter a name.");
    expect(input()).toBeInvalid();
    await user.keyboard("{Enter}");

    await user.clear(input());
    await user.click(input());
    await user.paste("a".repeat(81));
    expect(screen.getByRole("alert")).toHaveTextContent("Use at most 80 characters.");
    expect(input()).toHaveAccessibleDescription(/Use at most 80 characters/);
    await user.keyboard("{Enter}");
    await user.tab();

    await user.clear(input());
    await user.click(input());
    await user.paste("a\tb");
    expect(screen.getByRole("alert")).toHaveTextContent("Remove line breaks, tabs and other control characters.");

    // 80 code points of an astral character are valid (counted as runes, A10).
    await user.clear(input());
    await user.click(input());
    await user.paste("😀".repeat(80));
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
    expect(api.requests("PUT", "/workspace")).toHaveLength(0);
  });

  it("shows a server validation_error inline and does not resend on blur", async () => {
    const api = mockFetch({
      "GET /workspace": () => info("default"),
      "PUT /workspace": () =>
        jsonResponse({ error: { code: "validation_error", message: "name is not valid" } }, 422),
    });
    const { user } = renderWithProviders(<WorkspaceNameField />);
    await loaded();

    await user.type(input(), "!{Enter}");
    expect(await screen.findByRole("alert")).toHaveTextContent("This name was not accepted");
    expect(screen.queryByText("Saved")).not.toBeInTheDocument();
    expect(input()).toHaveValue("default!");

    await user.tab();
    expect(api.requests("PUT", "/workspace")).toHaveLength(1);

    await user.type(input(), "?");
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  });

  it("renders no hard-coded copy under pseudo messages (AC22)", async () => {
    mockWorkspace();
    const { container, user } = renderWithProviders(<WorkspaceNameField />, { messages: pseudoMessages() });
    const field = () => screen.getByRole("textbox", { name: /settingsWorkspace\.name\.inputAria/ });
    await waitFor(() => expect(field()).toBeEnabled());

    await user.type(field(), "x");
    await user.clear(field());
    await user.type(field(), " ");
    await screen.findByText(/settingsWorkspace\.name\.errors\.empty/);
    expect(strayText(container)).toEqual([]);

    await user.type(field(), "x{Enter}");
    await screen.findByText(/settingsWorkspace\.name\.saved/);
    expect(strayText(container)).toEqual([]);
  });
});

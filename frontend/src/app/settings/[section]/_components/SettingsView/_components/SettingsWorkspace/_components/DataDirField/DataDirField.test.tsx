import { describe, it, expect, afterEach, vi } from "vitest";
import { renderWithProviders, screen, cleanup, waitFor, pseudoMessages } from "@/test/render";
import { mockFetch } from "@/test/fetch-mock";
import { strayText } from "@/test/pseudo";
import { DataDirField } from "./DataDirField";

afterEach(cleanup);

const DATA_DIR = "/home/me/.devdigest";
const workspace = {
  workspaceId: "11111111-1111-4111-8111-111111111111",
  name: "default",
  dataDir: DATA_DIR,
  cloneDir: `${DATA_DIR}/clones`,
  repos: [],
};

const input = () => screen.getByRole("textbox", { name: "Data directory" });
const copyButton = () => screen.getByRole("button", { name: "Copy" });

describe("DataDirField", () => {
  it("shows the data dir read-only with the restart hint, and copies it (AC4)", async () => {
    mockFetch({ "GET /workspace": workspace });
    const { user } = renderWithProviders(<DataDirField />);
    expect(copyButton()).toBeDisabled();

    await waitFor(() => expect(input()).toHaveValue(DATA_DIR));
    expect(input()).toHaveAttribute("readonly");
    expect(input()).toHaveClass("mono");
    expect(
      screen.getByText("Holds the local secrets file and repository clones. Changing it needs a restart."),
    ).toBeInTheDocument();

    // user-event installs its clipboard stub when the test user is created, so stub after render.
    const writeText = vi.fn().mockResolvedValue(undefined);
    vi.stubGlobal("navigator", { ...navigator, clipboard: { writeText } });
    await user.click(copyButton());
    expect(writeText).toHaveBeenCalledWith(DATA_DIR);
    expect(await screen.findByText("Copied")).toBeInTheDocument();
  });

  it("toasts when the clipboard is unavailable", async () => {
    mockFetch({ "GET /workspace": workspace });
    const { user } = renderWithProviders(<DataDirField />);
    await waitFor(() => expect(input()).toHaveValue(DATA_DIR));

    vi.stubGlobal("navigator", { ...navigator, clipboard: undefined });
    await user.click(copyButton());
    expect(await screen.findByText("Could not copy the data directory.")).toBeInTheDocument();
    expect(screen.queryByText("Copied")).not.toBeInTheDocument();
  });

  it("renders no hard-coded copy under pseudo messages (AC22)", async () => {
    mockFetch({ "GET /workspace": workspace });
    const { container, user } = renderWithProviders(<DataDirField />, { messages: pseudoMessages() });
    await waitFor(() =>
      expect(screen.getByRole("textbox", { name: /settingsWorkspace\.dataDir\.inputAria/ })).toHaveValue(DATA_DIR),
    );

    const writeText = vi.fn().mockResolvedValue(undefined);
    vi.stubGlobal("navigator", { ...navigator, clipboard: { writeText } });
    await user.click(screen.getByRole("button", { name: /settingsWorkspace\.dataDir\.copy/ }));
    await screen.findByText(/settingsWorkspace\.dataDir\.copied/);
    expect(strayText(container)).toEqual([]);
  });
});

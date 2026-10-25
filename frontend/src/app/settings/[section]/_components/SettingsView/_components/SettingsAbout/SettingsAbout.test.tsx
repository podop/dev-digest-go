/* SettingsAbout — the About table from GET /about (FR21, AC15) with loading/error states and
   pseudo-message coverage (AC22); real hooks over a stubbed API. */
import { describe, it, expect, afterEach } from "vitest";
import { renderWithProviders, screen, cleanup, within, waitFor, pseudoMessages } from "@/test/render";
import { mockFetch, jsonResponse } from "@/test/fetch-mock";
import { strayText } from "@/test/pseudo";
import { SettingsAbout } from "./SettingsAbout";

afterEach(cleanup);

const ABOUT = {
  version: "1.4.2",
  build_id: "9f3c2ab",
  runner: { kind: "go", version: "2" },
  config_format: "devdigest-plugin/v1",
  data_dir: "/var/lib/devdigest",
  secrets_file: "/var/lib/devdigest/secrets.json",
};

const rowValue = (label: string) => {
  const head = screen.getByRole("rowheader", { name: label });
  return within(head.closest("tr") as HTMLElement).getByRole("cell");
};

describe("SettingsAbout", () => {
  it("shows every row from /about and the run-setup-again link", async () => {
    mockFetch({ "GET /about": ABOUT });
    renderWithProviders(<SettingsAbout />);

    // Loading first: row skeletons, no table yet.
    expect(screen.getByRole("status", { name: "Loading about information…" })).toBeInTheDocument();
    expect(await screen.findByRole("table", { name: "About this install" })).toBeInTheDocument();

    expect(rowValue("Version")).toHaveTextContent("1.4.2");
    expect(rowValue("Build")).toHaveTextContent("9f3c2ab");
    expect(rowValue("Runner")).toHaveTextContent("Go runner v2");
    expect(rowValue("Config format")).toHaveTextContent("devdigest-plugin/v1");
    expect(rowValue("Data directory")).toHaveTextContent("/var/lib/devdigest");
    expect(screen.getAllByRole("row")).toHaveLength(5);
    expect(screen.queryByText(/updated/i)).not.toBeInTheDocument();
    // The secrets file belongs to API Keys' info box, not here.
    expect(screen.queryByText(ABOUT.secrets_file)).not.toBeInTheDocument();

    expect(screen.getByRole("link", { name: "Run setup again" })).toHaveAttribute("href", "/setup?step=model");
    expect(screen.queryByRole("status", { name: "Loading about information…" })).not.toBeInTheDocument();
  });

  it("shows `dev` values as they are", async () => {
    mockFetch({ "GET /about": { ...ABOUT, version: "dev", build_id: "dev" } });
    renderWithProviders(<SettingsAbout />);
    await screen.findByRole("table");

    expect(rowValue("Version")).toHaveTextContent(/^dev$/);
    expect(rowValue("Build")).toHaveTextContent(/^dev$/);
  });

  it("shows an error with Retry that reloads the table", async () => {
    let calls = 0;
    const api = mockFetch({
      "GET /about": () =>
        ++calls === 1 ? jsonResponse({ error: { code: "internal", message: "boom" } }, 500) : ABOUT,
    });
    const { user } = renderWithProviders(<SettingsAbout />);

    expect(await screen.findByRole("alert")).toHaveTextContent("Could not load the about information.");
    expect(screen.queryByRole("table")).not.toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "Retry" }));
    expect(await screen.findByRole("table")).toBeInTheDocument();
    expect(rowValue("Version")).toHaveTextContent("1.4.2");
    await waitFor(() => expect(api.requests("GET", "/about")).toHaveLength(2));
  });

  it("takes every visible string from the message files (pseudo bundle)", async () => {
    mockFetch({ "GET /about": ABOUT });
    const { container } = renderWithProviders(<SettingsAbout />, { messages: pseudoMessages() });
    await screen.findByRole("table");

    expect(screen.getByText("⟦about.runnerValue⟧")).toBeInTheDocument();
    expect(strayText(container, [ABOUT.version, ABOUT.build_id, ABOUT.config_format, ABOUT.data_dir])).toEqual([]);
  });

  it("takes the loading and error copy from the message files too", async () => {
    mockFetch({ "GET /about": () => jsonResponse({ error: { code: "internal", message: "boom" } }, 500) });
    const { container } = renderWithProviders(<SettingsAbout />, { messages: pseudoMessages() });
    await screen.findByText("⟦about.loadError⟧");

    expect(strayText(container)).toEqual([]);
  });
});

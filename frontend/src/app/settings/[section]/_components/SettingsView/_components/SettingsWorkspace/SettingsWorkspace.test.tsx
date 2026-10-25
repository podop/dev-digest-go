/* SettingsWorkspace — the Workspace section composed from its parts (FR4), the pseudo-locale
   check over the whole section (AC22) and AC12's "name and retention still work while the
   stats fail"; real hooks over a stubbed API. */
import { describe, it, expect, afterEach } from "vitest";
import { renderWithProviders, screen, cleanup, waitFor, pseudoMessages } from "@/test/render";
import { mockFetch, jsonResponse } from "@/test/fetch-mock";
import { pickOption } from "@/test/select";
import { strayText } from "@/test/pseudo";
import { SettingsWorkspace } from "./SettingsWorkspace";

afterEach(cleanup);

const STATS = { agents: 4, skills: 12, eval_cases: 31, learnings: 0, repos: 3, runs: 1280 };
const DATA_DIR = "/data/devdigest";

const info = (name: string) => ({
  workspaceId: "11111111-1111-4111-8111-111111111111",
  name,
  dataDir: DATA_DIR,
  cloneDir: `${DATA_DIR}/clones`,
  repos: [],
});

/** GET /workspace serves `state.name`; PUT /workspace stores it; PUT /settings echoes the body. */
function stubApi(stats: unknown = STATS) {
  const state = { name: "default" };
  return mockFetch({
    "GET /workspace": () => info(state.name),
    "PUT /workspace": (req) => {
      state.name = (req.body as { name: string }).name;
      return info(state.name);
    },
    "GET /workspace/stats": stats as never,
    "GET /settings": {},
    "PUT /settings": (req) => req.body,
  });
}

describe("SettingsWorkspace", () => {
  it("shows the heading and sub-title, then name, data directory, retention, six counts and the danger zone", async () => {
    stubApi();
    renderWithProviders(<SettingsWorkspace />);

    expect(screen.getByRole("heading", { level: 2, name: "Workspace" })).toBeInTheDocument();
    expect(screen.getByText("Where this workspace keeps its agents, skills, learnings, and run history.")).toBeInTheDocument();
    expect(screen.getByRole("textbox", { name: "Workspace name" })).toBeInTheDocument();
    expect(screen.getByRole("textbox", { name: "Data directory" })).toHaveAttribute("readonly");
    expect(await screen.findByRole("combobox", { name: "Retention" })).toBeInTheDocument();
    await waitFor(() => expect(screen.getByRole("textbox", { name: "Data directory" })).toHaveValue(DATA_DIR));
    expect(await screen.findByRole("region", { name: "Workspace statistics" })).toBeInTheDocument();
    expect(screen.getAllByRole("term")).toHaveLength(6);
    expect(screen.getByRole("button", { name: "Export workspace" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Reset run history" })).toBeInTheDocument();
    // DangerZone owns its heading: the section has exactly one Danger zone heading.
    expect(screen.getAllByRole("heading", { name: "Danger zone" })).toHaveLength(1);
  });

  it("keeps the name and retention fields working while the stats request fails (AC12)", async () => {
    const api = stubApi(() => jsonResponse({ error: { code: "internal", message: "boom" } }, 500));
    const { user } = renderWithProviders(<SettingsWorkspace />);

    expect(await screen.findByText("Could not load the workspace statistics.")).toBeInTheDocument();
    expect(screen.getAllByRole("definition")).toHaveLength(6);

    const input = screen.getByRole("textbox", { name: "Workspace name" });
    await waitFor(() => expect(input).toHaveValue("default"));
    await user.clear(input);
    await user.type(input, "acme{Enter}");
    expect(await screen.findByText("Saved")).toBeInTheDocument();
    expect(api.requests("PUT", "/workspace")[0]!.body).toEqual({ name: "acme" });

    await pickOption(user, screen.getByRole("combobox", { name: "Retention" }), "30 days");
    await waitFor(() => expect(api.requests("PUT", "/settings")).toHaveLength(1));
    expect(api.requests("PUT", "/settings")[0]!.body).toEqual({ retention_days: 30 });
  });

  it("takes every visible string of the section from the message files (AC22)", async () => {
    stubApi();
    const { container } = renderWithProviders(<SettingsWorkspace />, { messages: pseudoMessages() });

    expect(screen.getByText("⟦settingsWorkspace.title⟧")).toBeInTheDocument();
    expect(screen.getByText("⟦settingsWorkspace.body⟧")).toBeInTheDocument();
    await screen.findByRole("region", { name: "⟦workspaceStats.groupLabel⟧" });
    await waitFor(() =>
      expect(screen.getByRole("textbox", { name: "⟦settingsWorkspace.dataDir.inputAria⟧" })).toHaveValue(DATA_DIR),
    );
    expect(strayText(container, ["4", "12", "31", "0", "3", "1,280"])).toEqual([]);
  });
});

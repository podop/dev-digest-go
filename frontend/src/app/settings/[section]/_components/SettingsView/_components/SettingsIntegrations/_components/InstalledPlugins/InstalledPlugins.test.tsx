/* InstalledPlugins — list states, Enable/Disable and the Remove entry point (FR17, EC15, AC20).
   Rendered alone; the plugin hooks run for real against a stubbed fetch. */
import { describe, it, expect, afterEach } from "vitest";
import type { InstalledPlugin, PluginList } from "@/lib/types";
import { renderWithProviders, screen, cleanup, waitFor, within, pseudoMessages } from "@/test/render";
import { mockFetch, jsonResponse } from "@/test/fetch-mock";
import { strayText } from "@/test/pseudo";
import { InstalledPlugins } from "./InstalledPlugins";

afterEach(cleanup);

const LONG_NAME = "a-very-long-plugin-name-that-needs-truncating-in-a-narrow-row-of-the-integrations-section";

function plugin(id: string, name: string, over: Partial<InstalledPlugin> = {}): InstalledPlugin {
  return {
    id,
    name,
    version: "1.0.0",
    source: `${name}.zip`,
    installed_at: "2026-10-26T10:00:00.000Z",
    enabled: false,
    description: null,
    counts: { agents: 2, skills: 3, eval_cases: 5, learnings: 1 },
    ...over,
  };
}

function list(plugins: InstalledPlugin[]): PluginList {
  return { plugins, last_export: null };
}

describe("InstalledPlugins", () => {
  it("shows the loading status, then rows with badge text, counts, version and actions", async () => {
    mockFetch({
      "GET /plugins": list([
        plugin("p1", "acme", { enabled: true, counts: { agents: 1, skills: 1, eval_cases: 0, learnings: 0 } }),
        plugin("p2", LONG_NAME, { version: null }),
      ]),
    });
    renderWithProviders(<InstalledPlugins />);

    expect(screen.getByRole("status", { name: "Loading plugins…" })).toBeInTheDocument();
    const rows = await screen.findAllByRole("listitem");
    expect(rows).toHaveLength(2);

    const first = within(rows[0]!);
    expect(first.getByText("acme")).toBeInTheDocument();
    expect(first.getByText("1.0.0")).toBeInTheDocument();
    expect(first.getByText("Enabled")).toBeInTheDocument();
    expect(first.getByText("1 agent")).toBeInTheDocument();
    expect(first.getByText("1 skill")).toBeInTheDocument();
    expect(first.getByText("0 eval cases")).toBeInTheDocument();
    expect(first.getByText("0 learnings")).toBeInTheDocument();
    expect(first.getByRole("button", { name: "Disable plugin acme" })).toBeInTheDocument();
    expect(first.getByRole("button", { name: "Remove plugin acme" })).toBeInTheDocument();

    const second = within(rows[1]!);
    expect(second.getByText("Disabled")).toBeInTheDocument();
    expect(second.getByText("2 agents")).toBeInTheDocument();
    expect(second.getByText("5 eval cases")).toBeInTheDocument();
    expect(second.getByRole("button", { name: `Enable plugin ${LONG_NAME}` })).toBeInTheDocument();
    // EC15: a long name is cut by CSS and carries the full text in a native title.
    expect(second.getByText(LONG_NAME)).toHaveAttribute("title", LONG_NAME);
  });

  it("shows the empty text, and an error with Retry that reloads the list", async () => {
    let state: "empty" | "error" | "ok" = "empty";
    const api = mockFetch({
      "GET /plugins": () =>
        state === "error"
          ? jsonResponse({ error: { code: "internal", message: "down" } }, 500)
          : list(state === "empty" ? [] : [plugin("p1", "acme")]),
    });
    const { user, queryClient } = renderWithProviders(<InstalledPlugins />);

    expect(await screen.findByText("No plugins installed")).toBeInTheDocument();
    expect(screen.queryByRole("list")).not.toBeInTheDocument();

    state = "error";
    await queryClient.invalidateQueries({ queryKey: ["plugins"] });
    expect(await screen.findByText("Could not load the installed plugins")).toBeInTheDocument();
    expect(screen.queryByText("No plugins installed")).not.toBeInTheDocument();

    state = "ok";
    await user.click(screen.getByRole("button", { name: "Retry" }));
    expect(await screen.findByText("acme")).toBeInTheDocument();
    expect(screen.queryByText("Could not load the installed plugins")).not.toBeInTheDocument();
    expect(api.requests("GET", "/plugins").length).toBeGreaterThanOrEqual(3);
  });

  it("enables a plugin with PATCH {enabled} and shows the refetched state; a failed switch keeps the row as it was", async () => {
    let enabled = false;
    let fail = false;
    const api = mockFetch({
      "GET /plugins": () => list([plugin("p1", "acme", { enabled })]),
      "PATCH /plugins/:id": () => {
        if (fail) return jsonResponse({ error: { code: "internal", message: "Disk busy" } }, 500);
        enabled = true;
        return plugin("p1", "acme", { enabled });
      },
    });
    const { user } = renderWithProviders(<InstalledPlugins />);

    await user.click(await screen.findByRole("button", { name: "Enable plugin acme" }));
    expect(await screen.findByText("Enabled")).toBeInTheDocument();
    expect(api.requests("PATCH")).toHaveLength(1);
    expect(api.requests("PATCH")[0]!.path).toBe("/plugins/p1");
    expect(api.requests("PATCH")[0]!.body).toEqual({ enabled: true });

    // Now a failing disable: nothing flips, the button is usable again.
    fail = true;
    await user.click(screen.getByRole("button", { name: "Disable plugin acme" }));
    await waitFor(() => expect(api.requests("PATCH")).toHaveLength(2));
    expect(api.requests("PATCH")[1]!.body).toEqual({ enabled: false });
    await waitFor(() => expect(screen.getByRole("button", { name: "Disable plugin acme" })).toBeEnabled());
    expect(screen.getByText("Enabled")).toBeInTheDocument();
    expect(screen.queryByText("Disabled")).not.toBeInTheDocument();
  });

  it("opens the remove dialog for the clicked row; Escape closes it and focus returns to that Remove button", async () => {
    mockFetch({
      "GET /plugins": list([plugin("p1", "acme"), plugin("p2", "beta")]),
      "GET /agents": [],
      "GET /ci/installations": { repos: [] },
    });
    const { user } = renderWithProviders(<InstalledPlugins />);

    const trigger = await screen.findByRole("button", { name: "Remove plugin beta" });
    await user.click(trigger);
    const dialog = screen.getByRole("dialog");
    expect(within(dialog).getByText("Remove plugin beta?")).toBeInTheDocument();

    await user.keyboard("{Escape}");
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    expect(trigger).toHaveFocus();
  });
});

describe("InstalledPlugins messages (NFR5)", () => {
  it("takes every visible string from the message files (pseudo bundle) in each state", async () => {
    let state: "ok" | "empty" | "error" = "ok";
    mockFetch({
      "GET /plugins": () =>
        state === "error"
          ? jsonResponse({ error: { code: "internal", message: "down" } }, 500)
          : list(state === "empty" ? [] : [plugin("p1", "acme", { enabled: true })]),
    });
    const { container, queryClient } = renderWithProviders(<InstalledPlugins />, { messages: pseudoMessages() });
    const allowed = ["acme", "1.0.0"];

    expect(screen.getByRole("status", { name: "⟦integrationsPlugins.loading⟧" })).toBeInTheDocument();
    expect(await screen.findByText("⟦integrationsPlugins.row.status.enabled⟧")).toBeInTheDocument();
    expect(strayText(container, allowed)).toEqual([]);

    state = "empty";
    await queryClient.invalidateQueries({ queryKey: ["plugins"] });
    expect(await screen.findByText("⟦integrationsPlugins.empty⟧")).toBeInTheDocument();
    expect(strayText(container)).toEqual([]);

    state = "error";
    await queryClient.invalidateQueries({ queryKey: ["plugins"] });
    expect(await screen.findByText("⟦integrationsPlugins.loadError⟧")).toBeInTheDocument();
    expect(strayText(container)).toEqual([]);
  });
});

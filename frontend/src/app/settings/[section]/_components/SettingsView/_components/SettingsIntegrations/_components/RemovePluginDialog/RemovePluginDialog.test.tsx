/* RemovePluginDialog — typed-confirm removal of a plugin (FR17, NFR5, AC20, A11). Rendered alone with a
   spy for onClose; the agents / CI installation reads and the remove hook run for real against a stubbed fetch. */
import { describe, it, expect, afterEach, vi } from "vitest";
import type { Agent } from "@devdigest/shared";
import type { InstalledPlugin } from "@/lib/types";
import { renderWithProviders, screen, cleanup, waitFor, within, pseudoMessages } from "@/test/render";
import { mockFetch, jsonResponse } from "@/test/fetch-mock";
import { strayText } from "@/test/pseudo";
import { AGENT } from "@/test/skill-fixtures";
import { RemovePluginDialog } from "./RemovePluginDialog";

afterEach(cleanup);

const PLUGIN: InstalledPlugin = {
  id: "11111111-1111-4111-8111-111111111111",
  name: "acme-pack",
  version: "1.2.0",
  source: "acme-pack.zip",
  installed_at: "2026-10-26T10:00:00.000Z",
  enabled: false,
  description: null,
  counts: { agents: 2, skills: 3, eval_cases: 5, learnings: 1 },
};
const REF = { id: PLUGIN.id, name: PLUGIN.name, enabled: false };

function agent(id: string, plugin: Agent["plugin"]): Agent {
  return { ...AGENT, id, name: id, plugin };
}

function install(id: string, agentId: string) {
  return { id, agent_id: agentId, agent_name: agentId, slug: null, installed_at: "2026-10-26T10:00:00.000Z" };
}

/** Two agents of the plugin and one foreign agent; 3 of the 4 installations belong to the plugin. */
const READS = {
  "GET /agents": [agent("a1", REF), agent("a2", REF), agent("a3", null)],
  "GET /ci/installations": {
    repos: [
      { repo: "acme/api", installations: [install("i1", "a1"), install("i2", "a2"), install("i3", "a3")], last_run: null },
      { repo: "acme/web", installations: [install("i4", "a1")], last_run: null },
    ],
  },
};

describe("RemovePluginDialog", () => {
  it("names what will be deleted, including the CI installations of the plugin's agents", async () => {
    mockFetch(READS);
    renderWithProviders(<RemovePluginDialog plugin={PLUGIN} onClose={() => {}} />);

    const dialog = screen.getByRole("dialog");
    expect(within(dialog).getByText("Remove plugin acme-pack?")).toBeInTheDocument();
    expect(within(dialog).getByText("2 agents")).toBeInTheDocument();
    expect(within(dialog).getByText("3 skills")).toBeInTheDocument();
    expect(within(dialog).getByText("5 eval cases")).toBeInTheDocument();
    expect(within(dialog).getByText("1 learning")).toBeInTheDocument();
    // EC7 / D21: runs and reviews of the plugin's agents are not deleted with it.
    expect(within(dialog).getByText("Past runs and reviews of its agents stay in the history.")).toBeInTheDocument();
    // The agents and installations load after the first paint; the count settles on 3 of 4 (A11).
    expect(await within(dialog).findByText("3 GitHub Action installations of its agents")).toBeInTheDocument();
  });

  it("does not claim a CI count it could not read", async () => {
    mockFetch({
      ...READS,
      "GET /ci/installations": jsonResponse({ error: { code: "internal", message: "down" } }, 500),
    });
    renderWithProviders(<RemovePluginDialog plugin={PLUGIN} onClose={() => {}} />);

    expect(await screen.findByText("GitHub Action installations of its agents")).toBeInTheDocument();
    expect(screen.queryByText(/\d+ GitHub Action installation/)).not.toBeInTheDocument();
  });

  it("keeps Remove disabled until the exact name is typed, then sends DELETE with {confirm}", async () => {
    const api = mockFetch({
      ...READS,
      "DELETE /plugins/:id": () => ({ ok: true, removed: { agents: 2, skills: 3, eval_cases: 5, learnings: 1, ci_installations: 3 } }),
    });
    const onClose = vi.fn();
    const { user } = renderWithProviders(<RemovePluginDialog plugin={PLUGIN} onClose={onClose} />);
    const remove = screen.getByRole("button", { name: "Remove plugin" });
    const input = screen.getByRole("textbox", { name: "Type acme-pack to confirm" });

    expect(input).toHaveFocus();
    expect(remove).toBeDisabled();
    expect(screen.getByText("The button stays disabled until the text matches the plugin name exactly.")).toBeInTheDocument();

    await user.type(input, "acme-pac");
    expect(remove).toBeDisabled();
    await user.type(input, "K");
    expect(remove).toBeDisabled(); // case matters
    await user.clear(input);
    await user.type(input, "acme-pack ");
    expect(remove).toBeDisabled(); // so does a trailing space
    await user.clear(input);
    await user.type(input, "acme-pack");
    expect(remove).toBeEnabled();
    expect(screen.queryByText(/stays disabled/)).not.toBeInTheDocument();
    expect(api.requests("DELETE")).toHaveLength(0);

    await user.click(remove);
    await waitFor(() => expect(onClose).toHaveBeenCalledTimes(1));
    const sent = api.requests("DELETE");
    expect(sent).toHaveLength(1);
    expect(sent[0]!.path).toBe(`/plugins/${PLUGIN.id}`);
    expect(sent[0]!.body).toEqual({ confirm: "acme-pack" });
  });

  it("shows the server's 422 confirmation_mismatch inline, keeps the dialog open and clears it on the next edit", async () => {
    const api = mockFetch({
      ...READS,
      "DELETE /plugins/:id": () =>
        jsonResponse({ error: { code: "confirmation_mismatch", message: "Type the plugin name to confirm" } }, 422),
    });
    const onClose = vi.fn();
    const { user } = renderWithProviders(<RemovePluginDialog plugin={PLUGIN} onClose={onClose} />);
    const input = screen.getByRole("textbox", { name: "Type acme-pack to confirm" });

    await user.type(input, "acme-pack");
    await user.click(screen.getByRole("button", { name: "Remove plugin" }));

    expect(await screen.findByRole("alert")).toHaveTextContent("Type the plugin name to confirm");
    expect(screen.getByRole("dialog")).toBeInTheDocument();
    expect(onClose).not.toHaveBeenCalled();
    expect(api.requests("DELETE")).toHaveLength(1);

    await user.type(input, "x");
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  });

  it("keeps the dialog open after any other failure and shows no inline message for it", async () => {
    const api = mockFetch({
      ...READS,
      "DELETE /plugins/:id": () => jsonResponse({ error: { code: "internal", message: "Disk busy" } }, 500),
    });
    const onClose = vi.fn();
    const { user } = renderWithProviders(<RemovePluginDialog plugin={PLUGIN} onClose={onClose} />);

    await user.type(screen.getByRole("textbox", { name: "Type acme-pack to confirm" }), "acme-pack");
    await user.click(screen.getByRole("button", { name: "Remove plugin" }));

    await waitFor(() => expect(api.requests("DELETE")).toHaveLength(1));
    await waitFor(() => expect(screen.getByRole("button", { name: "Remove plugin" })).toBeEnabled());
    expect(screen.queryByRole("alert")).not.toBeInTheDocument(); // the hook toasts it
    expect(screen.getByRole("dialog")).toBeInTheDocument();
    expect(onClose).not.toHaveBeenCalled();
  });

  it("closes on Escape and on Cancel without sending anything", async () => {
    const api = mockFetch(READS);
    const onClose = vi.fn();
    const { user } = renderWithProviders(<RemovePluginDialog plugin={PLUGIN} onClose={onClose} />);

    await user.keyboard("{Escape}");
    expect(onClose).toHaveBeenCalledTimes(1);
    await user.click(screen.getByRole("button", { name: "Cancel" }));
    expect(onClose).toHaveBeenCalledTimes(2);
    expect(api.requests("DELETE")).toHaveLength(0);
  });
});

describe("RemovePluginDialog messages (NFR5)", () => {
  it("takes every visible string from the message files (pseudo bundle), mismatch state included", async () => {
    mockFetch({
      ...READS,
      "DELETE /plugins/:id": () =>
        jsonResponse({ error: { code: "confirmation_mismatch", message: "Server says no" } }, 422),
    });
    const { user } = renderWithProviders(<RemovePluginDialog plugin={PLUGIN} onClose={() => {}} />, {
      messages: pseudoMessages(),
    });
    // The server's name appears only in data places (the dialog never prints the name itself under pseudo).
    expect(await screen.findByText("⟦integrationsPlugins.dialog.deleted.ci⟧")).toBeInTheDocument();
    expect(strayText(document.body)).toEqual([]);

    await user.type(screen.getByRole("textbox"), "acme-pack");
    await user.click(screen.getByRole("button", { name: "⟦integrationsPlugins.dialog.confirm⟧" }));
    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent("Server says no"); // API data, not copy
    expect(strayText(document.body, ["Server says no"])).toEqual([]);
  });
});

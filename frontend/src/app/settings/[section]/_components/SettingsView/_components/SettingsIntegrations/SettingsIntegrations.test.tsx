/* SettingsIntegrations — the Integrations section composes the GitHub Action card, plugin export,
   plugin import with the installed plugins list inside it, in that order under one heading (FR19, A10), and
   renders no hard-coded copy (NFR5, AC22). The real cards run against a stubbed fetch. */
import { describe, it, expect, afterEach } from "vitest";
import type { InstalledPlugin } from "@devdigest/shared";
import { renderWithProviders, screen, cleanup, pseudoMessages } from "@/test/render";
import { mockFetch } from "@/test/fetch-mock";
import { strayText } from "@/test/pseudo";
import { SettingsIntegrations } from "./SettingsIntegrations";

afterEach(cleanup);

const PLUGIN: InstalledPlugin = {
  id: "p1",
  name: "acme",
  version: "1.0.0",
  source: "acme.zip",
  installed_at: "2026-10-26T10:00:00.000Z",
  enabled: false,
  description: null,
  counts: { agents: 2, skills: 3, eval_cases: 5, learnings: 1 },
};

function stubApi() {
  return mockFetch({
    "GET /ci/installations": { repos: [] },
    "GET /plugins": { plugins: [PLUGIN], last_export: null },
    "GET /agents": [],
    "GET /skills": [],
    "GET /repos": [],
  });
}

describe("SettingsIntegrations", () => {
  it("shows the heading and intro, then the four parts in prototype order", async () => {
    stubApi();
    renderWithProviders(<SettingsIntegrations />);

    expect(screen.getByRole("heading", { level: 2, name: "Integrations" })).toBeInTheDocument();
    expect(screen.getByText(/Run your agents as a GitHub Action/)).toBeInTheDocument();

    // The installed list loads last; once its row is there every part has rendered.
    expect(await screen.findByText("acme")).toBeInTheDocument();
    expect(screen.getAllByRole("heading", { level: 3 }).map((h) => h.textContent)).toEqual([
      "GitHub Action",
      "Export workspace as plugin",
      "Import plugin",
    ]);
    // The installed plugins sit inside the import card, as in the prototype.
    const installed = screen.getByRole("heading", { level: 4, name: "Installed plugins" });
    expect(installed.closest("section[aria-labelledby]")?.parentElement?.closest("section")).toHaveTextContent("Import plugin");
  });

  it("renders no hard-coded copy under the pseudo-locale", async () => {
    stubApi();
    const { container } = renderWithProviders(<SettingsIntegrations />, { messages: pseudoMessages() });

    await screen.findAllByRole("heading", { level: 3 });
    expect(strayText(container)).toEqual([]);
  });
});

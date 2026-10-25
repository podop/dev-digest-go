/* DangerZone — export and reset entry points (FR18, AC20, AC21). The hooks run for real
   against a stubbed fetch; jsdom has no object URLs, so stand-ins are installed. */
import { describe, it, expect, afterEach, onTestFinished, vi } from "vitest";
import { createAppQueryClient } from "@/lib/providers";
import { renderWithProviders, screen, cleanup, waitFor, within, pseudoMessages } from "@/test/render";
import { mockFetch, jsonResponse } from "@/test/fetch-mock";
import { strayText } from "@/test/pseudo";
import { DangerZone } from "./DangerZone";

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

const INFO = {
  workspaceId: "11111111-1111-4111-8111-111111111111",
  name: "acme",
  dataDir: "/data",
  cloneDir: "/data/clones",
  repos: [],
};
const STATS = { agents: 3, skills: 2, eval_cases: 1, learnings: 24, repos: 1, runs: 120 };

/** jsdom has no URL.createObjectURL: install stand-ins, remove them again, record anchor clicks. */
function stubDownloads(): string[] {
  URL.createObjectURL = () => "blob:test";
  URL.revokeObjectURL = () => {};
  onTestFinished(() => {
    Reflect.deleteProperty(URL, "createObjectURL");
    Reflect.deleteProperty(URL, "revokeObjectURL");
  });
  const names: string[] = [];
  vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(function (this: HTMLAnchorElement) {
    names.push(this.download);
  });
  return names;
}

function exportResponse() {
  return new Response('{"manifest":{}}', {
    status: 200,
    headers: {
      "content-type": "application/json",
      "content-disposition": 'attachment; filename="devdigest-workspace-acme-2026-10-25.json"',
    },
  });
}

function setup() {
  return renderWithProviders(<DangerZone />, { queryClient: createAppQueryClient() });
}

describe("DangerZone", () => {
  it("shows the copy, disables Export while in flight, downloads under the server's file name and toasts", async () => {
    const names = stubDownloads();
    let release: () => void = () => {};
    const gate = new Promise<void>((res) => (release = res));
    const api = mockFetch({
      "GET /workspace": INFO,
      "GET /workspace/export": async () => {
        await gate;
        return exportResponse();
      },
    });
    const { user } = setup();

    expect(screen.getByRole("heading", { name: "Danger zone" })).toBeInTheDocument();
    expect(screen.getByText(/Agents and skills are kept\. This cannot be undone\./)).toBeInTheDocument();
    expect(
      screen.getByText("Run history includes reviews, findings and Agent Performance data."),
    ).toBeInTheDocument();

    await user.dblClick(screen.getByRole("button", { name: "Export workspace" }));
    expect(await screen.findByRole("button", { name: "Exporting…" })).toBeDisabled();

    release();
    await waitFor(() => expect(names).toEqual(["devdigest-workspace-acme-2026-10-25.json"]));
    expect(await screen.findByText("Exported devdigest-workspace-acme-2026-10-25.json")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Export workspace" })).toBeEnabled();
    expect(api.requests("GET", "/workspace/export")).toHaveLength(1);
  });

  it("a failed export toasts the server message and re-enables the button", async () => {
    stubDownloads();
    mockFetch({
      "GET /workspace": INFO,
      "GET /workspace/export": () => jsonResponse({ error: { code: "internal", message: "Export failed on the server" } }, 500),
    });
    const { user } = setup();

    await user.click(screen.getByRole("button", { name: "Export workspace" }));
    expect(await within(screen.getByRole("status")).findByText("Export failed on the server")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Export workspace" })).toBeEnabled();
  });

  it("Reset run history opens the dialog on the workspace name, Escape closes it and returns focus", async () => {
    mockFetch({ "GET /workspace": INFO, "GET /workspace/stats": STATS });
    const { user } = setup();

    const trigger = screen.getByRole("button", { name: "Reset run history" });
    expect(trigger).toBeDisabled();
    expect(screen.getByText(/workspace name has not loaded/)).toBeInTheDocument();
    await waitFor(() => expect(trigger).toBeEnabled());
    expect(screen.queryByText(/workspace name has not loaded/)).not.toBeInTheDocument();

    await user.click(trigger);
    const dialog = screen.getByRole("dialog");
    expect(within(dialog).getByRole("textbox", { name: "Type acme to confirm" })).toHaveFocus();

    await user.keyboard("{Escape}");
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    expect(trigger).toHaveFocus();
  });
});

describe("DangerZone messages (NFR6)", () => {
  it("takes every visible string from the message files (pseudo bundle)", async () => {
    mockFetch({ "GET /workspace": INFO, "GET /workspace/stats": STATS });
    renderWithProviders(<DangerZone />, { queryClient: createAppQueryClient(), messages: pseudoMessages() });

    expect(screen.getByText("⟦workspaceDanger.title⟧")).toBeInTheDocument();
    expect(screen.getByText("⟦workspaceDanger.reset.nameUnavailable⟧")).toBeInTheDocument();
    expect(strayText(document.body)).toEqual([]);
    await waitFor(() => expect(screen.getByRole("button", { name: "⟦workspaceDanger.reset.label⟧" })).toBeEnabled());
    expect(strayText(document.body)).toEqual([]);
  });
});

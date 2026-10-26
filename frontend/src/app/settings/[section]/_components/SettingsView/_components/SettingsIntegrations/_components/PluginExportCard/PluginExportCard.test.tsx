/* PluginExportCard — FR15, EC15, AC18, NFR5. The hooks run for real against a stubbed fetch;
   jsdom has no object URLs, so stand-ins are installed (as in DangerZone.test). */
import { describe, it, expect, afterEach, onTestFinished, vi } from "vitest";
import { createAppQueryClient } from "@/lib/providers";
import { renderWithProviders, screen, cleanup, waitFor, within, pseudoMessages } from "@/test/render";
import { mockFetch } from "@/test/fetch-mock";
import { strayText } from "@/test/pseudo";
import { PluginExportCard } from "./PluginExportCard";

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

const AGENTS = [
  { id: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", name: "Security reviewer" },
  { id: "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb", name: "Style reviewer" },
];
const SKILLS = [{ id: "cccccccc-cccc-4ccc-8ccc-cccccccccccc", name: "owasp-top-10" }];
const LAST = {
  file_name: "devdigest-acme-2026-10-25.zip",
  exported_at: "2026-10-25T10:00:00Z",
  counts: { agents: 2, skills: 1, eval_cases: 4, learnings: 0 },
};

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

function zipResponse(fileName: string) {
  return new Response("zip", {
    status: 200,
    headers: { "content-type": "application/zip", "content-disposition": `attachment; filename="${fileName}"` },
  });
}

function setup(messages?: ReturnType<typeof pseudoMessages>) {
  return renderWithProviders(<PluginExportCard />, { queryClient: createAppQueryClient(), messages });
}

describe("PluginExportCard", () => {
  it("shows the last export with a truncated, titled file name, or nothing when there is none", async () => {
    mockFetch({
      "GET /agents": AGENTS,
      "GET /skills": SKILLS,
      "GET /plugins": { plugins: [], last_export: { ...LAST, file_name: `${"long-".repeat(30)}.zip` } },
    });
    setup();

    const line = (await screen.findByText(/Last export:/)).closest("p")!;
    expect(line).toHaveTextContent(`Last export: ${"long-".repeat(30)}.zip · 1 skill · 2 agents`);
    expect(within(line).getByTitle(`${"long-".repeat(30)}.zip`)).toBeInTheDocument();
    cleanup();

    mockFetch({ "GET /agents": AGENTS, "GET /skills": SKILLS, "GET /plugins": { plugins: [], last_export: null } });
    setup();
    expect(await screen.findByRole("button", { name: "Export workspace as plugin" })).toBeEnabled();
    await waitFor(() => expect(screen.queryByText(/Last export:/)).not.toBeInTheDocument());
  });

  it("disables the button and says why when the workspace has no agents and no skills", async () => {
    mockFetch({ "GET /agents": [], "GET /skills": [], "GET /plugins": { plugins: [], last_export: null } });
    setup();

    const button = await screen.findByRole("button", { name: "Export workspace as plugin" });
    await waitFor(() => expect(button).toBeDisabled());
    expect(button).toHaveAccessibleDescription(/nothing to export yet/);
  });

  it("exports with the defaults: no ids, eval cases on, learnings off, downloads the zip and shows the new last export", async () => {
    const names = stubDownloads();
    let release: () => void = () => {};
    const gate = new Promise<void>((res) => (release = res));
    let last: typeof LAST | null = null;
    const api = mockFetch({
      "GET /agents": AGENTS,
      "GET /skills": SKILLS,
      "GET /plugins": () => ({ plugins: [], last_export: last }),
      "POST /plugins/export": async () => {
        await gate;
        last = LAST;
        return zipResponse(LAST.file_name);
      },
    });
    const { user } = setup();

    await user.click(await screen.findByRole("button", { name: "Export workspace as plugin" }));
    const dialog = await screen.findByRole("dialog", { name: "Export workspace as plugin" });
    expect(within(dialog).getByRole("textbox", { name: "Plugin name" })).toHaveValue("");
    expect(await within(dialog).findByRole("checkbox", { name: "Security reviewer" })).toBeChecked();
    expect(within(dialog).getByRole("checkbox", { name: "Style reviewer" })).toBeChecked();
    expect(within(dialog).getByRole("checkbox", { name: "owasp-top-10" })).toBeChecked();
    expect(within(dialog).getByRole("checkbox", { name: "Include eval cases" })).toBeChecked();
    expect(within(dialog).getByRole("checkbox", { name: "Include learnings" })).not.toBeChecked();

    await user.click(within(dialog).getByRole("button", { name: "Export" }));

    // In flight: the card's own button is busy, the dialog cannot be re-submitted.
    expect(await screen.findAllByRole("button", { name: "Exporting…" })).toHaveLength(2);
    release();

    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    expect(api.requests("POST", "/plugins/export")[0]?.body).toEqual({
      include_eval_cases: true,
      include_learnings: false,
    });
    expect(names).toEqual([LAST.file_name]);
    const line = (await screen.findByText(/Last export:/)).closest("p");
    expect(line).toHaveTextContent("Last export: devdigest-acme-2026-10-25.zip · 1 skill · 2 agents");
    expect(screen.getByRole("button", { name: "Export workspace as plugin" })).toBeEnabled();
  });

  it("renders every string of the card and dialog from the message files (pseudo messages)", async () => {
    mockFetch({
      "GET /agents": AGENTS,
      "GET /skills": SKILLS,
      "GET /plugins": { plugins: [], last_export: LAST },
    });
    const { user } = setup(pseudoMessages());

    await user.click(await screen.findByRole("button", { name: "⟦integrationsExport.card.button⟧" }));
    await screen.findByRole("checkbox", { name: AGENTS[0]!.name });

    const allowed = [...AGENTS.map((a) => a.name), ...SKILLS.map((sk) => sk.name), LAST.file_name];
    expect(strayText(document.body, allowed)).toEqual([]);
  });
});

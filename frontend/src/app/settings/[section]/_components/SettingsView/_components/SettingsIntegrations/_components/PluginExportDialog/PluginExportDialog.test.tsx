/* PluginExportDialog — FR15, EC15, NFR5, §6 "Export" errors, D12. Rendered with a small host that
   owns the mutation like the card does, so the trigger can be checked for focus return. */
import React from "react";
import { describe, it, expect, afterEach, onTestFinished, vi } from "vitest";
import { createAppQueryClient } from "@/lib/providers";
import { useExportPlugin } from "@/lib/hooks";
import { renderWithProviders, screen, cleanup, waitFor, within } from "@/test/render";
import { mockFetch, jsonResponse } from "@/test/fetch-mock";
import { PluginExportDialog } from "./PluginExportDialog";
import { buildExportRequest, isInvalidExportName } from "./helpers";

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

const A1 = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa";
const A2 = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb";
const S1 = "cccccccc-cccc-4ccc-8ccc-cccccccccccc";
const S2 = "dddddddd-dddd-4ddd-8ddd-dddddddddddd";
const AGENTS = [
  { id: A1, name: "Security reviewer" },
  { id: A2, name: "Style reviewer" },
];
const SKILLS = [
  { id: S1, name: "owasp-top-10" },
  { id: S2, name: "naming" },
];

function Host() {
  const exporter = useExportPlugin();
  const [open, setOpen] = React.useState(false);
  return (
    <>
      <button type="button" onClick={() => setOpen(true)}>
        Open export
      </button>
      {open && (
        <PluginExportDialog
          onClose={() => setOpen(false)}
          onExport={exporter.mutateAsync}
          pending={exporter.isPending}
          error={exporter.error}
        />
      )}
    </>
  );
}

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

const zip = () =>
  new Response("zip", {
    headers: { "content-type": "application/zip", "content-disposition": 'attachment; filename="p.zip"' },
  });

async function openDialog() {
  const view = renderWithProviders(<Host />, { queryClient: createAppQueryClient() });
  await view.user.click(screen.getByRole("button", { name: "Open export" }));
  const dialog = await screen.findByRole("dialog", { name: "Export workspace as plugin" });
  await within(dialog).findByRole("checkbox", { name: "naming" });
  return { ...view, dialog };
}

describe("PluginExportDialog", () => {
  it("sends the chosen name and only the picked agents and skills when the selection is a subset", async () => {
    stubDownloads();
    const api = mockFetch({ "GET /agents": AGENTS, "GET /skills": SKILLS, "POST /plugins/export": zip() });
    const { user, dialog } = await openDialog();

    const name = within(dialog).getByRole("textbox", { name: "Plugin name" });
    await user.click(name);
    await user.paste("Team Reviews");
    expect(name).toBeInvalid();
    expect(name).toHaveAccessibleDescription(/lowercase letters, digits and single dashes/i);
    expect(within(dialog).getByRole("button", { name: "Export" })).toBeDisabled();

    await user.clear(name);
    await user.paste("team-reviews");
    expect(name).toBeValid();
    await user.click(within(dialog).getByRole("checkbox", { name: "Style reviewer" }));
    await user.click(within(dialog).getByRole("checkbox", { name: "naming" }));
    await user.click(within(dialog).getByRole("checkbox", { name: "Include eval cases" }));
    await user.click(within(dialog).getByRole("checkbox", { name: "Include learnings" }));
    await user.click(within(dialog).getByRole("button", { name: "Export" }));

    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    expect(api.requests("POST", "/plugins/export")[0]?.body).toEqual({
      name: "team-reviews",
      agent_ids: [A1],
      skill_ids: [S1],
      include_eval_cases: false,
      include_learnings: true,
    });
  });

  it("disables Export with a hint when nothing is selected, and Clear / Select all flip a group", async () => {
    mockFetch({ "GET /agents": AGENTS, "GET /skills": SKILLS });
    const { user, dialog } = await openDialog();

    const agents = within(dialog).getByRole("group", { name: "Agents" });
    const skills = within(dialog).getByRole("group", { name: "Skills" });
    await user.click(within(agents).getByRole("button", { name: "Clear" }));
    expect(within(agents).getByRole("checkbox", { name: "Security reviewer" })).not.toBeChecked();
    expect(within(dialog).getByRole("button", { name: "Export" })).toBeEnabled();

    await user.click(within(skills).getByRole("button", { name: "Clear" }));
    expect(within(dialog).getByText("Select at least one agent or skill.")).toBeInTheDocument();
    expect(within(dialog).getByRole("button", { name: "Export" })).toBeDisabled();

    await user.click(within(skills).getByRole("button", { name: "Select all" }));
    expect(within(dialog).getByRole("button", { name: "Export" })).toBeEnabled();
  });

  it("shows a 4xx message in the dialog, 'Export failed — try again' for a 5xx and a network error, and saves no file", async () => {
    const names = stubDownloads();
    const api = mockFetch({
      "GET /agents": AGENTS,
      "GET /skills": SKILLS,
      "POST /plugins/export": jsonResponse({ error: { code: "nothing_to_export", message: "Nothing to export" } }, 422),
    });
    const { user, dialog } = await openDialog();
    const exportButton = within(dialog).getByRole("button", { name: "Export" });

    await user.click(exportButton);
    expect(await within(dialog).findByRole("alert")).toHaveTextContent("Nothing to export");

    api.on("POST /plugins/export", jsonResponse({ error: { code: "internal_error", message: "boom" } }, 500));
    await user.click(exportButton);
    await waitFor(() => expect(within(dialog).getByRole("alert")).toHaveTextContent("Export failed — try again"));
    expect(within(dialog).queryByText("boom")).not.toBeInTheDocument();

    api.on("POST /plugins/export", () => {
      throw new TypeError("Failed to fetch");
    });
    await user.click(exportButton);
    await waitFor(() => expect(within(dialog).getByRole("alert")).toHaveTextContent("Export failed — try again"));

    expect(screen.getByRole("dialog")).toBeInTheDocument();
    expect(names).toEqual([]);
  });

  it("closes on Escape and returns focus to the trigger", async () => {
    mockFetch({ "GET /agents": AGENTS, "GET /skills": SKILLS });
    const { user } = await openDialog();
    expect(screen.getByRole("textbox", { name: "Plugin name" })).toHaveFocus();

    await user.keyboard("{Escape}");

    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    expect(screen.getByRole("button", { name: "Open export" })).toHaveFocus();
  });
});

describe("export request helpers", () => {
  const selection = { agentIds: [A1, A2], skillIds: [S1], excludedAgents: new Set<string>(), excludedSkills: new Set<string>() };
  const options = { name: "  ", includeEvalCases: true, includeLearnings: false };

  it("sends no ids when everything is selected, and drops an emptied group", () => {
    expect(buildExportRequest(selection, options)).toEqual({ include_eval_cases: true, include_learnings: false });
    expect(buildExportRequest({ ...selection, excludedSkills: new Set([S1]) }, options)).toEqual({
      agent_ids: [A1, A2],
      include_eval_cases: true,
      include_learnings: false,
    });
  });

  it("accepts an empty or kebab-case name of at most 64 characters", () => {
    expect(["", " ", "a", "team-reviews", "a1-b2", "a".repeat(64)].map(isInvalidExportName)).toEqual(Array(6).fill(false));
    expect(["Team", "a_b", "-a", "a-", "a--b", "a".repeat(65)].map(isInvalidExportName)).toEqual(Array(6).fill(true));
  });
});

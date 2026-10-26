/* InstallActionDialog — "Install in a repo" (FR3, EC13, AC3, NFR5). Rendered with a trigger
   button; the repo and agent lists and the install hook run for real against a stubbed fetch. */
import React from "react";
import { describe, it, expect, afterEach } from "vitest";
import type { Agent } from "@devdigest/shared";
import type { Repo } from "@/lib/types";
import { renderWithProviders, screen, cleanup, waitFor, within, pseudoMessages } from "@/test/render";
import { mockFetch, jsonResponse } from "@/test/fetch-mock";
import { pickOption } from "@/test/select";
import { strayText } from "@/test/pseudo";
import { AGENT } from "@/test/skill-fixtures";
import { InstallActionDialog } from "./InstallActionDialog";

afterEach(cleanup);

const REPO: Repo = {
  id: "r1",
  workspace_id: "w1",
  owner: "acme",
  name: "api",
  full_name: "acme/api",
  default_branch: "main",
  clone_path: null,
  last_polled_at: null,
  created_by: null,
};

const agent = (id: string, name: string, over: Partial<Agent> = {}): Agent => ({ ...AGENT, id, name, provider: "openrouter", ...over });
const A1 = agent("a1", "Security Reviewer");
const A2 = agent("a2", "Style Reviewer");
const A3 = agent("a3", "Perf Reviewer");
const CLAUDE = agent("a4", "Claude Reviewer", { provider: "anthropic" });
const OFF = { id: "p1", name: "acme-pack", enabled: false };
const HIDDEN = agent("a5", "Pack Reviewer", { plugin: OFF });
const HIDDEN_CLAUDE = agent("a6", "Pack Claude", { provider: "anthropic", plugin: OFF });
const ON_PLUGIN = agent("a7", "Pack On Reviewer", { plugin: { ...OFF, enabled: true } });

const ok = (pr: string | null) => jsonResponse({ installation: null, files: [], pr_url: pr }, 201);

function Harness() {
  const [open, setOpen] = React.useState(false);
  return (
    <>
      <button type="button" onClick={() => setOpen(true)}>
        open dialog
      </button>
      {open && <InstallActionDialog onClose={() => setOpen(false)} />}
    </>
  );
}

async function openDialog(user: ReturnType<typeof renderWithProviders>["user"]) {
  await user.click(screen.getByRole("button", { name: "open dialog" }));
  await screen.findByRole("dialog");
}

describe("InstallActionDialog", () => {
  it("lists agents with reasons for the unselectable ones, hides disabled-plugin agents, and keeps Install off until repo + agent", async () => {
    mockFetch({ "GET /repos": [REPO], "GET /agents": [A1, CLAUDE, HIDDEN, HIDDEN_CLAUDE, ON_PLUGIN] });
    const { user } = renderWithProviders(<Harness />);
    await openDialog(user);

    const install = screen.getByRole("button", { name: "Install" });
    expect(install).toBeDisabled();
    const security = await screen.findByRole("checkbox", { name: "Security Reviewer" });
    expect(screen.getByRole("checkbox", { name: "Pack On Reviewer" })).toBeEnabled();

    const claude = screen.getByRole("checkbox", { name: "Claude Reviewer" });
    expect(claude).toBeDisabled();
    expect(claude).toHaveAccessibleDescription("Only OpenRouter agents can run in GitHub Actions.");
    expect(screen.queryByText("Pack Reviewer")).not.toBeInTheDocument();
    expect(screen.queryByText("Pack Claude")).not.toBeInTheDocument();

    await user.click(security);
    expect(install).toBeDisabled(); // an agent without a repository
    await pickOption(user, screen.getByRole("combobox", { name: "Repository" }), "acme/api");
    expect(install).toBeEnabled();
    await user.click(security);
    expect(install).toBeDisabled();
  });

  it("installs one agent at a time, keeps going after a failure, and Retry failed resends only the failed one (EC13, AC3)", async () => {
    let inFlight = 0;
    let maxInFlight = 0;
    const order: string[] = [];
    let a2Fails = true;
    const api = mockFetch({
      "GET /repos": [REPO],
      "GET /agents": [A1, A2, A3, CLAUDE],
      "POST /agents/:id/export-ci": async (req) => {
        inFlight++;
        maxInFlight = Math.max(maxInFlight, inFlight);
        order.push(req.params.id!);
        await new Promise((r) => setTimeout(r, 15));
        inFlight--;
        if (req.params.id === "a2" && a2Fails) {
          return jsonResponse({ error: { code: "validation_error", message: "Agent has no model" } }, 422);
        }
        return ok(`https://github.com/acme/api/pull/${order.length}`);
      },
    });
    const { user } = renderWithProviders(<Harness />);
    await openDialog(user);

    await pickOption(user, await screen.findByRole("combobox", { name: "Repository" }), "acme/api");
    for (const name of ["Security Reviewer", "Style Reviewer", "Perf Reviewer"]) {
      await user.click(await screen.findByRole("checkbox", { name: name }));
    }
    await user.click(screen.getByRole("button", { name: "Install" }));

    expect(await screen.findByText("Failed: Agent has no model")).toBeInTheDocument();
    await waitFor(() => expect(screen.getAllByRole("link", { name: "View pull request" })).toHaveLength(2));
    expect(order).toEqual(["a1", "a2", "a3"]); // the third was still tried
    expect(maxInFlight).toBe(1);
    for (const r of api.requests("POST")) {
      expect(r.body).toEqual({ repo: "acme/api", target: "gha", action: "open_pr" });
    }
    const links = screen.getAllByRole("link", { name: "View pull request" });
    expect(links[0]).toHaveAttribute("href", "https://github.com/acme/api/pull/1");
    expect(links[1]).toHaveAttribute("href", "https://github.com/acme/api/pull/3");
    expect(screen.getByRole("checkbox", { name: "Claude Reviewer" })).toBeDisabled();

    a2Fails = false;
    await user.click(screen.getByRole("button", { name: "Retry failed" }));
    await waitFor(() => expect(screen.getAllByRole("link", { name: "View pull request" })).toHaveLength(3));
    expect(order).toEqual(["a1", "a2", "a3", "a2"]); // only the failed agent again
    expect(screen.queryByRole("button", { name: "Retry failed" })).not.toBeInTheDocument();
    expect(screen.queryByText(/^Failed:/)).not.toBeInTheDocument();
  });

  it("shows a spinner on the running agent and 'Waiting' on the queued one; Close and Escape wait for the run", async () => {
    let release: () => void = () => {};
    const gate = new Promise<void>((res) => (release = res));
    mockFetch({
      "GET /repos": [REPO],
      "GET /agents": [A1, A2],
      "POST /agents/:id/export-ci": async () => {
        await gate;
        return ok("https://github.com/acme/api/pull/1");
      },
    });
    const { user } = renderWithProviders(<Harness />);
    await openDialog(user);
    await pickOption(user, await screen.findByRole("combobox", { name: "Repository" }), "acme/api");
    await user.click(await screen.findByRole("checkbox", { name: "Security Reviewer" }));
    await user.click(screen.getByRole("checkbox", { name: "Style Reviewer" }));
    await user.click(screen.getByRole("button", { name: "Install" }));

    const dialog = screen.getByRole("dialog");
    expect(await within(dialog).findByText("Installing…", { selector: "div" })).toBeInTheDocument();
    expect(within(dialog).getByText("Waiting")).toBeInTheDocument();
    expect(screen.getByRole("checkbox", { name: "Security Reviewer" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "Cancel" })).toBeDisabled();
    await user.keyboard("{Escape}");
    expect(screen.getByRole("dialog")).toBeInTheDocument();

    release();
    await waitFor(() => expect(screen.getAllByRole("link", { name: "View pull request" })).toHaveLength(2));
    await user.click(screen.getByRole("button", { name: "Done" }));
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  });

  it("renders no link for an unsafe pull request URL", async () => {
    mockFetch({
      "GET /repos": [REPO],
      "GET /agents": [A1],
      "POST /agents/:id/export-ci": () => ok("javascript:alert(1)"),
    });
    const { user } = renderWithProviders(<Harness />);
    await openDialog(user);
    await pickOption(user, await screen.findByRole("combobox", { name: "Repository" }), "acme/api");
    await user.click(await screen.findByRole("checkbox", { name: "Security Reviewer" }));
    await user.click(screen.getByRole("button", { name: "Install" }));

    expect(await screen.findByText("Installed")).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: "View pull request" })).not.toBeInTheDocument();
    expect(document.querySelector('a[href^="javascript:"]')).toBeNull();
  });

  it("offers 'Add repository' when no repository is connected", async () => {
    mockFetch({ "GET /repos": [], "GET /agents": [A1] });
    const { user } = renderWithProviders(<Harness />);
    await openDialog(user);

    expect(await screen.findByText(/No repository is connected yet/)).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Add repository" })).toHaveAttribute("href", "/settings/github");
    expect(screen.queryByRole("combobox", { name: "Repository" })).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Install" })).toBeDisabled();
  });

  it("Escape closes an open repository list first, then the dialog, and focus returns to the trigger (NFR5)", async () => {
    mockFetch({ "GET /repos": [REPO], "GET /agents": [A1] });
    const { user } = renderWithProviders(<Harness />);
    const trigger = screen.getByRole("button", { name: "open dialog" });
    await openDialog(user);

    await user.click(await screen.findByRole("combobox", { name: "Repository" }));
    expect(screen.getByRole("listbox")).toBeInTheDocument();
    await user.keyboard("{Escape}");
    expect(screen.queryByRole("listbox")).not.toBeInTheDocument();
    expect(screen.getByRole("dialog")).toBeInTheDocument();

    await user.keyboard("{Escape}");
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    expect(trigger).toHaveFocus();
  });

  it("shows a load error with Retry for the repositories", async () => {
    let fail = true;
    mockFetch({
      "GET /repos": () => (fail ? jsonResponse({ error: { code: "internal_error", message: "x" } }, 500) : [REPO]),
      "GET /agents": [A1],
    });
    const { user } = renderWithProviders(<Harness />);
    await openDialog(user);

    expect(await screen.findByText("Could not load the repositories", undefined, { timeout: 5000 })).toBeInTheDocument();
    fail = false;
    await user.click(screen.getByRole("button", { name: "Retry" }));
    expect(await screen.findByRole("combobox", { name: "Repository" })).toBeInTheDocument();
  });
});

describe("InstallActionDialog messages (AC22)", () => {
  it("takes every visible string from the message files (pseudo bundle), results and reasons included", async () => {
    mockFetch({
      "GET /repos": [REPO],
      "GET /agents": [A1, A2, CLAUDE],
      "POST /agents/:id/export-ci": (req) =>
        req.params.id === "a2" ? jsonResponse({ error: { code: "validation_error", message: "No model" } }, 422) : ok("https://github.com/acme/api/pull/1"),
    });
    const { user } = renderWithProviders(<Harness />, { messages: pseudoMessages() });
    await user.click(screen.getByRole("button", { name: "open dialog" }));
    await screen.findByRole("dialog");

    await pickOption(user, await screen.findByRole("combobox", { name: "⟦integrationsGithubAction.dialog.repoLabel⟧" }), "acme/api");
    await user.click(await screen.findByRole("checkbox", { name: "Security Reviewer" }));
    await user.click(screen.getByRole("checkbox", { name: "Style Reviewer" }));
    await user.click(screen.getByRole("button", { name: "⟦integrationsGithubAction.dialog.actions.install⟧" }));
    await screen.findByRole("alert");
    await screen.findByText("⟦integrationsGithubAction.dialog.progress.done⟧");

    // Data: the harness trigger, repo and agent names, and the server's error text.
    expect(strayText(document.body, ["open dialog", "acme/api", "Security Reviewer", "Style Reviewer", "Claude Reviewer"])).toEqual([]);
  });
});

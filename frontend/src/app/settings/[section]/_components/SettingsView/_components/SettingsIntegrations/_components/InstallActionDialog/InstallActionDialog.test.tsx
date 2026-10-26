/* InstallActionDialog — "Install in a repo" (FR3, EC13, AC3, NFR5). Rendered with a trigger
   button; the repo and agent lists and the install hook run for real against a stubbed fetch. */
import React from "react";
import { describe, it, expect, afterEach } from "vitest";
import type { Agent } from "@devdigest/shared";
import type { CustomProvider, Repo } from "@/lib/types";
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

const OPENAI_AGENT = agent("a8", "GPT Reviewer", { provider: "openai" });
const SYNAPSE_AGENT = agent("a9", "Synapse Reviewer", { provider: "custom:synapse" });
const GHOST_AGENT = agent("a10", "Ghost Reviewer", { provider: "custom:ghost" });

const SYNAPSE: CustomProvider = {
  id: "custom:synapse",
  name: "Synapse",
  protocol: "openai",
  base_url: "https://llm.acme.test/v1",
  plain_http: false,
  auth_mode: "bearer",
  auth_header: null,
  structured_output: "strict",
  key_configured: true,
  extra_headers: [],
  models: [],
  created_at: "2026-10-26T10:00:00.000Z",
  updated_at: "2026-10-26T10:00:00.000Z",
};

const ok = (pr: string | null, needs: { secrets?: string[]; variables?: string[] } = {}) =>
  jsonResponse(
    {
      installation: null,
      files: [],
      pr_url: pr,
      required_secrets: needs.secrets ?? ["OPENROUTER_API_KEY"],
      required_variables: needs.variables ?? [],
    },
    201,
  );

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
    mockFetch({
      "GET /repos": [REPO],
      "GET /agents": [A1, CLAUDE, OPENAI_AGENT, SYNAPSE_AGENT, GHOST_AGENT, HIDDEN, HIDDEN_CLAUDE, ON_PLUGIN],
      "GET /settings/custom-providers": [SYNAPSE],
    });
    const { user } = renderWithProviders(<Harness />);
    await openDialog(user);

    const install = screen.getByRole("button", { name: "Install" });
    expect(install).toBeDisabled();
    const security = await screen.findByRole("checkbox", { name: "Security Reviewer" });
    expect(screen.getByRole("checkbox", { name: "Pack On Reviewer" })).toBeEnabled();

    // openai, anthropic and a defined custom provider are installable; an undefined custom id is not (FR32, EC34).
    expect(screen.getByRole("checkbox", { name: "Claude Reviewer" })).toBeEnabled();
    expect(screen.getByRole("checkbox", { name: "GPT Reviewer" })).toBeEnabled();
    expect(await screen.findByRole("checkbox", { name: "Ghost Reviewer" })).toBeDisabled();
    expect(screen.getByRole("checkbox", { name: "Ghost Reviewer" })).toHaveAccessibleDescription(
      "Custom provider custom:ghost is not configured",
    );
    expect(screen.getByRole("checkbox", { name: "Synapse Reviewer" })).toBeEnabled();
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
      "GET /agents": [A1, A2, A3, GHOST_AGENT],
      "GET /settings/custom-providers": [],
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
      expect(r.body).toEqual({ repo: "acme/api", target: "gha", action: "open_pr", runner: "github_hosted" });
    }
    const links = screen.getAllByRole("link", { name: "View pull request" });
    expect(links[0]).toHaveAttribute("href", "https://github.com/acme/api/pull/1");
    expect(links[1]).toHaveAttribute("href", "https://github.com/acme/api/pull/3");
    expect(screen.getByRole("checkbox", { name: "Ghost Reviewer" })).toBeDisabled();

    a2Fails = false;
    await user.click(screen.getByRole("button", { name: "Retry failed" }));
    await waitFor(() => expect(screen.getAllByRole("link", { name: "View pull request" })).toHaveLength(3));
    expect(order).toEqual(["a1", "a2", "a3", "a2"]); // only the failed agent again
    expect(screen.queryByRole("button", { name: "Retry failed" })).not.toBeInTheDocument();
    expect(screen.queryByText(/^Failed:/)).not.toBeInTheDocument();
  });

  it("offers one Runner for the run, Self-hosted once a custom-provider agent is picked, and lists each agent's secrets and variables (FR32, AC37)", async () => {
    const api = mockFetch({
      "GET /repos": [REPO],
      "GET /agents": [A1, SYNAPSE_AGENT, CLAUDE, GHOST_AGENT],
      "GET /settings/custom-providers": [SYNAPSE],
      "POST /agents/:id/export-ci": (req) => {
        switch (req.params.id) {
          case "a9":
            return ok("https://github.com/acme/api/pull/2", {
              secrets: ["DEVDIGEST_SYNAPSE_API_KEY", "DEVDIGEST_SYNAPSE_HEADER_1"],
              variables: ["DEVDIGEST_SYNAPSE_BASE_URL"],
            });
          case "a4":
            return ok("https://github.com/acme/api/pull/3", { secrets: ["ANTHROPIC_API_KEY"] });
          default:
            return ok("https://github.com/acme/api/pull/1");
        }
      },
    });
    const { user } = renderWithProviders(<Harness />);
    await openDialog(user);
    await pickOption(user, await screen.findByRole("combobox", { name: "Repository" }), "acme/api");

    const hosted = screen.getByRole("radio", { name: "GitHub-hosted" });
    const selfHosted = screen.getByRole("radio", { name: "Self-hosted (linux x64)" });
    expect(hosted).toBeChecked();

    await user.click(await screen.findByRole("checkbox", { name: "Security Reviewer" }));
    await user.click(screen.getByRole("checkbox", { name: "Claude Reviewer" }));
    expect(hosted).toBeChecked(); // openrouter and anthropic stay GitHub-hosted
    expect(screen.getByRole("checkbox", { name: "Ghost Reviewer" })).toBeDisabled();
    expect(screen.getByRole("checkbox", { name: "Ghost Reviewer" })).toHaveAccessibleDescription(
      "Custom provider custom:ghost is not configured",
    );

    await user.click(screen.getByRole("checkbox", { name: "Synapse Reviewer" }));
    expect(selfHosted).toBeChecked();
    await user.click(screen.getByRole("checkbox", { name: "Synapse Reviewer" }));
    expect(hosted).toBeChecked(); // the default follows the selection
    await user.click(screen.getByRole("checkbox", { name: "Synapse Reviewer" }));
    expect(selfHosted).toBeChecked();

    await user.click(screen.getByRole("button", { name: "Install" }));
    await waitFor(() => expect(screen.getAllByRole("link", { name: "View pull request" })).toHaveLength(3));
    expect(api.requests("POST").map((r) => r.body)).toEqual([
      { repo: "acme/api", target: "gha", action: "open_pr", runner: "self_hosted" },
      { repo: "acme/api", target: "gha", action: "open_pr", runner: "self_hosted" },
      { repo: "acme/api", target: "gha", action: "open_pr", runner: "self_hosted" },
    ]);
    expect(selfHosted).toBeDisabled(); // locked once the run started

    const rowOf = (name: string) => screen.getByRole("checkbox", { name }).closest("li") as HTMLElement;
    const security = within(rowOf("Security Reviewer"));
    expect(security.getByText("OPENROUTER_API_KEY")).toBeInTheDocument();
    expect(security.queryByText("Variables to add:")).not.toBeInTheDocument();
    const synapse = within(rowOf("Synapse Reviewer"));
    expect(synapse.getByText("DEVDIGEST_SYNAPSE_API_KEY")).toBeInTheDocument();
    expect(synapse.getByText("DEVDIGEST_SYNAPSE_HEADER_1")).toBeInTheDocument();
    expect(synapse.getByText("DEVDIGEST_SYNAPSE_BASE_URL")).toBeInTheDocument();
    expect(within(rowOf("Claude Reviewer")).getByText("ANTHROPIC_API_KEY")).toBeInTheDocument();
  });

  it("keeps the Runner the user chose when the selection changes", async () => {
    const api = mockFetch({
      "GET /repos": [REPO],
      "GET /agents": [A1, SYNAPSE_AGENT],
      "GET /settings/custom-providers": [SYNAPSE],
      "POST /agents/:id/export-ci": () => ok("https://github.com/acme/api/pull/1"),
    });
    const { user } = renderWithProviders(<Harness />);
    await openDialog(user);
    await pickOption(user, await screen.findByRole("combobox", { name: "Repository" }), "acme/api");

    await user.click(await screen.findByRole("checkbox", { name: "Synapse Reviewer" }));
    await user.click(screen.getByRole("radio", { name: "GitHub-hosted" }));
    await user.click(screen.getByRole("button", { name: "Install" }));
    await screen.findByRole("link", { name: "View pull request" });
    expect(api.requests("POST")[0]!.body).toMatchObject({ runner: "github_hosted" });
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
      "GET /agents": [A1, A2, CLAUDE, GHOST_AGENT],
      "GET /settings/custom-providers": [],
      "POST /agents/:id/export-ci": (req) =>
        req.params.id === "a2"
          ? jsonResponse({ error: { code: "validation_error", message: "No model" } }, 422)
          : ok("https://github.com/acme/api/pull/1", { variables: ["DEVDIGEST_X_BASE_URL"] }),
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
    expect(screen.getByRole("checkbox", { name: "Ghost Reviewer" })).toHaveAccessibleDescription(
      "⟦integrationsGithubAction.dialog.reason.custom_not_configured⟧",
    );
    expect(
      strayText(document.body, [
        "open dialog",
        "acme/api",
        "Security Reviewer",
        "Style Reviewer",
        "Claude Reviewer",
        "Ghost Reviewer",
        "OPENROUTER_API_KEY",
        "DEVDIGEST_X_BASE_URL",
      ]),
    ).toEqual([]);
  });
});

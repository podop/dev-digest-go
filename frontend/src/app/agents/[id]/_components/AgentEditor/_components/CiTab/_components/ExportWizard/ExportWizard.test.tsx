/* ExportWizard — Runner choice, the custom provider's self-hosted note, the loopback warning and
   the secrets and variables read from the export response (FR31, EC21, AC36). Everything below
   the wizard runs for real against a stubbed fetch. */
import { describe, it, expect, afterEach } from "vitest";
import type { CiFile } from "@devdigest/shared";
import type { Repo } from "@/lib/types";
import { renderWithProviders, screen, cleanup, waitFor, within, pseudoMessages } from "@/test/render";
import { mockFetch, jsonResponse } from "@/test/fetch-mock";
import { strayText } from "@/test/pseudo";
import { ExportWizard } from "./ExportWizard";

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

const GITLAB_REPO: Repo = {
  ...REPO,
  id: "r2",
  owner: "grp",
  name: "svc",
  full_name: "grp/svc",
  provider: "gitlab",
  host: "gitlab.com",
  web_url: "https://gitlab.com/grp/svc",
  connection_id: "c1",
};

const FILES: CiFile[] = [
  { path: ".github/workflows/devdigest-review-synapse-bot.yml", contents: "name: review\n", editable: true },
  { path: ".devdigest/runner/index.js", contents: "// bundle", editable: false },
];

const provider = (baseUrl: string) => ({
  id: "custom:synapse",
  name: "Synapse",
  protocol: "openai",
  base_url: baseUrl,
  plain_http: false,
  auth_mode: "bearer",
  auth_header: null,
  structured_output: "strict",
  key_configured: true,
  extra_headers: [],
  models: [],
  created_at: "2026-10-26T10:00:00.000Z",
  updated_at: "2026-10-26T10:00:00.000Z",
});

const exported = (required_secrets: string[], required_variables: string[]) => ({
  installation: null,
  files: FILES,
  pr_url: null,
  required_secrets,
  required_variables,
});

const CUSTOM_EXPORT = exported(["DEVDIGEST_SYNAPSE_API_KEY"], ["DEVDIGEST_SYNAPSE_BASE_URL"]);

function stub(baseUrl: string, body: Record<string, unknown>) {
  return mockFetch({
    "GET /repos": [REPO],
    "GET /settings/custom-providers": [provider(baseUrl)],
    "POST /agents/:id/export-ci": body,
  });
}

function open(agentProvider: string) {
  return renderWithProviders(
    <ExportWizard agentId="a1" agentName="Synapse bot" provider={agentProvider} initialRepo="acme/api" onClose={() => {}} />,
  );
}

/** Target → Preview → Configure, once the preview has loaded. */
async function toConfigure(user: ReturnType<typeof open>["user"]) {
  await user.click(await screen.findByRole("button", { name: "Continue" }));
  const next = await screen.findByRole("button", { name: "Continue" });
  await waitFor(() => expect(next).toBeEnabled());
  await user.click(next);
  return screen.findByRole("group", { name: "Runner" });
}

const exportBodies = (http: ReturnType<typeof mockFetch>) =>
  http.requests("POST", "/agents/a1/export-ci").map((r) => r.body as Record<string, unknown>);

describe("ExportWizard — repository picker", () => {
  it("lists the repositories as radios keyed by id and sends the unchanged GitHub body (no repo_id)", async () => {
    const http = mockFetch({
      "GET /repos": [REPO, GITLAB_REPO],
      "GET /settings/custom-providers": [],
      "POST /agents/:id/export-ci": exported(["OPENROUTER_API_KEY"], []),
    });
    const { user } = renderWithProviders(
      <ExportWizard agentId="a1" agentName="Bot" provider="openrouter" onClose={() => {}} />,
    );

    const group = await screen.findByRole("group", { name: "Target repository" });
    expect(within(group).getAllByRole("radio").map((r) => r.getAttribute("value"))).toEqual(["r1", "r2"]);
    await user.click(within(group).getByRole("radio", { name: "acme/api" }));
    await user.click(screen.getByRole("button", { name: "Continue" }));

    await waitFor(() => expect(exportBodies(http)).toHaveLength(1));
    expect(exportBodies(http)[0]).toEqual({
      repo: "acme/api",
      target: "gha",
      action: "files",
      post_as: "github_review",
      triggers: ["opened", "synchronize"],
      runner: "github_hosted",
    });
  });
});

describe("ExportWizard — custom provider", () => {
  it("defaults to Self-hosted with the note, lists the response's secrets and variables with the base URL and a copy button, and re-requests the preview on a Runner change", async () => {
    const http = stub("https://gw.corp.example:8443/v1", CUSTOM_EXPORT);
    const { user } = open("custom:synapse");

    const runner = await toConfigure(user);
    expect(within(runner).getByRole("radio", { name: "Self-hosted (linux x64)" })).toBeChecked();
    expect(
      await within(runner).findByText(
        "This agent uses Synapse at gw.corp.example:8443. GitHub-hosted runners reach only public addresses; if gw.corp.example:8443 is on a private network, use a self-hosted runner that can reach it.",
      ),
    ).toBeInTheDocument();
    expect(screen.getByText("DEVDIGEST_SYNAPSE_API_KEY")).toBeInTheDocument();
    expect(screen.getByText("DEVDIGEST_SYNAPSE_BASE_URL")).toBeInTheDocument();
    expect(screen.getByText("https://gw.corp.example:8443/v1")).toBeInTheDocument();
    expect(screen.queryByText(/points to this machine/)).not.toBeInTheDocument();
    expect(exportBodies(http).map((b) => b.runner)).toEqual(["self_hosted"]);

    await user.click(within(runner).getByRole("radio", { name: "GitHub-hosted" }));
    await waitFor(() => expect(exportBodies(http).map((b) => b.runner)).toEqual(["self_hosted", "github_hosted"]));
    expect(exportBodies(http)[1]).toMatchObject({ action: "files", post_as: "github_review" });

    await user.click(screen.getByRole("button", { name: "Continue" }));
    expect(await screen.findByText("DEVDIGEST_SYNAPSE_API_KEY")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Copy the value of DEVDIGEST_SYNAPSE_BASE_URL" }));
    expect(await navigator.clipboard.readText()).toBe("https://gw.corp.example:8443/v1");
    expect(await screen.findByText("Copied")).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "Install" }));
    await waitFor(() => expect(exportBodies(http).at(-1)).toMatchObject({ action: "open_pr", runner: "github_hosted" }));
  });

  it("warns about a loopback base URL and names the variable (EC21)", async () => {
    stub("http://localhost:8080/v1", CUSTOM_EXPORT);
    const { user } = open("custom:synapse");

    await toConfigure(user);

    expect(await screen.findByRole("alert")).toHaveTextContent(
      "http://localhost:8080/v1 points to this machine; set DEVDIGEST_SYNAPSE_BASE_URL to an address the CI runner can reach.",
    );
  });

  it("shows the provider_not_configured error box as before when the export is refused", async () => {
    mockFetch({
      "GET /repos": [REPO],
      "GET /settings/custom-providers": [],
      "POST /agents/:id/export-ci": jsonResponse(
        { error: { code: "provider_not_configured", message: "Custom provider custom:ghost is not configured" } },
        422,
      ),
    });
    const { user } = open("custom:ghost");

    await user.click(await screen.findByRole("button", { name: "Continue" }));

    const box = await screen.findByRole("alert");
    expect(box).toHaveTextContent("provider_not_configured");
    expect(box).toHaveTextContent("Custom provider custom:ghost is not configured");
  });
});

describe("ExportWizard — built-in providers", () => {
  it.each([
    { provider: "openrouter", secrets: ["OPENROUTER_API_KEY"] },
    { provider: "openai", secrets: ["OPENAI_API_KEY"] },
  ])("$provider: GitHub-hosted by default, no note, its one key secret and no variable", async ({ provider: id, secrets }) => {
    const http = stub("https://gw.corp.example/v1", exported(secrets, []));
    const { user } = open(id);

    const runner = await toConfigure(user);

    expect(within(runner).getByRole("radio", { name: "GitHub-hosted" })).toBeChecked();
    expect(within(runner).getByRole("radio", { name: "Self-hosted (linux x64)" })).not.toBeChecked();
    expect(screen.getByText(secrets[0]!)).toBeInTheDocument();
    expect(screen.getByText("GITHUB_TOKEN")).toBeInTheDocument();
    expect(screen.queryByText(/This agent uses/)).not.toBeInTheDocument();
    expect(screen.queryByText("Variables expected")).not.toBeInTheDocument();
    expect(screen.queryByText(/points to this machine/)).not.toBeInTheDocument();
    expect(exportBodies(http)[0]).toMatchObject({ runner: "github_hosted" });
  });
});

describe("ExportWizard — messages", () => {
  it("renders every Configure string from the message files", async () => {
    stub("http://localhost:8080/v1", CUSTOM_EXPORT);
    const { user } = renderWithProviders(
      <ExportWizard agentId="a1" agentName="Synapse bot" provider="custom:synapse" initialRepo="acme/api" onClose={() => {}} />,
      { messages: pseudoMessages() },
    );

    await user.click(await screen.findByRole("button", { name: "⟦ci.exportWizard.continue⟧" }));
    const next = await screen.findByRole("button", { name: "⟦ci.exportWizard.continue⟧" });
    await waitFor(() => expect(next).toBeEnabled());
    await user.click(next);
    await screen.findByText("⟦ci.exportWizard.runnerLabel⟧");

    const dialog = screen.getByRole("dialog");
    const stray = strayText(dialog, [
      "DEVDIGEST_SYNAPSE_API_KEY",
      "DEVDIGEST_SYNAPSE_BASE_URL",
      "GITHUB_TOKEN",
      "http://localhost:8080/v1",
      /^pull_request:/,
      /^\d$/, // the stepper's step numbers
    ]);
    expect(stray).toEqual([]);
  });
});

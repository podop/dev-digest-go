/* SettingsApiKeys — the API Keys page as the user sees it: rows (FR2), badges (FR4), masking
   (FR3), test result (AC3), default model + spend cap fields (FR15) and the secrets info box;
   real hooks over a stubbed API. */
import { describe, it, expect, afterEach } from "vitest";
import type { ConnTestResult } from "@/lib/types";
import { renderWithProviders, screen, cleanup, within, waitFor, pseudoMessages } from "@/test/render";
import { mockFetch } from "@/test/fetch-mock";
import { strayText } from "@/test/pseudo";
import { SettingsApiKeys } from "./SettingsApiKeys";

afterEach(cleanup);

const SECRETS_FILE = "/data/devdigest/secrets.env";
const KEY = "sk-or-v1-abcdefgh1234";
const CONN = "POST /settings/test-connection";

function stubApi(extra: Record<string, unknown> = {}) {
  return mockFetch({
    "GET /settings/secrets-status": { openrouter: true, openai: false, anthropic: false, github: true },
    "GET /settings/custom-providers": [],
    "GET /settings": { feature_models: {}, default_model: null, monthly_spend_cap_usd: null },
    "GET /settings/spend": {
      month_start: "2026-10-01T00:00:00.000Z",
      spent_usd: 18.4,
      cap_usd: null,
      remaining_usd: null,
      cap_reached: false,
    },
    "GET /providers/openrouter/models": [{ id: "deepseek/deepseek-v4-flash", provider: "openrouter" }],
    "GET /about": {
      version: "dev",
      build_id: "dev",
      runner: { kind: "go", version: "2" },
      config_format: "devdigest-plugin/v1",
      data_dir: "/data/devdigest",
      secrets_file: SECRETS_FILE,
    },
    ...extra,
  });
}

/** The row (label + badge + input + Test) of the field labelled `label`. */
const row = (label: string) => within(screen.getByText(label).parentElement?.parentElement as HTMLElement);
const input = (label: string) => screen.getByRole("textbox", { name: label });

describe("SettingsApiKeys", () => {
  it("shows OpenRouter, OpenAI (optional) and Anthropic in order with their badges, and no GitHub row (AC2)", async () => {
    stubApi();
    renderWithProviders(<SettingsApiKeys />);

    expect(await screen.findByText("Connected")).toBeInTheDocument();
    const labels = screen.getAllByText(/ API key$/).map((el) => el.textContent);
    expect(labels).toEqual(["OpenRouter API key", "OpenAI API key", "Anthropic API key"]);
    expect(screen.queryByText(/GitHub|Personal access token/i, { selector: "label" })).toBeNull();
    expect(screen.queryByRole("link", { name: /Create a token/ })).toBeNull();

    expect(row("OpenRouter API key").getByText("Connected")).toBeInTheDocument();
    expect(row("OpenAI API key").getByText("Not connected")).toBeInTheDocument();
    expect(row("OpenAI API key").getByText("Optional")).toBeInTheDocument();
    expect(row("Anthropic API key").getByText("Not connected")).toBeInTheDocument();
    expect(row("OpenRouter API key").queryByText("Optional")).toBeNull();
    expect(screen.getByText("Used only when an agent or feature uses an OpenAI model.")).toBeInTheDocument();

    // Each row links to its provider's key page in a new tab.
    const links = screen.getAllByRole("link", { name: /Where to get your key/ });
    expect(links.map((l) => l.getAttribute("href"))).toEqual([
      "https://openrouter.ai/keys",
      "https://platform.openai.com/api-keys",
      "https://console.anthropic.com/settings/keys",
    ]);
    expect(links.map((l) => l.getAttribute("target"))).toEqual(["_blank", "_blank", "_blank"]);

    // A configured row shows the "Key saved" placeholder, an unset one the empty placeholder.
    expect(input("OpenRouter API key")).toHaveAttribute("placeholder", "Key saved — paste a new key to replace it");
    expect(input("OpenAI API key")).toHaveAttribute("placeholder", "Paste your key");
  });

  it("masks a typed key as 17 dots plus its last 4 and reveals it (AC2, FR3)", async () => {
    stubApi();
    const { user } = renderWithProviders(<SettingsApiKeys />);
    await screen.findByText("Connected");

    await user.type(input("OpenAI API key"), KEY);
    expect(input("OpenAI API key")).toHaveValue(`${"•".repeat(17)}1234`);
    await user.click(row("OpenAI API key").getByRole("button", { name: "Show key" }));
    expect(input("OpenAI API key")).toHaveValue(KEY);
    // The other rows are untouched.
    expect(input("Anthropic API key")).toHaveValue("");
  });

  it("tests one row's key and shows the verified banner and badge on it only (AC3)", async () => {
    const result: ConnTestResult = {
      provider: "anthropic",
      ok: true,
      message: "OK",
      model: "anthropic/claude-sonnet-4",
      model_reachable: true,
      latency_ms: 142,
    };
    const api = stubApi({ [CONN]: result });
    const { user } = renderWithProviders(<SettingsApiKeys />);
    await screen.findByText("Connected");

    await user.type(input("Anthropic API key"), KEY);
    await user.click(row("Anthropic API key").getByRole("button", { name: "Test connection" }));

    expect(await screen.findByText("Connection verified · anthropic/claude-sonnet-4 reachable · 142ms")).toBeInTheDocument();
    expect(row("Anthropic API key").getByText("Connected")).toBeInTheDocument();
    expect(row("OpenAI API key").getByText("Not connected")).toBeInTheDocument();
    expect(api.requests("POST", "/settings/test-connection")).toHaveLength(1);
    expect(api.requests("POST", "/settings/test-connection")[0]?.body).toEqual({ provider: "anthropic", key: KEY });
  });

  it("shows 'Failed' and the error banner after a failed test (AC3)", async () => {
    stubApi({ [CONN]: { provider: "openrouter", ok: false, message: "Invalid key" } satisfies ConnTestResult });
    const { user } = renderWithProviders(<SettingsApiKeys />);
    await screen.findByText("Connected");

    await user.click(row("OpenRouter API key").getByRole("button", { name: "Test connection" }));
    expect(await screen.findByText("Invalid key")).toHaveAttribute("role", "alert");
    expect(row("OpenRouter API key").getByText("Failed")).toBeInTheDocument();
  });

  it("composes the default model and spend cap fields and the secrets info box (FR15, AC15)", async () => {
    stubApi();
    renderWithProviders(<SettingsApiKeys />);

    expect(await screen.findByText("Default model")).toBeInTheDocument();
    expect(await screen.findByRole("textbox", { name: "Monthly spend cap in USD" })).toBeInTheDocument();
    expect(await screen.findByText(/\$18\.40 used this month/)).toBeInTheDocument();

    const box = await screen.findByText(/Keys live in/);
    expect(box).toHaveTextContent(`Keys live in ${SECRETS_FILE} on this machine.`);
    expect(box).toHaveTextContent("For CI, add the same key as a repository secret — see GitHub Integration.");
    expect(within(box).getByRole("link", { name: "GitHub Integration" })).toHaveAttribute("href", "/settings/github");
  });

  it("names GitLab CI/CD variables and links Integrations once a GitLab repository exists (GitLab C FR47, AC42, SCR5)", async () => {
    stubApi({ "GET /repos": [{ id: "r2", full_name: "acme/platform/api", provider: "gitlab", host: "gitlab.com" }] });
    renderWithProviders(<SettingsApiKeys />);

    const box = await screen.findByText(/Keys live in/);
    await waitFor(() =>
      expect(box).toHaveTextContent(
        "For CI, add the same key as a repository secret or GitLab CI/CD variable — see Integrations.",
      ),
    );
    expect(box).toHaveTextContent(`Keys live in ${SECRETS_FILE} on this machine.`);
    expect(within(box).getByRole("link", { name: "Integrations" })).toHaveAttribute("href", "/settings/integrations");
    expect(within(box).queryByRole("link", { name: "GitHub Integration" })).toBeNull();
  });

  it("shows the custom providers block below the built-in rows, above the default model (FR16, AC21)", async () => {
    stubApi({
      "GET /settings/custom-providers": [
        {
          id: "custom:synapse",
          name: "Synapse",
          protocol: "openai",
          base_url: "https://gw.example.com/v1",
          plain_http: false,
          auth_mode: "bearer",
          auth_header: null,
          structured_output: "strict",
          key_configured: true,
          extra_headers: [],
          models: [],
          created_at: "2026-10-26T00:00:00Z",
          updated_at: "2026-10-26T00:00:00Z",
        },
      ],
    });
    renderWithProviders(<SettingsApiKeys />);

    const heading = await screen.findByRole("heading", { name: "Custom providers" });
    expect(await screen.findByText("Synapse")).toBeInTheDocument();
    // the built-in rows stay as they were: three key fields, none for the custom provider
    expect(screen.getAllByText(/ API key$/).map((el) => el.textContent)).toEqual(["OpenRouter API key", "OpenAI API key", "Anthropic API key"]);
    const follows = (a: Node, b: Node) => Boolean(a.compareDocumentPosition(b) & Node.DOCUMENT_POSITION_FOLLOWING);
    expect(follows(screen.getByText("Anthropic API key"), heading)).toBe(true);
    expect(follows(heading, await screen.findByText("Default model"))).toBe(true);
  });

  it("shows no info box when /about fails", async () => {
    stubApi({ "GET /about": new Response("{}", { status: 500, headers: { "content-type": "application/json" } }) });
    renderWithProviders(<SettingsApiKeys />);
    await screen.findByText("Connected");
    await waitFor(() => expect(screen.queryByText(/Keys live in/)).toBeNull());
  });

  it("takes every visible string from the message files (pseudo bundle, AC22)", async () => {
    stubApi();
    const { container } = renderWithProviders(<SettingsApiKeys />, { messages: pseudoMessages() });
    await screen.findByText("⟦apiKeys.infoBox⟧");
    await screen.findAllByText("⟦keyField.badge.connected⟧");

    // Only data is left: the secrets path is wrapped by the rich-text tag, which pseudo messages lack.
    expect(strayText(container)).toEqual([]);
    expect(screen.getByText("⟦apiKeys.title⟧")).toBeInTheDocument();
    expect(screen.getByText("⟦customProviders.empty⟧")).toBeInTheDocument();
    expect(screen.getAllByText("⟦keyField.badge.notConnected⟧")).toHaveLength(2);
    expect(screen.getByText("⟦keyField.optional⟧")).toBeInTheDocument();
  });

  it("takes the GitLab CI copy of the info box from the message files too (pseudo bundle, GitLab C AC43)", async () => {
    stubApi({ "GET /repos": [{ id: "r2", full_name: "acme/platform/api", provider: "gitlab", host: "gitlab.com" }] });
    const { container } = renderWithProviders(<SettingsApiKeys />, { messages: pseudoMessages() });
    await screen.findByText("⟦apiKeys.infoBoxCiHosts⟧");
    await screen.findAllByText("⟦keyField.badge.connected⟧");
    expect(strayText(container)).toEqual([]);
  });
});

/* CustomProviders — the API Keys block: rows (AC21), test banners, delete confirmation and the 409
   "Used by:" answer (AC28), hard-coded copy (AC29). Real hooks over a stubbed fetch. */
import { describe, it, expect, afterEach } from "vitest";
import type { ConnTestResult, CustomProvider } from "@/lib/types";
import { renderWithProviders, screen, cleanup, waitFor, within, pseudoMessages } from "@/test/render";
import { mockFetch, jsonResponse } from "@/test/fetch-mock";
import { strayText } from "@/test/pseudo";
import { CustomProviders } from "./CustomProviders";

afterEach(cleanup);

const LIST = "GET /settings/custom-providers";
const base: CustomProvider = {
  id: "custom:synapse",
  name: "Synapse",
  protocol: "openai",
  base_url: "https://gw.example.com:8443/v1",
  plain_http: false,
  auth_mode: "bearer",
  auth_header: null,
  structured_output: "strict",
  key_configured: true,
  extra_headers: [],
  models: [],
  created_at: "2026-10-26T00:00:00Z",
  updated_at: "2026-10-26T00:00:00Z",
};
const LAB: CustomProvider = {
  ...base,
  id: "custom:lab",
  name: "Lab",
  protocol: "anthropic",
  base_url: "http://10.0.0.5:4000/v1",
  plain_http: true,
  key_configured: false,
  structured_output: "prompt_only",
};

/** The row (list item) of the provider named `name`. */
const row = (name: string) => within(screen.getByText(name).closest("li") as HTMLElement);

describe("CustomProviders", () => {
  it("lists providers with name, id, protocol, host, key badge, mode label and the plain-http warning (AC21)", async () => {
    mockFetch({ [LIST]: [base, LAB] });
    renderWithProviders(<CustomProviders />);

    expect(await screen.findByText("Synapse")).toBeInTheDocument();
    const synapse = row("Synapse");
    expect(synapse.getByText("custom:synapse")).toBeInTheDocument();
    expect(synapse.getByText("OpenAI-compatible")).toBeInTheDocument();
    expect(synapse.getByText("gw.example.com:8443")).toBeInTheDocument();
    expect(synapse.getByText("Key saved")).toBeInTheDocument();
    expect(synapse.queryByText("Key is sent unencrypted over HTTP")).toBeNull();
    expect(synapse.queryByText(/Structured output/)).toBeNull();

    const lab = row("Lab");
    expect(lab.getByText("Anthropic-compatible")).toBeInTheDocument();
    expect(lab.getByText("10.0.0.5:4000")).toBeInTheDocument();
    expect(lab.getByText("No key")).toBeInTheDocument();
    expect(lab.getByText("Key is sent unencrypted over HTTP")).toBeInTheDocument();
    expect(lab.getByText("Structured output: Prompt only")).toBeInTheDocument();
  });

  it("shows the empty state with Add, and the load error with Retry (AC21)", async () => {
    let answer: Response | unknown[] = jsonResponse({ error: { code: "internal_error", message: "boom" } }, 500);
    mockFetch({ [LIST]: () => answer });
    const { user } = renderWithProviders(<CustomProviders />);

    expect(await screen.findByRole("alert")).toHaveTextContent("Could not load custom providers");
    answer = [];
    await user.click(screen.getByRole("button", { name: "Retry" }));
    expect(await screen.findByText("No custom providers yet")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Add custom provider" })).toBeInTheDocument();
  });

  it("Add opens the dialog and Edit opens it for that provider; closing it keeps the list", async () => {
    mockFetch({ [LIST]: [base] });
    const { user } = renderWithProviders(<CustomProviders />);
    await screen.findByText("Synapse");

    await user.click(screen.getByRole("button", { name: "Add custom provider" }));
    expect(screen.getByText("Add custom provider", { selector: "div" })).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Cancel" }));
    expect(screen.queryByRole("dialog")).toBeNull();

    await user.click(screen.getByRole("button", { name: "Edit Synapse" }));
    expect(within(screen.getByRole("dialog")).getByText("Edit Synapse")).toBeInTheDocument();
    await user.keyboard("{Escape}");
    expect(screen.queryByRole("dialog")).toBeNull();
    expect(screen.getByRole("button", { name: "Edit Synapse" })).toHaveFocus();
  });

  it("Test shows the verified banner, or the warning banner for a result with a warning (AC28)", async () => {
    let result: ConnTestResult = { provider: "custom:synapse", ok: true, message: "OK — 3 models available", model: "syn-large", model_reachable: true, latency_ms: 42 };
    const api = mockFetch({ [LIST]: [base], "POST /settings/test-connection": () => result });
    const { user } = renderWithProviders(<CustomProviders />);
    await screen.findByText("Synapse");

    await user.click(screen.getByRole("button", { name: "Test Synapse" }));
    expect(await screen.findByText("Connection verified · syn-large reachable · 42ms")).toHaveAttribute("role", "status");
    expect(api.requests("POST", "/settings/test-connection")[0]!.body).toEqual({ provider: "custom:synapse" });

    result = { provider: "custom:synapse", ok: true, message: "OK", warning: "Model list not available; key not verified", model_reachable: null };
    await user.click(screen.getByRole("button", { name: "Test Synapse" }));
    expect(await screen.findByText("Model list not available; key not verified")).toHaveAttribute("role", "status");
    expect(screen.queryByText(/Connection verified/)).toBeNull();
  });

  it("Delete confirms naming the provider: Cancel sends nothing, Confirm sends one DELETE and the row goes (AC28)", async () => {
    let list: CustomProvider[] = [base, LAB];
    const api = mockFetch({ [LIST]: () => list, "DELETE /settings/custom-providers/:id": () => { list = [LAB]; return { deleted: "custom:synapse" }; } });
    const { user } = renderWithProviders(<CustomProviders />);
    await screen.findByText("Synapse");

    await user.click(screen.getByRole("button", { name: "Delete Synapse" }));
    const dialog = screen.getByRole("dialog");
    expect(within(dialog).getByText("Delete Synapse?")).toBeInTheDocument();
    await user.click(within(dialog).getByRole("button", { name: "Cancel" }));
    expect(api.requests("DELETE")).toHaveLength(0);

    await user.click(screen.getByRole("button", { name: "Delete Synapse" }));
    await user.click(within(screen.getByRole("dialog")).getByRole("button", { name: "Delete" }));
    await waitFor(() => expect(screen.queryByText("Synapse")).toBeNull());
    expect(api.requests("DELETE")).toHaveLength(1);
    expect(api.requests("DELETE")[0]!.path).toBe("/settings/custom-providers/custom%3Asynapse");
    expect(screen.getByText("Lab")).toBeInTheDocument();
  });

  it("a 409 provider_in_use lists the agent, the feature and Default model and keeps the row (AC28)", async () => {
    const details = { agents: [{ id: "a1", name: "Reviewer" }], feature_models: ["conventions"], default_model: true };
    const api = mockFetch({
      [LIST]: [base],
      "DELETE /settings/custom-providers/:id": jsonResponse({ error: { code: "provider_in_use", message: "Provider is in use", details } }, 409),
    });
    const { user } = renderWithProviders(<CustomProviders />);
    await screen.findByText("Synapse");

    await user.click(screen.getByRole("button", { name: "Delete Synapse" }));
    await user.click(within(screen.getByRole("dialog")).getByRole("button", { name: "Delete" }));

    const used = await screen.findByText("Used by:");
    const box = within(used.parentElement as HTMLElement);
    expect(box.getByText("Reviewer")).toBeInTheDocument();
    expect(box.getByText("Conventions")).toBeInTheDocument();
    expect(box.getByText("Default model")).toBeInTheDocument();
    expect(api.requests("DELETE")).toHaveLength(1);
    expect(screen.getAllByText("Synapse").length).toBeGreaterThan(0);
    expect(row("Synapse").getByText("custom:synapse")).toBeInTheDocument();
  });

  it("takes every visible string from the message files (pseudo bundle, AC29)", async () => {
    const details = { agents: [{ id: "a1", name: "Reviewer" }], feature_models: ["conventions"], default_model: true };
    mockFetch({
      [LIST]: [base, LAB],
      "DELETE /settings/custom-providers/:id": jsonResponse({ error: { code: "provider_in_use", message: "in use", details } }, 409),
    });
    const { container, user } = renderWithProviders(<CustomProviders />, { messages: pseudoMessages() });
    await screen.findByText("Synapse");
    await user.click(screen.getAllByRole("button", { name: "⟦customProviders.row.deleteAria⟧" })[0]!);
    await user.click(within(screen.getByRole("dialog")).getByRole("button", { name: "⟦customProviders.delete.confirm⟧" }));
    await screen.findByText("⟦customProviders.delete.usedBy⟧");

    // Left over: provider names, ids, hosts, and the agent / feature names the API returned.
    expect(strayText(container, ["Synapse", "Lab", "custom:synapse", "custom:lab", "gw.example.com:8443", "10.0.0.5:4000", "Reviewer", "Conventions"])).toEqual([]);
  });
});

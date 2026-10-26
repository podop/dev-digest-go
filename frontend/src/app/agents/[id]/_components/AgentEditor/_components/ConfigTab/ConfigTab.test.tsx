import { describe, it, expect, afterEach, beforeEach } from "vitest";
import { renderWithProviders, screen, cleanup, waitFor } from "@/test/render";
import { mockFetch } from "@/test/fetch-mock";
import { pickOption } from "@/test/select";
import type { Agent } from "@devdigest/shared";
import { ConfigTab } from "./ConfigTab";

// The real useUpdateAgent / useProviderModels run against a stubbed API.
let api: ReturnType<typeof mockFetch>;
beforeEach(() => {
  api = mockFetch({
    "GET /settings/custom-providers": [
      { id: "custom:synapse", name: "Synapse" },
      { id: "custom:lab", name: "Lab" },
    ],
    "GET /providers/:provider/models": [],
    "GET /providers/openai/models": [{ id: "gpt-4.1", provider: "openai" }],
    "PUT /agents/:id": (req) => ({ ...AGENT, ...(req.body as object) }),
  });
});
afterEach(cleanup);

const AGENT: Agent = {
  id: "ag1",
  name: "Security Reviewer",
  description: "Flags secrets and injection",
  provider: "openai",
  model: "gpt-4.1",
  system_prompt: "You are a security reviewer.",
  output_schema: null,
  strategy: "single-pass",
  ci_fail_on: "critical",
  repo_intel: true,
  enabled: true,
  version: 1,
};

/** Body of the (single) PUT /agents/ag1 the Save button sent. */
async function savedPatch() {
  await waitFor(() => expect(api.requests("PUT", "/agents/ag1")).toHaveLength(1));
  return api.requests("PUT", "/agents/ag1")[0]!.body as Partial<Agent>;
}

describe("ConfigTab save", () => {
  it("does not revert 'enabled' toggled elsewhere (agent list) after the form mounted", async () => {
    const { rerender, user } = renderWithProviders(<ConfigTab agent={AGENT} />);
    // AgentCard toggle → cache update → the page re-renders with fresh server data.
    rerender(<ConfigTab agent={{ ...AGENT, enabled: false }} />);
    await user.type(screen.getByDisplayValue("Security Reviewer"), "!");
    await user.click(screen.getByRole("button", { name: "Save agent" }));
    expect((await savedPatch()).enabled).not.toBe(true);
  });

  it("Save is disabled until a field is edited (an empty PUT is never sent)", async () => {
    const { user } = renderWithProviders(<ConfigTab agent={AGENT} />);
    const save = screen.getByRole("button", { name: "Save agent" });
    expect(save).toBeDisabled();
    await user.type(screen.getByDisplayValue("Security Reviewer"), "!");
    expect(save).toBeEnabled();
  });

  it("sends only the fields the user edited", async () => {
    const { user } = renderWithProviders(<ConfigTab agent={AGENT} />);
    const name = screen.getByDisplayValue("Security Reviewer");
    await user.clear(name);
    await user.type(name, "Sec v2");
    await user.click(screen.getByRole("button", { name: "Save agent" }));
    expect(await savedPatch()).toEqual({ name: "Sec v2" });
  });

  it("shows fresh server values for fields the user has not touched", () => {
    const { rerender } = renderWithProviders(<ConfigTab agent={AGENT} />);
    rerender(<ConfigTab agent={{ ...AGENT, description: "Updated elsewhere" }} />);
    expect(screen.getByDisplayValue("Updated elsewhere")).toBeInTheDocument();
  });

  it("picking a new provider from the themed Select saves only that field", async () => {
    const { user } = renderWithProviders(<ConfigTab agent={AGENT} />);
    await pickOption(user, screen.getByRole("combobox", { name: "Provider" }), "Anthropic");
    await user.click(screen.getByRole("button", { name: "Save agent" }));
    expect(await savedPatch()).toEqual({ provider: "anthropic" });
  });

  it("lists the built-ins, then the custom providers by name, and loads the picked provider's models (AC24)", async () => {
    const { user } = renderWithProviders(<ConfigTab agent={AGENT} />);
    await user.click(screen.getByRole("combobox", { name: "Provider" }));
    await waitFor(() => expect(screen.getAllByRole("option")).toHaveLength(5));
    expect(screen.getAllByRole("option").map((o) => o.textContent)).toEqual([
      "OpenRouter",
      "OpenAI",
      "Anthropic",
      "Lab",
      "Synapse",
    ]);
    await user.click(screen.getByRole("option", { name: "Synapse" }));

    await waitFor(() => expect(api.requests("GET", "/providers/custom%3Asynapse/models")).toHaveLength(1));
    await user.click(screen.getByRole("button", { name: "Save agent" }));
    expect(await savedPatch()).toEqual({ provider: "custom:synapse" });
  });

  it("shows an undefined custom provider as Not configured and keeps Save disabled until another is picked (EC23)", async () => {
    const { user } = renderWithProviders(<ConfigTab agent={{ ...AGENT, provider: "custom:ghost" }} />);
    const box = screen.getByRole("combobox", { name: "Provider" });
    expect(await screen.findByText("This provider is not configured. Pick another provider to save.")).toBeInTheDocument();
    expect(box).toHaveTextContent("custom:ghost · Not configured");

    // editing another field does not unblock it
    await user.type(screen.getByDisplayValue("Security Reviewer"), "!");
    const save = screen.getByRole("button", { name: "Save agent" });
    expect(save).toBeDisabled();

    await pickOption(user, box, "OpenAI");
    expect(save).toBeEnabled();
    await user.click(save);
    expect(await savedPatch()).toEqual({ name: "Security Reviewer!", provider: "openai" });
  });
});

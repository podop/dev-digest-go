/* SettingsModels — Feature Models pickers: a feature without its own choice shows the workspace
   default model (FR10, AC13); real hooks over a stubbed API. */
import { describe, it, expect, afterEach } from "vitest";
import { renderWithProviders, screen, cleanup, waitFor, within, pseudoMessages } from "@/test/render";
import { mockFetch } from "@/test/fetch-mock";
import { pickOption } from "@/test/select";
import { strayText } from "@/test/pseudo";
import { FEATURE_MODELS } from "@devdigest/shared/constants/feature-models";
import { SettingsModels } from "./SettingsModels";

afterEach(cleanup);

type Choice = { provider: string; model: string };

const stubApi = (settings: { feature_models?: Record<string, Choice>; default_model: Choice | null }) =>
  mockFetch({
    "GET /settings": { feature_models: {}, ...settings },
    "GET /settings/custom-providers": [
      { id: "custom:synapse", name: "Synapse" },
      { id: "custom:lab", name: "Lab" },
    ],
    "GET /providers/custom%3Asynapse/models": [{ id: "syn-large", provider: "custom:synapse" }],
    "GET /providers/openrouter/models": [
      { id: "qwen/qwen3-coder", provider: "openrouter" },
      { id: "deepseek/deepseek-v4-flash", provider: "openrouter" },
    ],
  });

const featureRow = (label: string) => screen.getByText(label).closest("label")?.parentElement?.parentElement as HTMLElement;

describe("SettingsModels default model", () => {
  it("shows features without their own choice as using the workspace default model", async () => {
    stubApi({
      default_model: { provider: "openai", model: "gpt-4.1" },
      feature_models: { risk_brief: { provider: "openrouter", model: "qwen/qwen3-coder" } },
    });
    renderWithProviders(<SettingsModels />);

    // Every feature but the overridden one is tagged with the default model's label.
    const tags = await screen.findAllByText("Default model (openai/gpt-4.1)");
    expect(tags).toHaveLength(FEATURE_MODELS.length - 1);
    expect(screen.queryByText("default")).not.toBeInTheDocument();

    const risk = within(featureRow("Risk Brief"));
    expect(risk.queryByText(/Default model/)).not.toBeInTheDocument();
    expect(risk.getByText("qwen/qwen3-coder")).toBeInTheDocument();
    // The picker value is the default model, not the feature's registry default.
    expect(within(featureRow("Onboarding Tour")).getByText("gpt-4.1")).toBeInTheDocument();
    expect(screen.queryByText("deepseek/deepseek-v4-flash")).not.toBeInTheDocument();
  });

  it("keeps the registry default and the plain 'default' tag when no default model is set", async () => {
    stubApi({ default_model: null });
    renderWithProviders(<SettingsModels />);

    expect(await screen.findAllByText("default")).toHaveLength(FEATURE_MODELS.length);
    expect(screen.queryByText(/Default model \(/)).not.toBeInTheDocument();
    expect(within(featureRow("Risk Brief")).getByText("gpt-4.1")).toBeInTheDocument();
  });

  it("saves a pick as the feature's own choice", async () => {
    const api = stubApi({ default_model: { provider: "openai", model: "gpt-4.1" } });
    const { user } = renderWithProviders(<SettingsModels />);
    await screen.findAllByText("Default model (openai/gpt-4.1)");
    await waitFor(() => expect(api.requests("GET", "/providers/openrouter/models")).toHaveLength(1));

    await user.click(within(featureRow("Conformance")).getByText("gpt-4.1"));
    await user.click(await screen.findByText("qwen/qwen3-coder"));

    await waitFor(() => expect(api.requests("PUT", "/settings")).toHaveLength(1));
    expect(api.requests("PUT", "/settings")[0]!.body).toEqual({
      feature_models: { conformance: { provider: "openrouter", model: "qwen/qwen3-coder" } },
    });
  });

  it("takes every visible string from the message files (pseudo bundle)", async () => {
    stubApi({ default_model: { provider: "openai", model: "gpt-4.1" } });
    const { container } = renderWithProviders(<SettingsModels />, { messages: pseudoMessages() });
    await screen.findAllByText("⟦settings.models.usingDefaultModel⟧");

    const data = [
      "gpt-4.1",
      ...FEATURE_MODELS.flatMap((f) => [f.label, f.description]),
    ];
    expect(strayText(container, data)).toEqual([]);
  });

  it("offers a provider choice per feature: built-ins, then custom by name; a pick saves {provider, model} (AC24)", async () => {
    const api = stubApi({ default_model: null });
    const { user } = renderWithProviders(<SettingsModels />);
    await screen.findAllByText("default");

    const providerBox = (label: string) => within(featureRow(label)).getByRole("combobox", { name: `${label} provider` });
    for (const f of FEATURE_MODELS) expect(providerBox(f.label)).toHaveTextContent("OpenRouter");

    await user.click(providerBox("Conformance"));
    await waitFor(() => expect(screen.getAllByRole("option")).toHaveLength(5));
    expect(screen.getAllByRole("option").map((o) => o.textContent)).toEqual([
      "OpenRouter",
      "OpenAI",
      "Anthropic",
      "Lab",
      "Synapse",
    ]);
    await user.click(screen.getByRole("option", { name: "Synapse" }));

    // the provider switch alone saves nothing; the model list of the custom provider loads
    await waitFor(() => expect(api.requests("GET", "/providers/custom%3Asynapse/models")).toHaveLength(1));
    expect(api.requests("PUT", "/settings")).toHaveLength(0);
    await user.click(within(featureRow("Conformance")).getByText("Search models…"));
    await user.click(await screen.findByText("syn-large"));

    await waitFor(() => expect(api.requests("PUT", "/settings")).toHaveLength(1));
    expect(api.requests("PUT", "/settings")[0]!.body).toEqual({
      feature_models: { conformance: { provider: "custom:synapse", model: "syn-large" } },
    });
  });

  it("shows an undefined custom provider as Not configured and offers no model until another is picked (EC23)", async () => {
    const api = stubApi({
      default_model: null,
      feature_models: { conventions: { provider: "custom:ghost", model: "m1" } },
    });
    const { user } = renderWithProviders(<SettingsModels />);

    const row = () => within(featureRow("Conventions"));
    expect(await row().findByText("custom:ghost · Not configured")).toBeInTheDocument();
    expect(row().getByText("This provider is not configured. Pick another provider to save.")).toBeInTheDocument();
    expect(row().queryByText("Search models…")).not.toBeInTheDocument();
    expect(row().queryByText("m1")).not.toBeInTheDocument();

    await pickOption(user, row().getByRole("combobox", { name: "Conventions provider" }), "OpenRouter");
    await waitFor(() => expect(row().getByText("Search models…")).toBeInTheDocument());
    expect(api.requests("PUT", "/settings")).toHaveLength(0);
  });
});

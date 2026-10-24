import { describe, it, expect, afterEach } from "vitest";
import { renderWithProviders, screen, cleanup, waitFor, pseudoMessages } from "@/test/render";
import { mockFetch, jsonResponse } from "@/test/fetch-mock";
import { pickOption } from "@/test/select";
import { strayText } from "@/test/pseudo";
import { DefaultModelField } from "./DefaultModelField";

afterEach(cleanup);

const NOT_SET = "Not set — features use their built-in defaults";
const SECRETS = { openai: true, anthropic: true, openrouter: false, github: false };

function mockModels(
  over: { settings?: Record<string, unknown>; secrets?: Record<string, unknown>; models?: Record<string, unknown> } = {},
) {
  const models = over.models ?? {
    openrouter: [
      { id: "deepseek/deepseek-v4-flash", provider: "openrouter" },
      { id: "z-ai/glm-5", provider: "openrouter" },
    ],
    anthropic: [{ id: "claude-sonnet-5", provider: "anthropic" }],
  };
  return mockFetch({
    "GET /settings": over.settings ?? { default_model: null },
    "GET /settings/secrets-status": over.secrets ?? SECRETS,
    "GET /providers/:provider/models": (req) => models[req.params.provider!] ?? [],
    "PUT /settings": (req) => req.body as Record<string, unknown>,
  });
}

const providerBox = () => screen.getByRole("combobox", { name: "Default model provider" });

describe("DefaultModelField", () => {
  it("lists the provider's models, saves {provider, model}, warns without a key and has no clear control (AC13)", async () => {
    const api = mockModels();
    const { user } = renderWithProviders(<DefaultModelField />);

    expect(await screen.findByText("New agents and features without their own model use this one.")).toBeInTheDocument();
    expect(await screen.findByText("No OpenRouter key configured")).toBeInTheDocument();

    await user.click(screen.getByText(NOT_SET));
    expect(await screen.findByRole("button", { name: "z-ai/glm-5" })).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "deepseek/deepseek-v4-flash" }));
    await waitFor(() => expect(api.requests("PUT", "/settings")).toHaveLength(1));
    expect(api.requests("PUT", "/settings")[0]!.body).toEqual({
      default_model: { provider: "openrouter", model: "deepseek/deepseek-v4-flash" },
    });

    // another provider: its own list, and no warning because it has a key
    await pickOption(user, providerBox(), "Anthropic");
    expect(await screen.findByText("Select a model")).toBeInTheDocument();
    expect(screen.queryByText(/key configured/)).not.toBeInTheDocument();
    await user.click(screen.getByText("Select a model"));
    await user.click(await screen.findByRole("button", { name: "claude-sonnet-5" }));
    await waitFor(() => expect(api.requests("PUT", "/settings")).toHaveLength(2));
    expect(api.requests("PUT", "/settings")[1]!.body).toEqual({
      default_model: { provider: "anthropic", model: "claude-sonnet-5" },
    });

    expect(screen.queryByRole("button", { name: /clear|remove|reset|unset/i })).not.toBeInTheDocument();
  });

  it("shows the saved default and warns when its provider has no key", async () => {
    mockModels({
      settings: { default_model: { provider: "openai", model: "gpt-4.1" } },
      secrets: { ...SECRETS, openai: false },
      models: { openai: [{ id: "gpt-4.1", provider: "openai" }] },
    });
    renderWithProviders(<DefaultModelField />);

    expect(await screen.findByText("gpt-4.1")).toBeInTheDocument();
    expect(providerBox()).toHaveTextContent("OpenAI");
    expect(await screen.findByText("No OpenAI key configured")).toBeInTheDocument();
  });

  it("falls back to a free-text model id when the list is empty or failed (FR8)", async () => {
    const api = mockModels({ models: {} });
    const { user } = renderWithProviders(<DefaultModelField />);

    const box = await screen.findByRole("textbox", { name: "Default model" });
    expect(screen.getByText("No model list is available for OpenRouter. Type a model id.")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Save" })).toBeDisabled();
    await user.type(box, "  acme/custom-1 ");
    await user.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(api.requests("PUT", "/settings")).toHaveLength(1));
    expect(api.requests("PUT", "/settings")[0]!.body).toEqual({
      default_model: { provider: "openrouter", model: "acme/custom-1" },
    });
  });

  it("uses the free-text field when the model list request fails and reports a failed save", async () => {
    const api = mockFetch({
      "GET /settings": { default_model: null },
      "GET /settings/secrets-status": SECRETS,
      "GET /providers/:provider/models": jsonResponse({ error: { code: "bad_gateway", message: "no key" } }, 502),
      "PUT /settings": jsonResponse({ error: { code: "validation_error", message: "invalid" } }, 422),
    });
    const { user } = renderWithProviders(<DefaultModelField />);

    await user.type(await screen.findByRole("textbox", { name: "Default model" }), "x{Enter}");
    expect(await screen.findByRole("alert")).toHaveTextContent("Could not save the default model.");
    expect(api.requests("PUT", "/settings")).toHaveLength(1);
  });

  it("renders no hard-coded copy under pseudo messages (AC22)", async () => {
    mockModels({ settings: { default_model: { provider: "openrouter", model: "z-ai/glm-5" } } });
    const { container } = renderWithProviders(<DefaultModelField />, { messages: pseudoMessages() });

    await screen.findByText(/settingsBudget\.defaultModel\.noKey/);
    // the picker trigger shows the saved model id, which is data
    expect(strayText(container, ["z-ai/glm-5"])).toEqual([]);
  });
});

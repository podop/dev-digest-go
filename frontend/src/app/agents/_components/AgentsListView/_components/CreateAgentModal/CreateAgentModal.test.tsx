/* CreateAgentModal — the model is picked from the selected provider's live
   model list (real hooks over a stubbed API), not typed by hand. */
import { describe, it, expect, afterEach, beforeEach, vi } from "vitest";
import { renderWithProviders, screen, cleanup, waitFor } from "@/test/render";
import { mockFetch } from "@/test/fetch-mock";
import { pickOption } from "@/test/select";

const nav = vi.hoisted(() => ({ push: vi.fn() }));
vi.mock("next/navigation", () => ({ useRouter: () => nav }));

import { CreateAgentModal } from "./CreateAgentModal";

let api: ReturnType<typeof mockFetch>;
const stubApi = (default_model: { provider: string; model: string } | null) => {
  api = mockFetch({
    "GET /settings": { feature_models: {}, default_model },
    "GET /settings/custom-providers": [
      { id: "custom:synapse", name: "Synapse" },
      { id: "custom:lab", name: "Lab" },
    ],
    "GET /providers/custom%3Asynapse/models": [{ id: "syn-large", provider: "custom:synapse" }],
    "GET /providers/openai/models": [{ id: "gpt-4.1", provider: "openai" }],
    "GET /providers/openrouter/models": [
      { id: "openai/gpt-4.1", provider: "openrouter" },
      { id: "qwen/qwen3-coder", provider: "openrouter" },
    ],
    "POST /agents": (req) => ({ id: "ag1", ...(req.body as object) }),
  });
};
beforeEach(() => stubApi(null));
afterEach(cleanup);

describe("CreateAgentModal model picker", () => {
  it("lists the selected provider's models and creates the agent with the picked one", async () => {
    const { user } = renderWithProviders(<CreateAgentModal onClose={() => {}} />);
    await pickOption(user, screen.getByRole("combobox", { name: "Provider" }), "OpenRouter");

    // The openai default is not an OpenRouter id: the model is cleared and Create waits for a pick.
    expect(screen.queryByText("gpt-4.1")).not.toBeInTheDocument();
    const create = screen.getByRole("button", { name: "Create agent" });
    expect(create).toBeDisabled();

    await user.click(screen.getByText("Search models…"));
    await user.click(await screen.findByText("qwen/qwen3-coder"));
    await user.click(create);

    await waitFor(() => expect(api.requests("POST", "/agents")).toHaveLength(1));
    expect(api.requests("POST", "/agents")[0]!.body).toMatchObject({
      provider: "openrouter",
      model: "qwen/qwen3-coder",
    });
  });

  it("keeps the built-in openai/gpt-4.1 when no workspace default model is set", async () => {
    const { user } = renderWithProviders(<CreateAgentModal onClose={() => {}} />);
    await waitFor(() => expect(api.requests("GET", "/settings")).toHaveLength(1));

    expect(screen.getByRole("combobox", { name: "Provider" })).toHaveTextContent("OpenAI");
    expect(screen.getByText("gpt-4.1")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Create agent" }));

    await waitFor(() => expect(api.requests("POST", "/agents")).toHaveLength(1));
    expect(api.requests("POST", "/agents")[0]!.body).toMatchObject({ provider: "openai", model: "gpt-4.1" });
  });

  it("preselects the workspace default model, and restores it when switching back to its provider", async () => {
    stubApi({ provider: "openrouter", model: "qwen/qwen3-coder" });
    const { user } = renderWithProviders(<CreateAgentModal onClose={() => {}} />);

    // Settings arrive after the modal opened: the picker follows them.
    expect(await screen.findByText("qwen/qwen3-coder")).toBeInTheDocument();
    expect(screen.getByRole("combobox", { name: "Provider" })).toHaveTextContent("OpenRouter");
    expect(screen.getByRole("button", { name: "Create agent" })).toBeEnabled();

    // A manual provider switch still clears the model; switching back restores the default.
    await pickOption(user, screen.getByRole("combobox", { name: "Provider" }), "Anthropic");
    expect(screen.queryByText("qwen/qwen3-coder")).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Create agent" })).toBeDisabled();
    await pickOption(user, screen.getByRole("combobox", { name: "Provider" }), "OpenRouter");
    expect(screen.getByText("qwen/qwen3-coder")).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "Create agent" }));
    await waitFor(() => expect(api.requests("POST", "/agents")).toHaveLength(1));
    expect(api.requests("POST", "/agents")[0]!.body).toMatchObject({
      provider: "openrouter",
      model: "qwen/qwen3-coder",
    });
  });

  it("lists the custom providers after the built-ins and creates the agent on the picked one (AC24)", async () => {
    const { user } = renderWithProviders(<CreateAgentModal onClose={() => {}} />);
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
    await user.click(screen.getByText("Search models…"));
    await user.click(await screen.findByText("syn-large"));
    await user.click(screen.getByRole("button", { name: "Create agent" }));
    await waitFor(() => expect(api.requests("POST", "/agents")).toHaveLength(1));
    expect(api.requests("POST", "/agents")[0]!.body).toMatchObject({ provider: "custom:synapse", model: "syn-large" });
  });

  it("blocks Create while the workspace default model is on an undefined custom provider (EC23)", async () => {
    stubApi({ provider: "custom:ghost", model: "m1" });
    const { user } = renderWithProviders(<CreateAgentModal onClose={() => {}} />);

    expect(await screen.findByText("custom:ghost · Not configured")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Create agent" })).toBeDisabled();

    await pickOption(user, screen.getByRole("combobox", { name: "Provider" }), "OpenAI");
    await user.click(screen.getByText("Search models…"));
    await user.click(await screen.findByText("gpt-4.1"));
    expect(screen.getByRole("button", { name: "Create agent" })).toBeEnabled();
  });
});

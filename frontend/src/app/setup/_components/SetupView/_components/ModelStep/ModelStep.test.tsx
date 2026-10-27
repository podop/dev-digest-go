import { afterEach, describe, expect, it, vi } from "vitest";
import type { ConnTestProvider, SecretsStatus } from "@/lib/types";
import { renderWithProviders, screen, cleanup, waitFor, pseudoMessages } from "@/test/render";
import { mockFetch } from "@/test/fetch-mock";
import { strayText } from "@/test/pseudo";
import { ModelStep } from "./ModelStep";

afterEach(cleanup);

const NONE: SecretsStatus = { openai: false, anthropic: false, openrouter: false, github: false };
const cont = () => screen.getByRole("button", { name: "Continue" });

function mockTest(ok: boolean) {
  return mockFetch({
    "POST /settings/test-connection": (req) => {
      const { provider } = req.body as { provider: ConnTestProvider };
      return ok ? { provider, ok, message: "OK" } : { provider, ok, message: "Invalid key" };
    },
  });
}

describe("ModelStep", () => {
  it("starts on OpenRouter with Continue off, enables it after a passing test, and resets on a provider switch (AC19, EC12)", async () => {
    mockTest(true);
    const onTested = vi.fn();
    const onContinue = vi.fn();
    const { user } = renderWithProviders(<ModelStep secrets={NONE} onTested={onTested} onContinue={onContinue} />);

    expect(screen.getByRole("radio", { name: "OpenRouter" })).toBeChecked();
    expect(screen.getByText("DevDigest uses your own key for every model call. Nothing runs through our servers.")).toBeInTheDocument();
    expect(screen.getByText("Stored locally on your machine — never uploaded.")).toBeInTheDocument();
    expect(cont()).toBeDisabled();
    expect(screen.getByRole("button", { name: "Back" })).toBeDisabled();

    await user.click(screen.getByRole("button", { name: "Test connection" }));
    await waitFor(() => expect(cont()).toBeEnabled());
    expect(onTested).toHaveBeenCalledWith("openrouter");

    await user.click(screen.getByRole("radio", { name: "OpenAI" }));
    expect(screen.getByRole("textbox", { name: "OpenAI API key" })).toBeInTheDocument();
    expect(cont()).toBeDisabled();

    await user.click(screen.getByRole("radio", { name: "OpenRouter" }));
    await user.click(screen.getByRole("button", { name: "Test connection" }));
    await waitFor(() => expect(cont()).toBeEnabled());
    await user.click(cont());
    expect(onContinue).toHaveBeenCalledTimes(1);
  });

  it("treats a stored key as 'Already connected' with Continue on, until a test of it fails", async () => {
    mockTest(false);
    const { user } = renderWithProviders(
      <ModelStep secrets={{ ...NONE, anthropic: true }} onTested={vi.fn()} onContinue={vi.fn()} />,
    );
    // The first provider with a key is preselected.
    expect(screen.getByRole("radio", { name: "Anthropic" })).toBeChecked();
    expect(screen.getByText("Already connected")).toBeInTheDocument();
    expect(cont()).toBeEnabled();

    await user.click(screen.getByRole("button", { name: "Test connection" }));
    expect(await screen.findByText("Invalid key")).toBeInTheDocument();
    expect(cont()).toBeDisabled();
    expect(screen.queryByText("Already connected")).toBeNull();
  });

  it("enables Continue with a custom provider and no key at all (FR21, D21)", async () => {
    const onContinue = vi.fn();
    const { user } = renderWithProviders(<ModelStep secrets={NONE} customCount={1} onTested={vi.fn()} onContinue={onContinue} />);
    expect(cont()).toBeEnabled();
    expect(screen.queryByText("Already connected")).toBeNull();
    await user.click(cont());
    expect(onContinue).toHaveBeenCalledTimes(1);
  });

  it("links to API Keys for a custom provider, keeping the built-in choice and its key field (AC26, FR21)", () => {
    renderWithProviders(<ModelStep secrets={NONE} onTested={vi.fn()} onContinue={vi.fn()} />);
    expect(screen.getByRole("link", { name: "Use a custom provider" })).toHaveAttribute("href", "/settings/api-keys");
    expect(screen.getByRole("radio", { name: "OpenRouter" })).toBeChecked();
    expect(screen.getByRole("textbox", { name: "OpenRouter API key" })).toBeInTheDocument();
  });

  it("renders the custom-provider link from the message file, with no hard-coded copy (AC22)", () => {
    const { container } = renderWithProviders(<ModelStep secrets={NONE} onTested={vi.fn()} onContinue={vi.fn()} />, {
      messages: pseudoMessages(),
    });
    expect(screen.getByRole("link", { name: "⟦setup.model.customLink⟧" })).toHaveAttribute("href", "/settings/api-keys");
    expect(strayText(container)).toEqual([]);
  });
});

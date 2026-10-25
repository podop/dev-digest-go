import { afterEach, describe, expect, it, vi } from "vitest";
import type { ConnTestProvider, SecretsStatus } from "@/lib/types";
import { renderWithProviders, screen, cleanup, waitFor } from "@/test/render";
import { mockFetch } from "@/test/fetch-mock";
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
    expect(screen.queryByRole("button", { name: "Back" })).toBeNull();

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
});

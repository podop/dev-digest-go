import { afterEach, describe, expect, it, vi } from "vitest";
import type { ConnTestResult, SecretsStatus } from "@/lib/types";
import { renderWithProviders, screen, cleanup, waitFor } from "@/test/render";
import { mockFetch } from "@/test/fetch-mock";
import { GithubStep } from "./GithubStep";

afterEach(cleanup);

const NONE: SecretsStatus = { openai: false, anthropic: false, openrouter: false, github: false };
const cont = () => screen.getByRole("button", { name: "Continue" });
const runTest = (user: ReturnType<typeof renderWithProviders>["user"]) =>
  user.click(screen.getByRole("button", { name: "Test connection" }));

function mockResult(over: Partial<ConnTestResult>) {
  mockFetch({ "POST /settings/test-connection": { provider: "github", ok: true, message: "OK", ...over } });
}

describe("GithubStep", () => {
  it("shows the login and scopes after a passing test, then enables Continue and Back (AC20)", async () => {
    mockResult({ login: "ann", scopes: ["repo", "read:org"] });
    const onBack = vi.fn();
    const onContinue = vi.fn();
    const onTested = vi.fn();
    const { user } = renderWithProviders(
      <GithubStep secrets={NONE} onTested={onTested} onBack={onBack} onContinue={onContinue} />,
    );
    expect(cont()).toBeDisabled();

    await runTest(user);
    expect(await screen.findByText("Connected as @ann")).toBeInTheDocument();
    expect(screen.getByText("Scopes: repo · read:org")).toBeInTheDocument();
    expect(screen.queryByText(/no repo scope/)).toBeNull();
    expect(cont()).toBeEnabled();
    expect(onTested).toHaveBeenCalledWith("github");

    await user.click(cont());
    await user.click(screen.getByRole("button", { name: "Back" }));
    expect(onContinue).toHaveBeenCalledTimes(1);
    expect(onBack).toHaveBeenCalledTimes(1);
  });

  it("warns about a classic token without repo but does not block Continue (FR24)", async () => {
    mockResult({ login: "ann", scopes: ["gist"] });
    const { user } = renderWithProviders(<GithubStep secrets={NONE} onTested={vi.fn()} onContinue={vi.fn()} />);
    await runTest(user);
    expect(await screen.findByText(/This token has no repo scope/)).toBeInTheDocument();
    expect(cont()).toBeEnabled();
  });

  it("notes a fine-grained token (scopes not reported) and an empty scope list", async () => {
    mockResult({ login: "ann", scopes: null });
    const { user, unmount } = renderWithProviders(<GithubStep secrets={NONE} onTested={vi.fn()} onContinue={vi.fn()} />);
    await runTest(user);
    expect(await screen.findByText("Fine-grained token — scopes are not reported by GitHub")).toBeInTheDocument();
    expect(screen.queryByText(/no repo scope/)).toBeNull();
    unmount();

    mockResult({ login: "bob", scopes: [] });
    const second = renderWithProviders(<GithubStep secrets={NONE} onTested={vi.fn()} onContinue={vi.fn()} />);
    await runTest(second.user);
    expect(await screen.findByText("Scopes: none")).toBeInTheDocument();
    expect(screen.getByText(/no repo scope/)).toBeInTheDocument();
  });

  it("accepts a stored token, and blocks again after a failed test", async () => {
    mockFetch({ "POST /settings/test-connection": { provider: "github", ok: false, message: "Bad credentials" } });
    const { user } = renderWithProviders(
      <GithubStep secrets={{ ...NONE, github: true }} onTested={vi.fn()} onContinue={vi.fn()} />,
    );
    expect(screen.getByText("Already connected")).toBeInTheDocument();
    expect(cont()).toBeEnabled();

    await runTest(user);
    expect(await screen.findByText("Bad credentials")).toBeInTheDocument();
    await waitFor(() => expect(cont()).toBeDisabled());
    expect(screen.queryByText("Connected as @ann")).toBeNull();
  });
});

/* TokenSection — the token field plus what its test reveals: scopes line, the missing-repo
   warning (A11), the fine-grained note and the comment identity. Rendered alone; KeyField
   and the secrets-status hook run for real against a stubbed fetch. */
import { describe, it, expect, afterEach } from "vitest";
import type { ConnTestResult } from "@/lib/types";
import { renderWithProviders, screen, cleanup, waitFor, pseudoMessages } from "@/test/render";
import { mockFetch } from "@/test/fetch-mock";
import { strayText } from "@/test/pseudo";
import { TokenSection } from "./TokenSection";

afterEach(cleanup);

const TEST = "/settings/test-connection";
const STATUS = { openrouter: false, openai: false, anthropic: false, github: true };
const WARNING = /no repo scope/;

function result(over: Partial<ConnTestResult> = {}): ConnTestResult {
  return { provider: "github", ok: true, message: "Connected as @ann", login: "ann", scopes: ["repo"], ...over };
}

describe("TokenSection", () => {
  it.each([
    { name: "a classic token with repo", scopes: ["repo", "gist"], line: "Scopes: repo · gist", warns: false },
    { name: "a classic token without repo", scopes: ["gist"], line: "Scopes: gist", warns: true },
    { name: "a classic token with no scopes at all", scopes: [], line: "No scopes granted", warns: true },
    {
      name: "a fine-grained token",
      scopes: null,
      line: "Fine-grained token — scopes are not reported by GitHub",
      warns: false,
    },
  ])("after the automatic test of $name it shows the scopes line and the warning only when due", async ({ scopes, line, warns }) => {
    mockFetch({ "GET /settings/secrets-status": STATUS, [`POST ${TEST}`]: result({ scopes }) });
    renderWithProviders(<TokenSection />);

    expect(await screen.findByText(line)).toBeInTheDocument();
    if (warns) expect(screen.getByText(WARNING)).toBeInTheDocument();
    else expect(screen.queryByText(WARNING)).not.toBeInTheDocument();
    expect(screen.getByText("Comments are posted as @ann.")).toBeInTheDocument();
  });

  it("falls back to the generic identity, with no extras, when no token is stored or the test fails", async () => {
    const api = mockFetch({ "GET /settings/secrets-status": { ...STATUS, github: false } });
    renderWithProviders(<TokenSection />);
    expect(await screen.findByText("Comments are posted as the token's owner.")).toBeInTheDocument();
    await waitFor(() => expect(api.requests("GET", "/settings/secrets-status")).toHaveLength(1));
    expect(api.requests("POST", TEST)).toHaveLength(0);
    expect(screen.queryByText(/^Scopes:/)).not.toBeInTheDocument();

    cleanup();
    mockFetch({
      "GET /settings/secrets-status": STATUS,
      [`POST ${TEST}`]: { provider: "github", ok: false, message: "Bad credentials" },
    });
    renderWithProviders(<TokenSection />);
    expect(await screen.findByText("Bad credentials")).toBeInTheDocument();
    expect(screen.queryByText(/^Scopes:/)).not.toBeInTheDocument();
    expect(screen.queryByText(WARNING)).not.toBeInTheDocument();
    expect(screen.getByText("Comments are posted as the token's owner.")).toBeInTheDocument();
  });
});

describe("TokenSection messages (AC22)", () => {
  it("takes every visible string from the message files (pseudo bundle)", async () => {
    mockFetch({ "GET /settings/secrets-status": STATUS, [`POST ${TEST}`]: result({ scopes: ["gist"] }) });
    const { container } = renderWithProviders(<TokenSection />, { messages: pseudoMessages() });

    expect(await screen.findByText("⟦githubIntegration.scopes.missingRepo⟧")).toBeInTheDocument();
    expect(screen.getByText("⟦githubIntegration.scopes.list⟧")).toBeInTheDocument();
    expect(screen.getByText("⟦githubIntegration.identity.login⟧")).toBeInTheDocument();
    expect(screen.getByText("⟦githubIntegration.token.label⟧")).toBeInTheDocument();
    // The banner shows the server's message (data); the docs link text and masks come from other namespaces' markers.
    expect(strayText(container, ["Connected as @ann"])).toEqual([]);
  });
});

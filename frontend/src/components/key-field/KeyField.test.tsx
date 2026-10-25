import { afterEach, describe, expect, it, vi } from "vitest";
import type { ConnTestResult } from "@/lib/types";
import { renderWithProviders, screen, cleanup, pseudoMessages } from "@/test/render";
import { strayText } from "@/test/pseudo";
import { mockFetch, jsonResponse } from "@/test/fetch-mock";
import { KeyField, KEY_PAGE_URLS, type KeyFieldProps } from "./index";

afterEach(cleanup);

const KEY = "sk-or-v1-abcdefgh1234";
const ROUTE = "POST /settings/test-connection";

function field(props: Partial<KeyFieldProps> = {}) {
  return (
    <KeyField
      provider="openrouter"
      label="OpenRouter API key"
      hint="One key, many models."
      docsHref={KEY_PAGE_URLS.openrouter}
      configured={false}
      {...props}
    />
  );
}

const input = () => screen.getByRole("textbox", { name: "OpenRouter API key" });
/** The banner element that shows `text` (the toast region is a `status` too, so role alone is ambiguous). */
async function banner(text: string | RegExp) {
  return screen.findByText(text);
}
const testButton = () => screen.getByRole("button", { name: /Test connection|Testing…/ });

function result(over: Partial<ConnTestResult> = {}): ConnTestResult {
  return { provider: "openrouter", ok: true, message: "OK", ...over };
}

describe("KeyField masking and reveal", () => {
  it("masks a typed key as 17 dots plus its last 4, edits it in place, and reveals it", { timeout: 20_000 }, async () => {
    const { user } = renderWithProviders(field());
    expect(input()).toHaveAttribute("autocomplete", "off");
    expect(input()).toHaveAttribute("placeholder", "Paste your key");

    await user.type(input(), KEY);
    expect(input()).toHaveValue(`${"•".repeat(17)}1234`);

    const toggle = screen.getByRole("button", { name: "Show key" });
    expect(toggle).toHaveAttribute("aria-pressed", "false");
    await user.click(toggle);
    expect(toggle).toHaveAttribute("aria-pressed", "true");
    expect(input()).toHaveValue(KEY);

    await user.type(input(), "5{Backspace}{Backspace}");
    expect(input()).toHaveValue(KEY.slice(0, -1));
    await user.click(toggle);
    expect(input()).toHaveValue(`${"•".repeat(16)}h123`);

    // Select everything and paste a new key over the mask.
    await user.clear(input());
    await user.click(input());
    await user.paste("sk-new-key-0000");
    await user.click(toggle);
    expect(input()).toHaveValue("sk-new-key-0000");

    // NFR7: nothing is written to the URL or to web storage.
    expect(window.location.href).not.toContain("key");
    expect(window.localStorage.length).toBe(0);
    expect(window.sessionStorage.length).toBe(0);
  });

  it("links to the provider's key page in a new tab, and github to the token page", () => {
    const { rerender } = renderWithProviders(field());
    const link = screen.getByRole("link", { name: /Where to get your key/ });
    expect(link).toHaveAttribute("href", "https://openrouter.ai/keys");
    expect(link).toHaveAttribute("target", "_blank");
    expect(link).toHaveAttribute("rel", expect.stringContaining("noopener"));

    rerender(field({ provider: "github", label: "Personal access token", docsHref: KEY_PAGE_URLS.github }));
    expect(screen.getByRole("link", { name: /Create a token/ })).toHaveAttribute("href", "https://github.com/settings/tokens");
  });
});

describe("KeyField placeholder and badge", () => {
  it("shows the 'Key saved' placeholder for a configured row and the badge for each stored state", () => {
    const { rerender } = renderWithProviders(field({ configured: true }));
    expect(input()).toHaveValue("");
    expect(input()).toHaveAttribute("placeholder", "Key saved — paste a new key to replace it");
    expect(screen.getByText("Connected")).toBeInTheDocument();

    rerender(field({ configured: false, optional: true }));
    expect(input()).toHaveAttribute("placeholder", "Paste your key");
    expect(screen.getByText("Not connected")).toBeInTheDocument();
    expect(screen.getByText("Optional")).toBeInTheDocument();

    rerender(field({ configured: undefined }));
    expect(screen.queryByText("Connected")).toBeNull();
    expect(screen.queryByText("Not connected")).toBeNull();
  });
});

describe("KeyField test connection", () => {
  it("sends the typed key once, even on a double click, and shows the verified banner", async () => {
    let release: () => void = () => {};
    const gate = new Promise<void>((r) => (release = r));
    const api = mockFetch({
      [ROUTE]: async () => {
        await gate;
        return result({ model: "anthropic/claude-sonnet-4", model_reachable: true, latency_ms: 142 });
      },
      "GET /settings/secrets-status": { openrouter: true, openai: false, anthropic: false, github: false },
    });
    const { user } = renderWithProviders(field());

    await user.type(input(), `  ${KEY}  `);
    await user.dblClick(testButton());
    expect(testButton()).toBeDisabled();
    expect(testButton()).toHaveTextContent("Testing…");
    release();

    const verified = await banner("Connection verified · anthropic/claude-sonnet-4 reachable · 142ms");
    expect(verified).toHaveAttribute("role", "status");
    expect(testButton()).toBeEnabled();
    expect(screen.getByText("Connected")).toBeInTheDocument();
    const sent = api.requests("POST", "/settings/test-connection");
    expect(sent).toHaveLength(1);
    expect(sent[0]?.body).toEqual({ provider: "openrouter", key: KEY });
  });

  it("warns when the model is not listed, and tests the stored key when the field is empty", async () => {
    const api = mockFetch({
      [ROUTE]: result({ model: "a/y", model_reachable: false, latency_ms: 90 }),
      "GET /settings/secrets-status": { openrouter: true, openai: false, anthropic: false, github: false },
    });
    const { user } = renderWithProviders(field({ configured: true }));

    await user.click(testButton());
    expect(await banner("Connected · a/y is not listed by OpenRouter")).toHaveAttribute("role", "status");
    expect(api.requests("POST", "/settings/test-connection")[0]?.body).toEqual({ provider: "openrouter" });
    expect(screen.getByText("Connected")).toBeInTheDocument();
  });

  it("shows the error banner and 'Failed' until the next successful test", async () => {
    let ok = false;
    mockFetch({
      [ROUTE]: () => (ok ? result({ message: "Connected as @ann", provider: "github" }) : result({ ok: false, message: "Invalid key" })),
      "GET /settings/secrets-status": { openrouter: true, openai: false, anthropic: false, github: false },
    });
    const { user } = renderWithProviders(field({ configured: true }));

    await user.click(testButton());
    expect(await banner("Invalid key")).toHaveAttribute("role", "alert");
    expect(screen.getByText("Failed")).toBeInTheDocument();
    expect(screen.queryByText("Connected")).toBeNull();

    ok = true;
    await user.click(testButton());
    expect(await banner("Connected as @ann")).toHaveAttribute("role", "status");
    expect(screen.queryByRole("alert")).toBeNull();
    expect(screen.getByText("Connected")).toBeInTheDocument();
  });

  it("turns an API error into the failure banner", async () => {
    mockFetch({ [ROUTE]: jsonResponse({ error: { code: "rate_limited", message: "Too many tests" } }, 429) });
    const { user } = renderWithProviders(field({ configured: false }));

    await user.click(testButton());
    expect(await banner("Too many tests")).toHaveAttribute("role", "alert");
    expect(screen.getByText("Failed")).toBeInTheDocument();
  });

  it("autoTest runs one keyless test for a stored key and reports the result", async () => {
    const onResult = vi.fn();
    const api = mockFetch({
      [ROUTE]: result({ provider: "github", message: "Connected as @ann", login: "ann", scopes: ["repo"] }),
      "GET /settings/secrets-status": { openrouter: false, openai: false, anthropic: false, github: true },
    });
    renderWithProviders(field({ provider: "github", configured: true, autoTest: true, onResult }));

    expect(await banner("Connected as @ann")).toHaveAttribute("role", "status");
    expect(api.requests("POST", "/settings/test-connection")).toHaveLength(1);
    expect(api.requests("POST", "/settings/test-connection")[0]?.body).toEqual({ provider: "github" });
    expect(onResult).toHaveBeenCalledWith(expect.objectContaining({ login: "ann", scopes: ["repo"] }));
  });

  it("does not autoTest without a stored key", () => {
    const api = mockFetch({});
    renderWithProviders(field({ configured: false, autoTest: true }));
    expect(api.requests("POST", "/settings/test-connection")).toHaveLength(0);
  });
});

describe("KeyField messages (AC22)", () => {
  it("takes every visible string from the message files (pseudo bundle)", async () => {
    mockFetch({
      [ROUTE]: result({ model: "a/x", model_reachable: true, latency_ms: 5 }),
      "GET /settings/secrets-status": { openrouter: true, openai: false, anthropic: false, github: false },
    });
    const { user, container } = renderWithProviders(field({ configured: true, optional: true }), {
      messages: pseudoMessages(),
    });
    const allowed = ["OpenRouter API key", "One key, many models."];

    expect(strayText(container, allowed)).toEqual([]);
    expect(input()).toHaveAttribute("placeholder", "⟦keyField.placeholderSaved⟧");
    expect(screen.getByRole("button", { name: "⟦keyField.reveal⟧" })).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "⟦keyField.test⟧" }));
    expect(await banner("⟦keyField.result.verified⟧")).toHaveAttribute("role", "status");
    expect(strayText(container, allowed)).toEqual([]);
  });
});

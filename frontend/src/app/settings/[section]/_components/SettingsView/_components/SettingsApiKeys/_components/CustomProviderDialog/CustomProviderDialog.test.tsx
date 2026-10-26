/* CustomProviderDialog — add / edit flow (AC22), models and prices (AC23), structured output (AC43),
   focus (NFR9) and hard-coded copy (AC29). Real hooks over a stubbed fetch. */
import React from "react";
import { describe, it, expect, afterEach, vi } from "vitest";
import type { ConnTestResult, CustomProvider } from "@/lib/types";
import { renderWithProviders, screen, cleanup, waitFor, within, pseudoMessages } from "@/test/render";
import { mockFetch, jsonResponse } from "@/test/fetch-mock";
import { strayText } from "@/test/pseudo";
import { CustomProviderDialog } from "./CustomProviderDialog";

afterEach(cleanup);

const BASE = "/settings/custom-providers";
const STORED: CustomProvider = {
  id: "custom:synapse",
  name: "Synapse",
  protocol: "openai",
  base_url: "https://gw.example.com/v1",
  plain_http: false,
  auth_mode: "bearer",
  auth_header: null,
  structured_output: "strict",
  key_configured: true,
  extra_headers: [{ name: "X-Tenant", value_configured: true }],
  models: [{ id: "syn-large", label: "Large", pricing: { promptPerM: 1, completionPerM: 4 } }],
  created_at: "2026-10-26T00:00:00Z",
  updated_at: "2026-10-26T00:00:00Z",
};

const field = (name: string | RegExp) => screen.getByRole("textbox", { name });
const save = () => screen.getByRole("button", { name: "Save" });

/** A 422 in the server's shape (httpx.NewValidationError). */
function validation(path: (string | number)[], message: string) {
  const instancePath = `/${path.join("/")}`;
  const details = [{ keyword: "custom", instancePath, schemaPath: `#${instancePath}/custom`, message, params: { issue: { code: "custom", path, message } } }];
  return jsonResponse({ error: { code: "validation_error", message: "Request validation failed", details } }, 422);
}

describe("CustomProviderDialog", () => {
  it("suggests the slug from the name, keeps it editable, sends POST and closes (AC22)", async () => {
    const api = mockFetch({ [`POST ${BASE}`]: STORED });
    const onClose = vi.fn();
    const { user } = renderWithProviders(<CustomProviderDialog onClose={onClose} />);

    expect(save()).toBeDisabled();
    await user.type(field("Name"), "Synapse Prod");
    expect(field("ID")).toHaveValue("synapse-prod");
    expect(field("ID")).not.toHaveAttribute("readonly");
    expect(screen.getByText("Used as custom:synapse-prod.", { exact: false })).toBeInTheDocument();
    await user.type(field("Base URL"), " https://gw.example.com/v1");
    expect(screen.getByText("API root including the version, e.g. https://gateway.example.com/v1")).toBeInTheDocument();
    await user.click(save());

    await waitFor(() => expect(onClose).toHaveBeenCalledTimes(1));
    expect(api.requests("POST", BASE)).toHaveLength(1);
    expect(api.requests("POST", BASE)[0]!.body).toEqual({
      id: "custom:synapse-prod",
      name: "Synapse Prod",
      protocol: "openai",
      base_url: "https://gw.example.com/v1",
      auth_mode: "bearer",
      auth_header: null,
      structured_output: "strict",
      extra_headers: [],
      models: [],
    });
  });

  it("shows a 422 at its field, keeps the dialog open, and a duplicate id at the ID field (AC22)", async () => {
    let answer: Response = validation(["base_url"], "Base URL resolves to a forbidden address");
    mockFetch({ [`POST ${BASE}`]: () => answer });
    const onClose = vi.fn();
    const { user } = renderWithProviders(<CustomProviderDialog onClose={onClose} />);

    await user.type(field("Name"), "Lab");
    await user.type(field("Base URL"), "http://169.254.0.1");
    await user.click(save());

    const msg = await screen.findByText("Base URL resolves to a forbidden address");
    expect(field("Base URL")).toHaveAttribute("aria-invalid", "true");
    expect(field("Base URL").getAttribute("aria-describedby")).toContain(msg.id);
    expect(onClose).not.toHaveBeenCalled();

    // typing in the field drops its message
    await user.type(field("Base URL"), "x");
    expect(screen.queryByText("Base URL resolves to a forbidden address")).toBeNull();

    answer = jsonResponse({ error: { code: "custom_provider_exists", message: "Custom provider custom:lab already exists" } }, 409);
    await user.click(save());
    const dup = await screen.findByText("Custom provider custom:lab already exists");
    expect(field("ID").getAttribute("aria-describedby")).toContain(dup.id);
  });

  it("edit: id is read-only, stored secrets show placeholders and Save sends no api_key and no header value (AC22)", async () => {
    const api = mockFetch({ [`PUT ${BASE}/:id`]: STORED });
    const onClose = vi.fn();
    const { user } = renderWithProviders(<CustomProviderDialog provider={STORED} onClose={onClose} />);

    expect(screen.getByText("Edit Synapse")).toBeInTheDocument();
    expect(field("ID")).toHaveAttribute("readonly");
    expect(field("ID")).toHaveValue("synapse");
    expect(field("API key")).toHaveAttribute("placeholder", "Key saved — paste a new key to replace it");
    expect(screen.getByLabelText("Header 1 value")).toHaveAttribute("placeholder", "Value saved");
    expect(field("Header 1 name")).toHaveValue("X-Tenant");

    await user.click(save());
    await waitFor(() => expect(onClose).toHaveBeenCalledTimes(1));
    const put = api.requests("PUT")[0]!;
    expect(put.path).toBe(`${BASE}/custom%3Asynapse`);
    expect(put.body).toEqual({
      name: "Synapse",
      protocol: "openai",
      base_url: "https://gw.example.com/v1",
      auth_mode: "bearer",
      auth_header: null,
      structured_output: "strict",
      extra_headers: [{ name: "X-Tenant" }],
      models: [{ id: "syn-large", label: "Large", pricing: { promptPerM: 1, completionPerM: 4 } }],
    });
    expect(put.body).not.toHaveProperty("api_key");
  });

  it("masks a typed key, sends it, and 'Remove key' sends api_key null (FR3, FR5)", async () => {
    const api = mockFetch({ [`PUT ${BASE}/:id`]: STORED });
    const { user } = renderWithProviders(<CustomProviderDialog provider={STORED} onClose={() => {}} />);

    await user.type(field("API key"), "sk-secret-1234");
    expect(field("API key")).toHaveValue(`${"•".repeat(10)}1234`);
    await user.click(save());
    await waitFor(() => expect(api.requests("PUT")).toHaveLength(1));
    expect(api.requests("PUT")[0]!.body).toMatchObject({ api_key: "sk-secret-1234" });

    cleanup();
    const second = renderWithProviders(<CustomProviderDialog provider={STORED} onClose={() => {}} />);
    await second.user.click(screen.getByRole("checkbox", { name: "Remove key" }));
    expect(screen.getByText("The saved key is removed when you save.")).toHaveAttribute("role", "status");
    await second.user.click(save());
    await waitFor(() => expect(api.requests("PUT")).toHaveLength(2));
    expect(api.requests("PUT")[1]!.body).toMatchObject({ api_key: null });
  });

  it("Save and test sends the PUT, then the test, and shows the result without closing (AC22, FR23)", async () => {
    const result: ConnTestResult = { provider: "custom:synapse", ok: true, message: "OK — 3 models available", warning: "Model list not available; key not verified" };
    const api = mockFetch({ [`PUT ${BASE}/:id`]: STORED, "POST /settings/test-connection": result });
    const onClose = vi.fn();
    const { user } = renderWithProviders(<CustomProviderDialog provider={STORED} onClose={onClose} />);

    await user.click(screen.getByRole("button", { name: "Save and test" }));

    const banner = await screen.findByText("Model list not available; key not verified");
    expect(banner).toHaveAttribute("role", "status");
    const order = api.fetch.mock.calls.map(([url, init]) => `${(init as RequestInit | undefined)?.method ?? "GET"} ${new URL(String(url)).pathname}`);
    expect(order).toEqual([`PUT ${BASE}/custom%3Asynapse`, "POST /settings/test-connection"]);
    expect(api.requests("POST", "/settings/test-connection")[0]!.body).toEqual({ provider: "custom:synapse" });
    expect(onClose).not.toHaveBeenCalled();
  });

  it("Save and test on a new provider then saves with PUT, not a second POST", async () => {
    const created = { ...STORED, id: "custom:lab", name: "Lab", extra_headers: [], models: [] };
    const api = mockFetch({
      [`POST ${BASE}`]: created,
      [`PUT ${BASE}/:id`]: created,
      "POST /settings/test-connection": { provider: "custom:lab", ok: false, message: "Unauthorized" } satisfies ConnTestResult,
    });
    const { user } = renderWithProviders(<CustomProviderDialog onClose={() => {}} />);

    await user.type(field("Name"), "Lab");
    await user.type(field("Base URL"), "https://gw.example.com/v1");
    await user.click(screen.getByRole("button", { name: "Save and test" }));
    expect(await screen.findByText("Unauthorized")).toHaveAttribute("role", "alert");
    expect(await screen.findByText("Edit Lab")).toBeInTheDocument();
    expect(field("ID")).toHaveAttribute("readonly");

    await user.click(save());
    await waitFor(() => expect(api.requests("PUT")).toHaveLength(1));
    expect(api.requests("POST", BASE)).toHaveLength(1);
  });

  it("adds a model with prices, removes another, and blocks Save on a bad price (AC23)", async () => {
    const api = mockFetch({ [`POST ${BASE}`]: STORED });
    const { user } = renderWithProviders(<CustomProviderDialog onClose={() => {}} />);
    await user.type(field("Name"), "Lab");
    await user.type(field("Base URL"), "https://gw.example.com/v1");

    expect(screen.getByText("No manual models")).toBeInTheDocument();
    expect(screen.getByText("Add models the gateway does not list, or prices so the spend cap counts them.")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Add model" }));
    await user.click(screen.getByRole("button", { name: "Add model" }));
    await user.type(field("Model 1 ID"), "m-a");
    await user.type(field("Model 1 input price per 1M tokens"), "abc");
    await user.type(field("Model 1 output price per 1M tokens"), "8");
    expect(screen.getByText("Enter a price from 0 to 10,000 with at most 6 decimals.")).toBeInTheDocument();
    expect(save()).toBeDisabled();

    await user.clear(field("Model 1 input price per 1M tokens"));
    await user.type(field("Model 1 input price per 1M tokens"), "1.1234567");
    expect(screen.getByText("Enter a price from 0 to 10,000 with at most 6 decimals.")).toBeInTheDocument();
    expect(save()).toBeDisabled();

    await user.clear(field("Model 1 input price per 1M tokens"));
    await user.type(field("Model 1 input price per 1M tokens"), "2");
    expect(screen.queryByText("Enter a price from 0 to 10,000 with at most 6 decimals.")).toBeNull();
    await user.type(field("Model 2 ID"), "m-b");
    await user.click(screen.getByRole("button", { name: "Remove model 2" }));
    expect(screen.queryByRole("textbox", { name: "Model 2 ID" })).toBeNull();

    await user.click(save());
    await waitFor(() => expect(api.requests("POST", BASE)).toHaveLength(1));
    expect(api.requests("POST", BASE)[0]!.body).toMatchObject({
      models: [{ id: "m-a", label: null, pricing: { promptPerM: 2, completionPerM: 8 } }],
    });
  });

  it("offers JSON object only for OpenAI-compatible and resets it with a note on the protocol switch (AC43)", async () => {
    const api = mockFetch({ [`POST ${BASE}`]: STORED });
    const { user } = renderWithProviders(<CustomProviderDialog onClose={() => {}} />);
    await user.type(field("Name"), "Lab");
    await user.type(field("Base URL"), "https://gw.example.com/v1");

    const group = () => screen.getByRole("group", { name: "Structured output" });
    expect(within(group()).getAllByRole("radio").map((r) => (r as HTMLInputElement).labels?.[0]?.textContent)).toEqual(["Strict", "JSON object", "Prompt only"]);
    expect(within(group()).getByRole("radio", { name: "Strict" })).toBeChecked();
    expect(group()).toHaveAccessibleDescription(/How DevDigest asks the gateway for JSON\. Strict: the gateway enforces the schema \(recommended\)/);

    await user.click(within(group()).getByRole("radio", { name: "JSON object" }));
    await user.click(screen.getByRole("radio", { name: "Anthropic-compatible" }));
    expect(within(group()).getByRole("radio", { name: "Strict" })).toBeChecked();
    expect(within(group()).queryByRole("radio", { name: "JSON object" })).toBeNull();
    expect(screen.getByText("JSON object is available only for OpenAI-compatible gateways; Structured output was set to Strict.")).toHaveAttribute("role", "status");
    // auth follows the protocol default: header mode with x-api-key
    expect(screen.getByRole("radio", { name: "Custom header" })).toBeChecked();
    expect(field("Header name")).toHaveValue("x-api-key");

    await user.click(within(group()).getByRole("radio", { name: "Prompt only" }));
    expect(screen.queryByText(/JSON object is available only/)).toBeNull();
    await user.click(save());
    await waitFor(() => expect(api.requests("POST", BASE)).toHaveLength(1));
    expect(api.requests("POST", BASE)[0]!.body).toMatchObject({
      protocol: "anthropic",
      auth_mode: "header",
      auth_header: "x-api-key",
      structured_output: "prompt_only",
    });
  });

  it("limits extra headers to five and sends typed values", async () => {
    const api = mockFetch({ [`POST ${BASE}`]: STORED });
    const { user } = renderWithProviders(<CustomProviderDialog onClose={() => {}} />);
    await user.type(field("Name"), "Lab");
    await user.type(field("Base URL"), "https://gw.example.com/v1");

    for (let i = 0; i < 5; i++) await user.click(screen.getByRole("button", { name: "Add header" }));
    expect(screen.getByRole("button", { name: "Add header" })).toBeDisabled();
    expect(save()).toBeDisabled(); // empty header names
    await user.type(field("Header 1 name"), "X-Tenant");
    await user.type(screen.getByLabelText("Header 1 value"), "acme");
    for (let i = 5; i >= 2; i--) await user.click(screen.getByRole("button", { name: `Remove header ${i}` }));
    await user.click(save());
    await waitFor(() => expect(api.requests("POST", BASE)).toHaveLength(1));
    expect(api.requests("POST", BASE)[0]!.body).toMatchObject({ extra_headers: [{ name: "X-Tenant", value: "acme" }] });
  });

  it("Escape closes the dialog and focus returns to the opener (NFR9)", async () => {
    mockFetch({});
    function Host() {
      const [open, setOpen] = React.useState(false);
      return (
        <>
          <button type="button" onClick={() => setOpen(true)}>
            Open
          </button>
          {open && <CustomProviderDialog onClose={() => setOpen(false)} />}
        </>
      );
    }
    const { user } = renderWithProviders(<Host />);
    const opener = screen.getByRole("button", { name: "Open" });
    await user.click(opener);
    const dialog = screen.getByRole("dialog");
    expect(dialog.contains(document.activeElement)).toBe(true);
    for (let i = 0; i < 12; i++) {
      await user.tab();
      expect(dialog.contains(document.activeElement)).toBe(true);
    }

    await user.keyboard("{Escape}");
    expect(screen.queryByRole("dialog")).toBeNull();
    expect(opener).toHaveFocus();
  });

  it("takes every visible string from the message files (pseudo bundle, AC29)", async () => {
    mockFetch({});
    const { container, user } = renderWithProviders(<CustomProviderDialog provider={STORED} onClose={() => {}} />, { messages: pseudoMessages() });
    await user.click(screen.getByRole("button", { name: "⟦customProviders.models.add⟧" }));
    await user.click(screen.getByRole("button", { name: "⟦customProviders.dialog.addHeader⟧" }));
    await user.click(screen.getByRole("checkbox", { name: "⟦customProviders.dialog.removeKey⟧" }));

    // Left over: data only (the provider's name in the title is wrapped by a message marker).
    expect(strayText(container)).toEqual([]);
  });
});

/* ProviderSelect — one picker order everywhere (SCR6, AC24), the "Not configured" entry of an undefined custom
   id (EC23), the failed-list error with Retry and the message-file copy (NFR9); real hooks over a stubbed API. */
import React from "react";
import { describe, it, expect, afterEach, vi } from "vitest";
import { renderWithProviders, screen, cleanup, waitFor, pseudoMessages } from "@/test/render";
import { mockFetch, jsonResponse } from "@/test/fetch-mock";
import { strayText } from "@/test/pseudo";
import type { Provider } from "@/lib/types";
import { ProviderSelect } from "./ProviderSelect";
import { useProviderUndefined } from "./useProviderUndefined";

afterEach(cleanup);

const CUSTOM = [
  { id: "custom:synapse", name: "Synapse" },
  { id: "custom:lab", name: "Lab" },
];

function Harness({ initial = "openrouter", onChange = () => {} }: { initial?: Provider; onChange?: (p: Provider) => void }) {
  const [value, setValue] = React.useState<Provider>(initial);
  const blocked = useProviderUndefined(value);
  return (
    <>
      <ProviderSelect
        value={value}
        onChange={(p) => {
          setValue(p);
          onChange(p);
        }}
        aria-label="Provider"
      />
      <span>{blocked ? "blocked" : "open"}</span>
    </>
  );
}

describe("ProviderSelect", () => {
  it("lists the built-ins, then the custom providers by name, and reports the pick (AC24)", async () => {
    mockFetch({ "GET /settings/custom-providers": CUSTOM });
    const onChange = vi.fn();
    const { user } = renderWithProviders(<Harness onChange={onChange} />);

    const box = screen.getByRole("combobox", { name: "Provider" });
    await user.click(box);
    await waitFor(() => expect(screen.getAllByRole("option")).toHaveLength(5));
    expect(screen.getAllByRole("option").map((o) => o.textContent)).toEqual([
      "OpenRouter",
      "OpenAI",
      "Anthropic",
      "Lab",
      "Synapse",
    ]);
    await user.click(screen.getByRole("option", { name: "Synapse" }));
    expect(onChange).toHaveBeenCalledWith("custom:synapse");
    expect(box).toHaveTextContent("Synapse");
    expect(screen.getByText("open")).toBeInTheDocument();
  });

  it("offers only the built-ins while the custom list loads", async () => {
    mockFetch({ "GET /settings/custom-providers": () => new Promise<Response>(() => {}) });
    const { user } = renderWithProviders(<Harness initial={"custom:synapse"} />);

    await user.click(screen.getByRole("combobox", { name: "Provider" }));
    // the stored id stays visible as the raw id, never as "Not configured" before the list is known
    expect(screen.getAllByRole("option").map((o) => o.textContent)).toEqual([
      "OpenRouter",
      "OpenAI",
      "Anthropic",
      "custom:synapse",
    ]);
    expect(screen.queryByText(/Not configured/)).not.toBeInTheDocument();
    expect(screen.getByText("open")).toBeInTheDocument();
  });

  it("shows an undefined custom id as 'custom:ghost · Not configured' and reports it blocked (EC23)", async () => {
    mockFetch({ "GET /settings/custom-providers": CUSTOM });
    const { user } = renderWithProviders(<Harness initial={"custom:ghost"} />);

    expect(await screen.findByText("blocked")).toBeInTheDocument();
    const box = screen.getByRole("combobox", { name: "Provider" });
    expect(box).toHaveTextContent("custom:ghost · Not configured");

    await user.click(box);
    await user.click(screen.getByRole("option", { name: "Lab" }));
    expect(box).toHaveTextContent("Lab");
    expect(await screen.findByText("open")).toBeInTheDocument();
  });

  it("keeps the built-ins and shows an error with Retry when the custom list fails", async () => {
    let fail = true;
    const api = mockFetch({
      "GET /settings/custom-providers": () =>
        fail ? jsonResponse({ error: { code: "internal", message: "boom" } }, 500) : CUSTOM,
    });
    const { user } = renderWithProviders(<Harness />);

    expect(await screen.findByText("Custom providers could not be loaded.")).toBeInTheDocument();
    await user.click(screen.getByRole("combobox", { name: "Provider" }));
    expect(screen.getAllByRole("option")).toHaveLength(3);
    await user.click(screen.getByRole("option", { name: "OpenAI" }));

    fail = false;
    await user.click(screen.getByRole("button", { name: "Retry" }));
    await waitFor(() => expect(screen.queryByText("Custom providers could not be loaded.")).not.toBeInTheDocument());
    expect(api.requests("GET", "/settings/custom-providers").length).toBeGreaterThanOrEqual(2);
  });

  it("takes every visible string from the message files (pseudo bundle, NFR9)", async () => {
    mockFetch({ "GET /settings/custom-providers": () => jsonResponse({ error: { code: "x", message: "y" } }, 500) });
    const { container } = renderWithProviders(<Harness initial={"custom:ghost"} />, { messages: pseudoMessages() });

    await screen.findByText("⟦providerSelect.loadFailed⟧");
    expect(strayText(container, ["blocked", "open", "custom:ghost"])).toEqual([]);
  });
});

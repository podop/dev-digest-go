import { describe, it, expect, afterEach } from "vitest";
import { renderWithProviders, screen, cleanup, waitFor, pseudoMessages } from "@/test/render";
import { mockFetch } from "@/test/fetch-mock";
import { strayText } from "@/test/pseudo";
import { customProviderKeys } from "@/lib/hooks/keys";
import { ProviderName } from "./index";

afterEach(cleanup);

const provider = (id: string, name: string) => ({
  id,
  name,
  protocol: "openai",
  base_url: "https://gw.example.com/v1",
  plain_http: false,
  auth_mode: "bearer",
  auth_header: null,
  structured_output: "strict",
  key_configured: true,
  extra_headers: [],
  models: [],
  created_at: "2026-10-26T10:00:00.000Z",
  updated_at: "2026-10-26T10:00:00.000Z",
});

describe("ProviderName", () => {
  it("shows a built-in's name and a custom provider's current name, and follows a rename (FR20, EC22, AC25)", async () => {
    let name = "Synapse";
    mockFetch({ "GET /settings/custom-providers": () => [provider("custom:synapse", name)] });
    const { queryClient } = renderWithProviders(
      <p>
        <ProviderName id="openai" /> <ProviderName id="custom:synapse" />
      </p>,
    );
    expect(screen.getByText("OpenAI")).toBeInTheDocument();
    expect(await screen.findByText("Synapse")).toBeInTheDocument();

    name = "Synapse EU";
    await queryClient.invalidateQueries({ queryKey: customProviderKeys.list() });
    expect(await screen.findByText("Synapse EU")).toBeInTheDocument();
    expect(screen.queryByText("Synapse")).toBeNull();
  });

  it("falls back to the raw id for an undefined custom id, and when the lookup fails (EC22)", async () => {
    mockFetch({ "GET /settings/custom-providers": [provider("custom:synapse", "Synapse")] });
    const { unmount } = renderWithProviders(<ProviderName id="custom:ghost" />);
    await waitFor(() => expect(screen.getByText("custom:ghost")).toBeInTheDocument());
    unmount();

    mockFetch({ "GET /settings/custom-providers": new Response("boom", { status: 500 }) });
    renderWithProviders(<ProviderName id="custom:synapse" />);
    expect(await screen.findByText("custom:synapse")).toBeInTheDocument();
  });

  it("carries the full name as a native title for a long (clipped) name", async () => {
    const name = "A very long gateway display name ".repeat(3).trim();
    mockFetch({ "GET /settings/custom-providers": [provider("custom:long", name)] });
    renderWithProviders(<ProviderName id="custom:long" />);
    expect(await screen.findByTitle(name)).toHaveTextContent(name);
  });

  it("adds no hard-coded copy (pseudo bundle)", async () => {
    mockFetch({ "GET /settings/custom-providers": [provider("custom:synapse", "Synapse")] });
    const { container } = renderWithProviders(<ProviderName id="custom:synapse" />, { messages: pseudoMessages() });
    expect(await screen.findByText("Synapse")).toBeInTheDocument();
    // The only text is the provider's own name (data, not copy).
    expect(strayText(container, ["Synapse"])).toEqual([]);
  });
});

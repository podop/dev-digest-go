/* AgentEditorView — the header badge names the agent's provider by its current display name
   (custom model providers, FR20/EC22/AC25). AppShell is a passthrough; hooks run over a stubbed API. */
import type React from "react";
import { describe, it, expect, afterEach, vi } from "vitest";
import { renderWithProviders, screen, cleanup, within, waitFor } from "@/test/render";
import { mockFetch } from "@/test/fetch-mock";
import type { Agent } from "@devdigest/shared";

vi.mock("next/navigation", () => ({ useRouter: () => ({ replace: vi.fn(), push: vi.fn() }) }));
vi.mock("@/components/app-shell", () => ({
  AppShell: ({ children }: { children: React.ReactNode }) => <>{children}</>,
}));

import { AgentEditorView } from "./AgentEditorView";

afterEach(cleanup);

const agent = (provider: Agent["provider"]): Agent => ({
  id: "ag1",
  name: "Security Reviewer",
  description: "Flags secrets",
  provider,
  model: "m1",
  system_prompt: "You are a reviewer.",
  output_schema: null,
  strategy: "single-pass",
  ci_fail_on: "critical",
  repo_intel: true,
  enabled: true,
  version: 1,
});

const synapse = (name: string) => ({
  id: "custom:synapse",
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

function stub(provider: Agent["provider"], name = "Synapse") {
  const a = agent(provider);
  mockFetch({
    "GET /agents": [a],
    "GET /agents/ag1": a,
    "GET /settings/custom-providers": [synapse(name)],
    "GET /providers/:id/models": [],
  });
}

/** The title row above the tabs: the agent name next to its provider/model badge. */
async function headerOf(): Promise<HTMLElement> {
  const heading = await screen.findByRole("heading", { name: "Security Reviewer" });
  return heading.parentElement as HTMLElement;
}

describe("AgentEditorView — provider name", () => {
  it("shows the custom provider's current name next to the model", async () => {
    stub("custom:synapse", "Synapse EU");
    renderWithProviders(<AgentEditorView id="ag1" tab="config" period={{ key: "30d" }} />);
    const header = await headerOf();
    await waitFor(() => expect(within(header).getByText("Synapse EU")).toBeInTheDocument());
    expect(within(header).queryByText("custom:synapse")).toBeNull();
    expect(header).toHaveTextContent("Synapse EU/m1");
  });

  it("shows the raw id for an undefined custom provider", async () => {
    stub("custom:ghost");
    renderWithProviders(<AgentEditorView id="ag1" tab="config" period={{ key: "30d" }} />);
    const header = await headerOf();
    await waitFor(() => expect(within(header).getByText("custom:ghost")).toBeInTheDocument());
    expect(header).toHaveTextContent("custom:ghost/m1");
  });
});

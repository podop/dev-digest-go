/* TourView — header, TOC and share (AC15, AC19), empty / generating / stale / error states with the
   previous tour kept (AC20) and a repo switch during generation (AC21), through the real hooks over
   a stubbed fetch. AppShell is a passthrough; the repo list comes from the real <RepoProvider>. */
import type React from "react";
import { describe, it, expect, afterEach, beforeEach, vi } from "vitest";
import type { OnboardingTour, OnboardingTourState } from "@devdigest/shared";
import { renderWithProviders, screen, cleanup, within, waitFor } from "@/test/render";
import { jsonResponse, mockFetch } from "@/test/fetch-mock";
import { RepoProvider } from "@/lib/repo-context";
import { repoKeys } from "@/lib/hooks/keys";

vi.mock("next/navigation", () => ({
  useRouter: () => ({ push: vi.fn(), replace: vi.fn() }),
  usePathname: () => "/repos/r1/onboarding",
}));
vi.mock("@/components/app-shell", () => ({
  AppShell: ({ children }: { children: React.ReactNode }) => <>{children}</>,
}));

import { TourView } from "./TourView";

const REPO = (id: string, name: string) => ({
  id,
  workspace_id: "w1",
  owner: "acme",
  name,
  full_name: `acme/${name}`,
  default_branch: "main",
  clone_path: `/clones/acme/${name}`,
  last_polled_at: null,
  created_by: null,
});

const tourFor = (summary: string): OnboardingTour => ({
  repo_id: "r1",
  generated_at: new Date(Date.now() - 2 * 3_600_000).toISOString(),
  indexed_sha: "abc123",
  files_indexed: 12450,
  provider: "openai",
  model: "m",
  tokens_in: 1,
  tokens_out: 1,
  cost_usd: null,
  prompt_version: 1,
  language: "en",
  architecture: {
    summary,
    nodes: [
      { id: "a", label: "client", kind: "entry" },
      { id: "b", label: "server", kind: "module" },
    ],
    edges: [{ from: "a", to: "b" }],
  },
  critical_paths: [{ path: "src/server.ts", reason: "bootstrap" }],
  run_steps: [{ command: "pnpm install" }],
  reading_path: [{ path: "README.md", reason: "start here" }],
  first_tasks: [{ title: "Add a probe", path: "src/health.ts", complexity: "low" }],
});

const ready = (summary = "A Node service.", extra: Partial<Extract<OnboardingTourState, { status: "ready" }>> = {}): OnboardingTourState => ({
  status: "ready",
  stale: false,
  tour: tourFor(summary),
  ...extra,
});

const err = (status: number, code: string) => jsonResponse({ error: { code, message: "m" } }, status);

function setup(tour: OnboardingTourState | Response, extra: Record<string, unknown> = {}) {
  return mockFetch({
    "GET /repos": [REPO("r1", "payments-api"), REPO("r2", "other-repo")],
    "GET /repos/r1/onboarding": tour,
    "GET /repos/r1/index-state": { repoId: "r1", status: "full", filesIndexed: 12450, filesSkipped: 0, durationMs: 1, lastIndexedSha: "abc123", indexerVersion: 1, updatedAt: "2026-10-01T00:00:00.000Z" },
    ...extra,
  } as Parameters<typeof mockFetch>[0]);
}

const view = (repoId = "r1") => (
  <RepoProvider>
    <TourView key={repoId} repoId={repoId} />
  </RepoProvider>
);

function stubClipboard(writeText: (t: string) => Promise<void>) {
  vi.stubGlobal("navigator", { ...navigator, clipboard: { writeText } });
}

const scrollIntoView = vi.fn();
beforeEach(() => {
  scrollIntoView.mockReset();
  Element.prototype.scrollIntoView = scrollIntoView;
  window.location.hash = "";
});
afterEach(() => {
  cleanup();
  // jsdom has no scrollIntoView; leave it that way for other tests.
  Reflect.deleteProperty(Element.prototype, "scrollIntoView");
});

describe("TourView — tour screen", () => {
  it("shows the header, five TOC entries that scroll and highlight, and copies the share link with the section", async () => {
    setup(ready());
    const { user } = renderWithProviders(view());

    expect(await screen.findByRole("heading", { level: 1, name: "Onboarding for payments-api" })).toBeInTheDocument();
    expect(screen.getByText("Generated from index of 12,450 files · last refreshed 2 hours ago")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Regenerate" })).toBeEnabled();

    const toc = screen.getByRole("navigation", { name: "On this page" });
    const entries = within(toc).getAllByRole("button");
    expect(entries.map((b) => b.textContent)).toEqual([
      "Architecture overview",
      "Critical paths",
      "How to run locally",
      "Guided reading path",
      "First tasks",
    ]);
    expect(entries[0]).toHaveAttribute("aria-current", "true");

    await user.click(within(toc).getByRole("button", { name: "Critical paths" }));
    expect(within(toc).getByRole("button", { name: "Critical paths" })).toHaveAttribute("aria-current", "true");
    expect(entries[0]).not.toHaveAttribute("aria-current");
    expect(scrollIntoView.mock.contexts[0]).toBe(document.getElementById("critical-paths"));

    const writeText = vi.fn().mockResolvedValue(undefined);
    stubClipboard(writeText);
    await user.click(screen.getByRole("button", { name: "Share link" }));
    expect(writeText).toHaveBeenCalledWith(`${window.location.origin}${window.location.pathname}#critical-paths`);
    expect(await screen.findByText("Link copied")).toBeInTheDocument();

    stubClipboard(vi.fn().mockRejectedValue(new Error("denied")));
    await user.click(screen.getByRole("button", { name: "Share link" }));
    expect(await screen.findByText("Copy failed")).toBeInTheDocument();

    // A card collapses from its header.
    await user.click(screen.getByRole("button", { name: "Toggle section Architecture overview" }));
    expect(screen.queryByText("A Node service.")).not.toBeInTheDocument();
  });

  it("scrolls to the section named in the URL hash of a shared link", async () => {
    window.location.hash = "#first-tasks";
    setup(ready());
    renderWithProviders(view());

    await screen.findByRole("heading", { level: 1 });
    await waitFor(() => expect(scrollIntoView.mock.contexts[0]).toBe(document.getElementById("first-tasks")));
    const toc = screen.getByRole("navigation", { name: "On this page" });
    expect(within(toc).getByRole("button", { name: "First tasks" })).toHaveAttribute("aria-current", "true");
  });
});

describe("TourView — states", () => {
  it("offers Generate when there is no tour, and shows skeletons with both buttons disabled until the answer", async () => {
    let finish: (r: Response) => void = () => {};
    const api = setup(
      { status: "none" },
      {
        "GET /settings": { feature_models: { onboarding: { provider: "openrouter", model: "acme/tour-model" } } },
        "POST /repos/r1/onboarding": () => new Promise<Response>((resolve) => (finish = resolve)),
      },
    );
    const { user } = renderWithProviders(view());

    expect(await screen.findByText("No tour yet")).toBeInTheDocument();
    expect(
      await screen.findByText(/makes exactly one LLM call with acme\/tour-model to write a five-part tour/),
    ).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Share link" })).not.toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Generate tour" }));

    expect(await screen.findByText("Generating… (one LLM call)")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Regenerating…" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "Share link" })).toBeDisabled();
    expect(screen.queryByRole("button", { name: "Generate tour" })).not.toBeInTheDocument();
    expect(screen.getAllByRole("region", { hidden: false }).length).toBeGreaterThanOrEqual(5);
    expect(api.requests("POST", "/repos/r1/onboarding")).toHaveLength(1);

    finish(jsonResponse(ready("Fresh tour.")));
    expect(await screen.findByText("Fresh tour.")).toBeInTheDocument();
    expect(await screen.findByText("Tour updated")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Regenerate" })).toBeEnabled();
  });

  it("names the default model, or points to Settings when the model is unknown", async () => {
    setup({ status: "none" }, { "GET /settings": { feature_models: {} } });
    const first = renderWithProviders(view());
    expect(await screen.findByText(/exactly one LLM call with deepseek\/deepseek-v4-flash to write/)).toBeInTheDocument();
    first.unmount();

    setup({ status: "none" });
    renderWithProviders(view());
    expect(
      await screen.findByText(/exactly one LLM call with the model configured for Onboarding Tour in Settings/),
    ).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Generate tour" })).toBeEnabled();
  });

  it("disables Generate and says to index first when the repo has no indexed files", async () => {
    setup(
      { status: "none" },
      { "GET /repos/r1/index-state": { repoId: "r1", status: "degraded", filesIndexed: 0, filesSkipped: 0, durationMs: 0, lastIndexedSha: "", indexerVersion: 1, updatedAt: "2026-10-01T00:00:00.000Z" } },
    );
    renderWithProviders(view());

    expect(await screen.findByText(/Index this repo first/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Generate tour" })).toBeDisabled();
  });

  it("shows the stale banner with its own Regenerate", async () => {
    const api = setup(ready("Old.", { stale: true, stale_reason: "index_changed" }), {
      "POST /repos/r1/onboarding": ready("New."),
    });
    const { user } = renderWithProviders(view());

    const banner = await screen.findByRole("region", { name: "This tour may be out of date" });
    expect(within(banner).getByText(/The index changed since this tour was generated/)).toBeInTheDocument();
    await user.click(within(banner).getByRole("button", { name: "Regenerate" }));

    await waitFor(() => expect(api.requests("POST", "/repos/r1/onboarding")).toHaveLength(1));
    expect(await screen.findByText("New.")).toBeInTheDocument();
  });

  it.each([
    [409, "generation_in_progress", /Already generating .*\(generation_in_progress\)/],
    [422, "index_not_ready", /no indexed files yet\. \(index_not_ready\)/],
    [422, "provider_not_configured", /No API key is set for the Onboarding Tour model\. \(provider_not_configured\)/],
    [502, "generation_failed", /previous tour was kept\. \(generation_failed\)/],
  ])("toasts the %i %s error and keeps the previous tour", async (status, code, toast) => {
    setup(ready("Keep me."), { "POST /repos/r1/onboarding": err(status, code) });
    const { user } = renderWithProviders(view());

    await user.click(await screen.findByRole("button", { name: "Regenerate" }));

    expect(await screen.findByText(toast)).toBeInTheDocument();
    expect(screen.getByText("Keep me.")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Regenerate" })).toBeEnabled();
    if (code === "provider_not_configured") {
      expect(screen.getByRole("link", { name: "Open Feature models" })).toHaveAttribute("href", "/settings/models");
    } else {
      expect(screen.queryByRole("link", { name: "Open Feature models" })).not.toBeInTheDocument();
    }
  });

  it("shows a load error with Retry", async () => {
    setup(err(500, "internal"));
    renderWithProviders(view());

    expect(await screen.findByText("Couldn’t load the onboarding tour")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Retry" })).toBeInTheDocument();
  });
});

describe("TourView — repo switch (EC10)", () => {
  it("never renders repo A's generated tour on repo B", async () => {
    let finish: (r: Response) => void = () => {};
    setup({ status: "none" }, {
      "GET /repos/r2/onboarding": ready("B tour."),
      "GET /repos/r2/index-state": { repoId: "r2", status: "full", filesIndexed: 3, filesSkipped: 0, durationMs: 1, lastIndexedSha: "d", indexerVersion: 1, updatedAt: "2026-10-01T00:00:00.000Z" },
      "POST /repos/r1/onboarding": () => new Promise<Response>((resolve) => (finish = resolve)),
    });
    const { user, rerender, queryClient } = renderWithProviders(view("r1"));

    await user.click(await screen.findByRole("button", { name: "Generate tour" }));
    expect(await screen.findByText("Generating… (one LLM call)")).toBeInTheDocument();

    rerender(view("r2"));
    expect(await screen.findByText("B tour.")).toBeInTheDocument();
    expect(screen.getByRole("heading", { level: 1, name: "Onboarding for other-repo" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Regenerate" })).toBeEnabled();

    finish(jsonResponse(ready("A tour.")));
    // Deterministic signal: the late POST settled, so the hook-level onSuccess wrote A's tour into
    // repo A's own cache entry — only then is the absence on B meaningful.
    await waitFor(() =>
      expect(queryClient.getQueryData<OnboardingTourState>(repoKeys.onboarding("r1"))).toMatchObject({ status: "ready" }),
    );
    expect(screen.queryByText("A tour.")).not.toBeInTheDocument();
    expect(screen.queryByText("Tour updated")).not.toBeInTheDocument();
    expect(screen.getByText("B tour.")).toBeInTheDocument();
  });
});

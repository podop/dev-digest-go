import { describe, it, expect, afterEach, vi } from "vitest";
import { renderWithProviders, screen, cleanup, waitFor } from "@/test/render";
import { mockFetch, jsonResponse, type RouteHandler } from "@/test/fetch-mock";
import type { PrMeta } from "@/lib/types";

const replace = vi.fn();
const push = vi.fn();
vi.mock("next/navigation", () => ({
  useRouter: () => ({ replace, push }),
  usePathname: () => "/repos/r1/pulls",
}));

import { PullsView } from "./PullsView";

afterEach(() => {
  cleanup();
  replace.mockReset();
  push.mockReset();
});

function pr(o: Partial<PrMeta>): PrMeta {
  return {
    number: 1,
    title: "PR",
    author: "a",
    branch: "b",
    base: "main",
    head_sha: "x",
    additions: 1,
    deletions: 1,
    files_count: 1,
    status: "needs_review",
    updated_at: "2026-06-01T00:00:00Z",
    score: null,
    ...o,
  };
}

function setup(status = "needs_review", extra: Record<string, RouteHandler> = {}) {
  const api = mockFetch({
    "GET /repos": [{ id: "r1", full_name: "acme/api" }],
    "GET /repos/r1/pulls": [
      pr({ number: 10, title: "Add rate limiting" }),
      pr({ number: 11, title: "Fix login", status: "reviewed" }),
      pr({ number: 12, title: "Bump deps", status: "merged" }),
      pr({ number: 13, title: "Abandoned spike", status: "closed" }),
    ],
    ...extra,
  });
  return { ...renderWithProviders(<PullsView repoId="r1" status={status} sort="newest" />), api };
}

describe("PullsView", () => {
  it("shows a PR count on every status chip", async () => {
    setup();
    await screen.findByText("Add rate limiting");
    for (const label of ["All · 4", "Needs review · 1", "Reviewed · 1", "Stale · 0", "Merged · 1", "Closed · 1"]) {
      expect(screen.getByRole("button", { name: label })).toBeInTheDocument();
    }
  });

  it("counts merged PRs in the header summary", async () => {
    setup();
    expect(await screen.findByText("2 open · 1 need review · 1 merged")).toBeInTheDocument();
  });

  it("the Merged chip lists merged PRs", async () => {
    setup("merged");
    expect(await screen.findByText("Bump deps")).toBeInTheDocument();
    expect(screen.queryByText("Add rate limiting")).toBeNull();
  });

  it("the Closed chip lists closed PRs", async () => {
    setup("closed");
    expect(await screen.findByText("Abandoned spike")).toBeInTheDocument();
    expect(screen.queryByText("Bump deps")).toBeNull();
  });

  it("lists only the PRs matching the status from the URL", async () => {
    setup();
    expect(await screen.findByText("Add rate limiting")).toBeInTheDocument();
    expect(screen.queryByText("Fix login")).toBeNull();
  });

  it("writes a status change to the URL instead of local state", async () => {
    const { user } = setup();
    await screen.findByText("Add rate limiting");
    await user.click(screen.getByText("All · 4"));
    expect(replace).toHaveBeenCalledWith("/repos/r1/pulls?status=all", { scroll: false });
  });

  describe("Auto-review pill", () => {
    const STATUS = "GET /settings/automatic-reviews-status";
    const status = (state: string) => ({
      state,
      last_poll_at: null,
      prs_tracked: 4,
      poll_interval_min: 1,
    });

    it("is ON with the polling interval when automatic reviews are active, and opens the settings", async () => {
      const { user } = setup("needs_review", { [STATUS]: status("active") });
      expect(await screen.findByText("ON")).toBeInTheDocument();
      expect(screen.getByText("· polling 1m")).toBeInTheDocument();
      expect(screen.queryByText("OFF")).toBeNull();

      await user.click(screen.getByRole("button", { name: /Auto-review/ }));
      expect(push).toHaveBeenCalledWith("/settings/automatic-reviews");
    });

    it.each(["off", "no_agents", "paused_spend_cap"])("is OFF without a detail when the state is %s", async (state) => {
      setup("needs_review", { [STATUS]: status(state) });
      expect(await screen.findByText("OFF")).toBeInTheDocument();
      expect(screen.queryByText("ON")).toBeNull();
      expect(screen.queryByText(/polling/)).toBeNull();
    });

    it("shows no pill while the status loads", async () => {
      setup("needs_review", { [STATUS]: () => new Promise(() => {}) });
      await screen.findByText("Add rate limiting");
      expect(screen.queryByText("ON")).toBeNull();
      expect(screen.queryByText("OFF")).toBeNull();
      expect(screen.queryByRole("button", { name: /Auto-review/ })).toBeNull();
    });

    it("shows no pill when the status request fails", async () => {
      const { api } = setup("needs_review", {
        [STATUS]: jsonResponse({ error: { code: "internal", message: "boom" } }, 500),
      });
      await screen.findByText("Add rate limiting");
      await waitFor(() => expect(api.requests("GET", "/settings/automatic-reviews-status").length).toBeGreaterThan(0));
      await new Promise((resolve) => setTimeout(resolve, 50));
      expect(screen.queryByText("ON")).toBeNull();
      expect(screen.queryByText("OFF")).toBeNull();
    });
  });
});

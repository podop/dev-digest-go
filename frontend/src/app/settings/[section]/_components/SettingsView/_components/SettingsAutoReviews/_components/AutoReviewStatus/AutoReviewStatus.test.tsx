/* AutoReviewStatus — the status line of automatic reviews (FR17, AC20, EC18). Rendered alone; the status
   hook runs for real against a stubbed fetch. */
import { describe, it, expect, afterEach, vi } from "vitest";
import type { AutomaticReviewsStatus } from "@/lib/types";
import { renderWithProviders, screen, cleanup, waitFor, act, pseudoMessages } from "@/test/render";
import { mockFetch, jsonResponse } from "@/test/fetch-mock";
import { strayText } from "@/test/pseudo";
import { AutoReviewStatus } from "./AutoReviewStatus";

const ROUTE = "GET /settings/automatic-reviews-status";
const NOW = Date.parse("2026-10-25T12:00:00.000Z");

afterEach(() => {
  cleanup();
  vi.useRealTimers();
});

/** The status line holding `text` (the toast region is a role=status too, so a role query is ambiguous). */
const line = (text: RegExp) => screen.findByText(text);

function status(over: Partial<AutomaticReviewsStatus> = {}): AutomaticReviewsStatus {
  return {
    state: "active",
    last_poll_at: new Date(NOW - 2 * 60_000).toISOString(),
    prs_tracked: 7,
    poll_interval_min: 5,
    ...over,
  };
}

describe("AutoReviewStatus", () => {
  it("shows 'Checking status…' while loading, then Active with the last poll age and the PR count", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    vi.setSystemTime(NOW);
    mockFetch({ [ROUTE]: status() });
    renderWithProviders(<AutoReviewStatus />);

    expect(screen.getByText("Checking status…")).toBeInTheDocument();
    expect(await line(/last poll/)).toHaveTextContent("Active · last poll 2 min ago · 7 PRs tracked");
    expect(screen.getByText("Active").tagName).toBe("STRONG");
    expect(screen.queryByRole("link")).not.toBeInTheDocument();
  });

  it.each([
    [{ last_poll_at: null, prs_tracked: 0 }, "Active · last poll never · 0 PRs tracked"],
    [{ last_poll_at: new Date(NOW - 20_000).toISOString(), prs_tracked: 1 }, "Active · last poll just now · 1 PR tracked"],
    [{ last_poll_at: new Date(NOW - 3 * 3_600_000).toISOString() }, "Active · last poll 3 h ago · 7 PRs tracked"],
    [{ last_poll_at: new Date(NOW - 50 * 3_600_000).toISOString() }, "Active · last poll 2 d ago · 7 PRs tracked"],
  ] as const)("renders the relative time and plural for %o", async (over, text) => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    vi.setSystemTime(NOW);
    mockFetch({ [ROUTE]: status(over) });
    renderWithProviders(<AutoReviewStatus />);

    expect(await line(/last poll/)).toHaveTextContent(text);
  });

  it.each([
    ["off", "Off — PRs are reviewed only when you start a review", null],
    ["poll_disabled", "Paused — background poll is off for this server (PR_POLL_ENABLED)", null],
    ["no_github_token", "Paused — no GitHub token", ["GitHub Integration", "/settings/github"]],
    ["no_agents", "Paused — no enabled agent selected", null],
    ["no_triggers", "Paused — no trigger condition selected", null],
    ["paused_spend_cap", "Paused — spend cap reached", ["API Keys", "/settings/api-keys"]],
  ] as const)("shows the copy of the %s state", async (state, text, link) => {
    mockFetch({ [ROUTE]: status({ state }) });
    renderWithProviders(<AutoReviewStatus />);

    expect(await screen.findByText(text)).toBeInTheDocument();
    if (link === null) {
      expect(screen.queryByRole("link")).not.toBeInTheDocument();
    } else {
      expect(screen.getByRole("link", { name: link[0] })).toHaveAttribute("href", link[1]);
    }
    expect(screen.queryByText(/last poll/)).not.toBeInTheDocument();
  });

  it("no_code_host_token (FR28) pauses with copy and links to both the GitHub and the GitLab section", async () => {
    mockFetch({ [ROUTE]: status({ state: "no_code_host_token" }) });
    renderWithProviders(<AutoReviewStatus />);

    expect(await screen.findByText("Paused — no code host token: connect GitHub or GitLab")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "GitHub Integration" })).toHaveAttribute("href", "/settings/github");
    expect(screen.getByRole("link", { name: "GitLab Integration" })).toHaveAttribute("href", "/settings/gitlab");
    expect(screen.queryByText(/last poll/)).not.toBeInTheDocument();
  });

  it("shows 'Status unavailable' with Retry when the request fails, and recovers on Retry", async () => {
    let fail = true;
    const api = mockFetch({
      [ROUTE]: () => (fail ? jsonResponse({ error: { code: "internal", message: "down" } }, 500) : status({ state: "no_agents" })),
    });
    const { user } = renderWithProviders(<AutoReviewStatus />);

    expect(await screen.findByText("Status unavailable")).toBeInTheDocument();

    fail = false;
    await user.click(screen.getByRole("button", { name: "Retry" }));
    expect(await screen.findByText("Paused — no enabled agent selected")).toBeInTheDocument();
    expect(screen.queryByText("Status unavailable")).not.toBeInTheDocument();
    expect(api.requests("GET", "/settings/automatic-reviews-status")).toHaveLength(2);
  });

  it("refetches every 30 s while mounted (A12), updates the line, and stops after unmount", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    vi.setSystemTime(NOW);
    let current = status();
    const api = mockFetch({ [ROUTE]: () => current });
    const { unmount } = renderWithProviders(<AutoReviewStatus />);

    expect(await line(/last poll 2 min ago/)).toBeInTheDocument();
    expect(api.requests("GET", "/settings/automatic-reviews-status")).toHaveLength(1);

    current = status({ state: "paused_spend_cap" });
    await act(() => vi.advanceTimersByTimeAsync(30_100));
    await waitFor(() => expect(api.requests("GET", "/settings/automatic-reviews-status")).toHaveLength(2));
    expect(await screen.findByText("Paused — spend cap reached")).toBeInTheDocument();

    unmount();
    await act(() => vi.advanceTimersByTimeAsync(65_000));
    expect(api.requests("GET", "/settings/automatic-reviews-status")).toHaveLength(2);
  });

  it("takes all its copy from the message files (pseudo-messages)", async () => {
    mockFetch({ [ROUTE]: status({ state: "no_github_token" }) });
    const { container } = renderWithProviders(<AutoReviewStatus />, { messages: pseudoMessages() });

    expect(await screen.findByText("⟦autoReviewStatus.states.no_github_token⟧")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "⟦autoReviewStatus.links.github⟧" })).toBeInTheDocument();
    expect(strayText(container)).toEqual([]);
  });

  it("marks the no_code_host_token copy and both links as messages", async () => {
    mockFetch({ [ROUTE]: status({ state: "no_code_host_token" }) });
    const { container } = renderWithProviders(<AutoReviewStatus />, { messages: pseudoMessages() });

    expect(await screen.findByText("⟦autoReviewStatus.states.no_code_host_token⟧")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "⟦autoReviewStatus.links.github⟧" })).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "⟦autoReviewStatus.links.gitlab⟧" })).toBeInTheDocument();
    expect(strayText(container)).toEqual([]);
  });

  it("marks the loading, error and active texts as messages too", async () => {
    mockFetch({ [ROUTE]: () => jsonResponse({ error: { code: "internal", message: "down" } }, 500) });
    const { container } = renderWithProviders(<AutoReviewStatus />, { messages: pseudoMessages() });

    expect(screen.getByText("⟦autoReviewStatus.checking⟧")).toBeInTheDocument();
    expect(await screen.findByText("⟦autoReviewStatus.unavailable⟧")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "⟦common.actions.retry⟧" })).toBeInTheDocument();
    expect(strayText(container)).toEqual([]);

    cleanup();
    mockFetch({ [ROUTE]: status() });
    const active = renderWithProviders(<AutoReviewStatus />, { messages: pseudoMessages() });
    expect(await screen.findByText("⟦autoReviewStatus.states.active⟧")).toBeInTheDocument();
    expect(strayText(active.container)).toEqual([]);
  });
});

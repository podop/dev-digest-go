/* Header — window label, period toggle, repo select and per-repo sync note (FR18, FR23, AC15, AC19). */
import { describe, it, expect, afterEach, vi } from "vitest";
import type { LeaderboardPeriod, LeaderboardRepo } from "@devdigest/shared";
import { renderWithProviders, screen, within, cleanup, pseudoMessages } from "@/test/render";
import { pickOption } from "@/test/select";
import { strayText } from "@/test/pseudo";
import { Header, type HeaderProps } from "./Header";

afterEach(cleanup);

const WINDOW: LeaderboardPeriod = {
  key: "week",
  tz: "Europe/Kyiv",
  from: "2026-09-28T00:00:00.000+03:00",
  to: "2026-10-05T00:00:00.000+03:00",
  previous_from: "2026-09-21T00:00:00.000+03:00",
  previous_to: "2026-09-28T00:00:00.000+03:00",
};
const REPOS = [
  { id: "r1", full_name: "acme/api" },
  { id: "r2", full_name: "acme/web" },
];

function props(over: Partial<HeaderProps> = {}): HeaderProps {
  return {
    period: "week",
    repo: "all",
    repos: REPOS,
    window: WINDOW,
    syncRepos: null,
    onPeriodChange: vi.fn(),
    onRepoChange: vi.fn(),
    ...over,
  };
}

describe("Header", () => {
  it("shows the window label and lets the user switch period and repository", async () => {
    const p = props();
    const { user, rerender } = renderWithProviders(<Header {...p} />);

    expect(screen.getByRole("heading", { level: 1, name: "PR Leaderboard" })).toBeInTheDocument();
    expect(screen.getByText("Week of Sep 28 – Oct 4 · compared with Sep 21 – 27")).toBeInTheDocument();

    const periods = within(screen.getByRole("group", { name: "Period" }));
    expect(periods.getByRole("button", { name: "This week" })).toHaveAttribute("aria-pressed", "true");
    expect(periods.getByRole("button", { name: "Last week" })).toHaveAttribute("aria-pressed", "false");
    expect(periods.getByRole("button", { name: "30d" })).toHaveAttribute("aria-pressed", "false");

    await user.click(periods.getByRole("button", { name: "This week" }));
    expect(p.onPeriodChange).not.toHaveBeenCalled();
    await user.click(periods.getByRole("button", { name: "30d" }));
    expect(p.onPeriodChange).toHaveBeenCalledWith("30d");

    const select = screen.getByRole("combobox", { name: "Repository" });
    expect(select).toHaveTextContent("All repositories");
    await pickOption(user, select, "acme/web");
    expect(p.onRepoChange).toHaveBeenCalledWith("r2");

    // 30d: no "Week of" prefix.
    rerender(
      <Header
        {...p}
        period="30d"
        repo="r2"
        window={{
          ...WINDOW,
          key: "30d",
          from: "2026-09-05T00:00:00.000+03:00",
          previous_from: "2026-08-06T00:00:00.000+03:00",
          previous_to: "2026-09-05T00:00:00.000+03:00",
        }}
      />,
    );
    expect(screen.getByText("Sep 5 – Oct 4 · compared with Aug 6 – Sep 4")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "30d" })).toHaveAttribute("aria-pressed", "true");
    expect(screen.getByRole("combobox", { name: "Repository" })).toHaveTextContent("acme/web");
  });

  it("has no label and a disabled repo select while loading", () => {
    renderWithProviders(<Header {...props({ window: null, repos: undefined })} />);
    expect(screen.queryByText(/compared with/)).not.toBeInTheDocument();
    expect(screen.queryByRole("list", { name: "Repository sync" })).not.toBeInTheDocument();
    expect(screen.getByRole("combobox", { name: "Repository" })).toHaveAttribute("aria-disabled", "true");
  });

  it("notes when each repository was last synced, or that it was never polled; all copy comes from messages", () => {
    const hoursAgo = new Date(Date.now() - 3 * 3_600_000).toISOString();
    const syncRepos: LeaderboardRepo[] = [
      { id: "r1", full_name: "acme/api", last_polled_at: hoursAgo },
      { id: "r2", full_name: "acme/web", last_polled_at: null },
    ];
    const { unmount } = renderWithProviders(<Header {...props({ syncRepos })} />);
    const items = within(screen.getByRole("list", { name: "Repository sync" })).getAllByRole("listitem");
    expect(items[0]).toHaveTextContent("acme/api synced 3 hours ago");
    expect(items[1]).toHaveTextContent("acme/web never polled");
    unmount();

    const pseudo = renderWithProviders(<Header {...props({ syncRepos })} />, { messages: pseudoMessages() });
    expect(strayText(pseudo.container, ["acme/api", "acme/web"])).toEqual([]);
  });
});

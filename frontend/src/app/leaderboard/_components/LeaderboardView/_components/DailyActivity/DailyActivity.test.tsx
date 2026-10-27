import { describe, expect, it } from "vitest";
import type { LeaderboardActivity, LeaderboardActivityPoint } from "@devdigest/shared";
import { renderWithProviders, screen, within } from "@/test/render";
import { DailyActivity } from "./DailyActivity";
import { BAR_AREA } from "./styles";
import { formatDay, formatMonthDay } from "./helpers";

const point = (day: string, opened: number, merged: number, critical: number): LeaderboardActivityPoint => ({
  from: day,
  to: day,
  opened,
  merged,
  critical,
});
const days: LeaderboardActivity = {
  granularity: "day",
  points: [point("2026-10-19", 4, 2, 3), point("2026-10-20", 0, 0, 0), point("2026-10-21", 2, 4, 0)],
};
const weeks: LeaderboardActivity = {
  granularity: "week",
  points: [
    { from: "2026-09-25", to: "2026-10-04", opened: 3, merged: 1, critical: 0 },
    { from: "2026-10-05", to: "2026-10-11", opened: 6, merged: 5, critical: 2 },
  ],
};
const quiet = (a: LeaderboardActivity): LeaderboardActivity => ({
  ...a,
  points: a.points.map((p) => ({ ...p, opened: 0, merged: 0, critical: 0 })),
});

describe("DailyActivity", () => {
  it("draws grouped bars scaled to the busiest value with counts, critical badges, day labels and a legend", () => {
    renderWithProviders(<DailyActivity activity={days} />);

    const cols = Array.from(document.querySelectorAll<HTMLElement>("[data-series]"));
    expect(cols.map((c) => c.getAttribute("data-series"))).toEqual(["2026-10-19", "2026-10-20", "2026-10-21"]);

    const mon = screen.getByRole("img", { name: "Mon 19: 4 opened, 2 merged, 3 critical" });
    const monQ = within(mon);
    expect(monQ.getByText("Mon 19")).toBeInTheDocument();
    expect(monQ.getByText("4")).toBeInTheDocument();
    expect(monQ.getByText("2")).toBeInTheDocument();
    expect(monQ.getByText("3")).toBeInTheDocument();
    expect(mon.querySelector("[data-bar=opened]")).toHaveStyle({ height: `${BAR_AREA}px`, background: "var(--accent)" });
    expect(mon.querySelector("[data-bar=merged]")).toHaveStyle({ height: `${BAR_AREA / 2}px`, background: "var(--ok)" });

    const tue = screen.getByRole("img", { name: "Tue 20: 0 opened, 0 merged, 0 critical" });
    expect(tue.querySelector("[data-bar=opened]")).toHaveStyle({ height: "0px" });
    expect(within(tue).queryByText("0")).not.toBeInTheDocument();

    const wed = screen.getByRole("img", { name: "Wed 21: 2 opened, 4 merged, 0 critical" });
    expect(wed.querySelector("[data-bar=merged]")).toHaveStyle({ height: `${BAR_AREA}px` });

    expect(screen.getByText("Opened")).toBeInTheDocument();
    expect(screen.getByText("Merged")).toBeInTheDocument();
    expect(screen.getByText("Critical")).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "Daily activity" })).toBeInTheDocument();
  });

  it('labels a week point "Wk of Oct 5" for the 30d granularity and titles the card Weekly activity', () => {
    renderWithProviders(<DailyActivity activity={weeks} />);

    expect(screen.getByRole("heading", { name: "Weekly activity" })).toBeInTheDocument();
    const second = screen.getByRole("img", { name: "Wk of Oct 5: 6 opened, 5 merged, 2 critical" });
    expect(within(second).getByText("Wk of Oct 5")).toBeInTheDocument();
    expect(screen.getByText("Wk of Sep 25")).toBeInTheDocument();
    expect(second.querySelector("[data-bar=opened]")).toHaveStyle({ height: `${BAR_AREA}px` });
    expect(screen.queryByText(/Mon|Tue|Wed|Thu|Fri|Sat|Sun/)).not.toBeInTheDocument();
  });

  it("shows the empty state when no day has activity", () => {
    renderWithProviders(<DailyActivity activity={quiet(days)} />);
    expect(screen.getByText("No activity")).toBeInTheDocument();
    expect(screen.queryByText("Opened")).not.toBeInTheDocument();

    renderWithProviders(<DailyActivity activity={{ granularity: "day", points: [] }} />);
    expect(screen.getAllByText("No activity")).toHaveLength(2);
  });
});

describe("formatDay", () => {
  it("formats a calendar day without timezone shifts and keeps garbage as-is", () => {
    expect(formatDay("2026-10-28")).toBe("Wed 28");
    expect(formatDay("2026-11-01")).toBe("Sun 1");
    expect(formatDay("nope")).toBe("nope");
  });

  it("formats the first day of a week as month and day", () => {
    expect(formatMonthDay("2026-10-05")).toBe("Oct 5");
    expect(formatMonthDay("2026-12-31")).toBe("Dec 31");
    expect(formatMonthDay("nope")).toBe("nope");
  });
});

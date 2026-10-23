import { describe, expect, it } from "vitest";
import type { LeaderboardDay } from "@devdigest/shared";
import { renderWithProviders, screen, within } from "@/test/render";
import { DailyActivity } from "./DailyActivity";
import { formatDay } from "./helpers";

const days: LeaderboardDay[] = [
  { date: "2026-10-19", opened: 4, merged: 2, critical: 3 },
  { date: "2026-10-20", opened: 0, merged: 0, critical: 0 },
  { date: "2026-10-21", opened: 2, merged: 4, critical: 0 },
];

describe("DailyActivity", () => {
  it("draws grouped bars scaled to the busiest value with counts, critical badges, day labels and a legend", () => {
    renderWithProviders(<DailyActivity days={days} />);

    const cols = Array.from(document.querySelectorAll<HTMLElement>("[data-series]"));
    expect(cols.map((c) => c.getAttribute("data-series"))).toEqual(["2026-10-19", "2026-10-20", "2026-10-21"]);

    const mon = screen.getByRole("img", { name: "Mon 19: 4 opened, 2 merged, 3 critical" });
    const monQ = within(mon);
    expect(monQ.getByText("Mon 19")).toBeInTheDocument();
    expect(monQ.getByText("4")).toBeInTheDocument();
    expect(monQ.getByText("2")).toBeInTheDocument();
    expect(monQ.getByText("3")).toBeInTheDocument();
    expect(mon.querySelector("[data-bar=opened]")).toHaveStyle({ height: "100px", background: "var(--accent)" });
    expect(mon.querySelector("[data-bar=merged]")).toHaveStyle({ height: "50px", background: "var(--ok)" });

    const tue = screen.getByRole("img", { name: "Tue 20: 0 opened, 0 merged, 0 critical" });
    expect(tue.querySelector("[data-bar=opened]")).toHaveStyle({ height: "0px" });
    expect(within(tue).queryByText("0")).not.toBeInTheDocument();

    const wed = screen.getByRole("img", { name: "Wed 21: 2 opened, 4 merged, 0 critical" });
    expect(wed.querySelector("[data-bar=merged]")).toHaveStyle({ height: "100px" });

    expect(screen.getByText("Opened")).toBeInTheDocument();
    expect(screen.getByText("Merged")).toBeInTheDocument();
    expect(screen.getByText("Critical")).toBeInTheDocument();
  });

  it("shows the empty state when no day has activity", () => {
    renderWithProviders(<DailyActivity days={days.map((d) => ({ ...d, opened: 0, merged: 0, critical: 0 }))} />);
    expect(screen.getByText("No activity")).toBeInTheDocument();
    expect(screen.queryByText("Opened")).not.toBeInTheDocument();

    renderWithProviders(<DailyActivity days={[]} />);
    expect(screen.getAllByText("No activity")).toHaveLength(2);
  });
});

describe("formatDay", () => {
  it("formats a calendar day without timezone shifts and keeps garbage as-is", () => {
    expect(formatDay("2026-10-28")).toBe("Wed 28");
    expect(formatDay("2026-11-01")).toBe("Sun 1");
    expect(formatDay("nope")).toBe("nope");
  });
});

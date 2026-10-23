import { describe, it, expect, afterEach } from "vitest";
import type { LeaderboardKpis } from "@devdigest/shared";
import { renderWithProviders, screen, cleanup, within, pseudoMessages } from "@/test/render";
import { strayText } from "@/test/pseudo";
import { KpiTiles } from "./KpiTiles";
import { KPI_SPECS, durationParts, kpiDelta } from "./helpers";

afterEach(cleanup);

const tile = (name: string) => within(screen.getByRole("group", { name }));

const summary: LeaderboardKpis = {
  prs_reviewed: 12,
  merged: 8,
  avg_health: 82.46,
  time_to_first_review_s: 7500,
  time_to_merge_s: 90_000,
  critical_found: 3,
  open_critical: 0,
  review_cost_usd: 1.5,
};
const previous: LeaderboardKpis = {
  prs_reviewed: 10,
  merged: 8,
  avg_health: 80,
  time_to_first_review_s: 3600,
  time_to_merge_s: 180_000,
  critical_found: 5,
  open_critical: 0,
  review_cost_usd: 1.1,
};

describe("KpiTiles", () => {
  it("shows eight tiles with values and arrow + amount deltas that say whether the move is better or worse", () => {
    renderWithProviders(<KpiTiles summary={summary} previous={previous} />);

    expect(screen.getAllByRole("group")).toHaveLength(8);
    expect(tile("PRs reviewed").getByText("12")).toBeInTheDocument();
    expect(tile("PRs reviewed").getByText("↑ 2")).toBeInTheDocument();
    expect(tile("PRs reviewed").getByText(/better than the previous period/)).toBeInTheDocument();
    expect(tile("Merged").getByText("8")).toBeInTheDocument();
    expect(tile("Merged").getByText("no change")).toBeInTheDocument();
    expect(tile("Avg health").getByText("82.5")).toBeInTheDocument();
    expect(tile("Avg health").getByText("↑ 2.5")).toBeInTheDocument();
    // lower is better for times, criticals and cost
    expect(tile("Time to first review").getByText("2h 5m")).toBeInTheDocument();
    expect(tile("Time to first review").getByText("↑ 1h 5m")).toBeInTheDocument();
    expect(tile("Time to first review").getByText(/worse than the previous period/)).toBeInTheDocument();
    expect(tile("Time to merge").getByText("1d 1h")).toBeInTheDocument();
    expect(tile("Time to merge").getByText("↓ 1d 1h")).toBeInTheDocument();
    expect(tile("Time to merge").getByText(/better than the previous period/)).toBeInTheDocument();
    expect(tile("Critical found").getByText("↓ 2")).toBeInTheDocument();
    expect(tile("Open critical").getByText("0")).toBeInTheDocument();
    expect(tile("Open critical").getByText("no change")).toBeInTheDocument();
    expect(tile("Review cost").getByText("$1.50")).toBeInTheDocument();
    expect(tile("Review cost").getByText("↑ $0.40")).toBeInTheDocument();
    expect(tile("Review cost").getByText(/worse than the previous period/)).toBeInTheDocument();
  });

  it('writes "—" for a null value and drops the delta when either side is null or there is no previous summary', () => {
    const { rerender } = renderWithProviders(
      <KpiTiles summary={{ ...summary, avg_health: null, time_to_merge_s: null }} previous={{ ...previous, time_to_first_review_s: null }} />,
    );
    expect(tile("Avg health").getByText("—")).toBeInTheDocument();
    expect(tile("Avg health").queryByText(/↑|↓|no change/)).not.toBeInTheDocument();
    expect(tile("Time to merge").getByText("—")).toBeInTheDocument();
    expect(tile("Time to merge").queryByText(/↑|↓|no change/)).not.toBeInTheDocument();
    expect(tile("Time to first review").getByText("2h 5m")).toBeInTheDocument();
    expect(tile("Time to first review").queryByText(/↑|↓|no change/)).not.toBeInTheDocument();
    expect(tile("PRs reviewed").getByText("↑ 2")).toBeInTheDocument();

    // 30d, or a previous week without rows (previous_summary null): values only
    rerender(<KpiTiles summary={summary} previous={null} />);
    expect(tile("PRs reviewed").getByText("12")).toBeInTheDocument();
    expect(tile("Avg health").getByText("82.5")).toBeInTheDocument();
    expect(screen.queryByText(/↑|↓|no change/)).not.toBeInTheDocument();
  });

  it("shows Open critical in red while it is above 0 and in the normal colour at 0", () => {
    const { rerender } = renderWithProviders(<KpiTiles summary={{ ...summary, open_critical: 2 }} previous={previous} />);
    expect(tile("Open critical").getByText("2")).toHaveStyle({ color: "var(--crit)" });
    expect(tile("Open critical").getByText("↑ 2")).toBeInTheDocument();
    expect(tile("Critical found").getByText("3")).not.toHaveStyle({ color: "var(--crit)" });

    rerender(<KpiTiles summary={summary} previous={previous} />);
    expect(tile("Open critical").getByText("0")).not.toHaveStyle({ color: "var(--crit)" });
  });

  it("takes every visible string from the message files", () => {
    const { container } = renderWithProviders(<KpiTiles summary={summary} previous={previous} />, { messages: pseudoMessages() });
    expect(strayText(container, [/^\$?[\d.]+$/])).toEqual([]);
  });
});

describe("kpi helpers", () => {
  it("colours a move by the metric's good direction and ignores float noise", () => {
    const spec = (key: string) => KPI_SPECS.find((k) => k.key === key)!;
    expect(kpiDelta(spec("merged"), 5, 3)).toEqual({ direction: "up", amount: 2, good: true });
    expect(kpiDelta(spec("open_critical"), 5, 3)).toEqual({ direction: "up", amount: 2, good: false });
    expect(kpiDelta(spec("review_cost_usd"), 0.1 + 0.2, 0.3)).toEqual({ direction: "same", amount: 0, good: null });
    expect(kpiDelta(spec("avg_health"), 80.04, 80.0)).toEqual({ direction: "same", amount: 0, good: null });
    expect(kpiDelta(spec("avg_health"), 80.2, 80.0)).toEqual({ direction: "up", amount: 0.2, good: true });
    // |Δ| < 0.05 is "no change" in the KPI's own unit
    expect(kpiDelta(spec("review_cost_usd"), 1.04, 1)).toEqual({ direction: "same", amount: 0, good: null });
    expect(kpiDelta(spec("review_cost_usd"), 1.05, 1)).toEqual({ direction: "up", amount: 0.05, good: false });
    expect(kpiDelta(spec("avg_health"), null, 80)).toBeNull();
  });

  it("splits seconds into the two most significant units", () => {
    expect(durationParts(45)).toEqual({ key: "seconds", values: { s: 45 } });
    expect(durationParts(59 * 60 + 59)).toEqual({ key: "minutes", values: { m: 59 } });
    expect(durationParts(3600 + 1200)).toEqual({ key: "hoursMinutes", values: { h: 1, m: 20 } });
    expect(durationParts(2 * 86_400 + 4 * 3600 + 59)).toEqual({ key: "daysHours", values: { d: 2, h: 4 } });
  });
});

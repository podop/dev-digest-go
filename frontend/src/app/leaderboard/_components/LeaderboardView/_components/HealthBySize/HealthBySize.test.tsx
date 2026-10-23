import { describe, expect, it } from "vitest";
import type { LeaderboardSizeBucket } from "@devdigest/shared";
import { renderWithProviders, screen, within } from "@/test/render";
import { HealthBySize } from "./HealthBySize";

const buckets: LeaderboardSizeBucket[] = [
  { size: "S", prs: 4, avg_health: 91.4, avg_findings: 0.5 },
  { size: "M", prs: 1, avg_health: 62, avg_findings: 3 },
  { size: "L", prs: 2, avg_health: 30.2, avg_findings: 7.25 },
  { size: "XL", prs: 0, avg_health: null, avg_findings: null },
];

describe("HealthBySize", () => {
  it("renders a coloured bar, health and summary per bucket and a dash for an empty one", () => {
    renderWithProviders(<HealthBySize buckets={buckets} />);

    expect(screen.getByText("Smaller PRs ship cleaner.")).toBeInTheDocument();
    const rows = within(screen.getByRole("list", { name: "Health by PR size" })).getAllByRole("listitem");
    expect(rows.map((r) => r.getAttribute("data-series"))).toEqual(["S", "M", "L", "XL"]);

    const s = within(rows[0]!);
    expect(s.getByText("91")).toBeInTheDocument();
    expect(s.getByText("4 PRs · 0.5 findings")).toBeInTheDocument();
    const sBar = s.getByRole("img", { name: "Size S: average health 91" });
    expect(sBar.firstElementChild).toHaveStyle({ width: "91.4%", background: "var(--ok)" });

    const m = within(rows[1]!);
    expect(m.getByText("1 PR · 3 findings")).toBeInTheDocument();
    expect(m.getByRole("img").firstElementChild).toHaveStyle({ background: "var(--warn)" });

    const l = within(rows[2]!);
    expect(l.getByText("30")).toBeInTheDocument();
    expect(l.getByText("2 PRs · 7.3 findings")).toBeInTheDocument();
    expect(l.getByRole("img").firstElementChild).toHaveStyle({ background: "var(--crit)" });

    const xl = within(rows[3]!);
    expect(xl.getByText("—")).toBeInTheDocument();
    expect(xl.queryByRole("img")).not.toBeInTheDocument();
    expect(xl.queryByText(/PRs/)).not.toBeInTheDocument();
  });

  it("shows the empty state when no bucket has PRs", () => {
    renderWithProviders(
      <HealthBySize buckets={buckets.map((b) => ({ ...b, prs: 0, avg_health: null, avg_findings: null }))} />,
    );

    expect(screen.getByText("No PRs by size")).toBeInTheDocument();
    expect(screen.queryByRole("list")).not.toBeInTheDocument();
  });
});

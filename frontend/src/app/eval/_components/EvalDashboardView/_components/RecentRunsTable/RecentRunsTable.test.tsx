/* RecentRunsTable — agent, date, version, three bars with percents and pass; no model or
   cost; headers exist for assistive technology only. */
import { describe, it, expect, afterEach } from "vitest";
import type { EvalRecentRun } from "@devdigest/shared";
import { renderWithProviders, screen, cleanup, within } from "@/test/render";
import { makeRun } from "@/test/eval-fixtures";
import { METRIC_COLOR } from "@/app/eval/constants";
import { RecentRunsTable } from "./RecentRunsTable";

afterEach(cleanup);

const RAN = new Date(2026, 4, 29, 9, 14).toISOString();
const RUNS: EvalRecentRun[] = [
  { ...makeRun({ id: "r1", agent_version: 7, started_at: RAN, cases_passed: 17, cases_total: 20, recall: 0.82, precision: 0.91, citation_accuracy: 0.95 }), agent_name: "Security Reviewer" },
  { ...makeRun({ id: "r2", agent_version: 2, status: "failed", error: "boom", recall: null, precision: null, citation_accuracy: null }), agent_name: "Perf Reviewer" },
];

describe("RecentRunsTable", () => {
  it("shows a row per run with bars, percents and pass, dashes for null metrics, no model or cost; headers are in the table for screen readers", () => {
    renderWithProviders(<RecentRunsTable runs={RUNS} />);
    const table = within(screen.getByRole("table", { name: "Recent eval runs of all agents" }));
    expect(table.getAllByRole("columnheader").map((h) => h.textContent)).toEqual([
      "Agent", "Ran at", "Version", "Recall", "Precision", "Citation", "Pass",
    ]);

    // AC17: the headers are visually hidden (srOnly) but stay in the accessibility tree.
    for (const header of table.getAllByRole("columnheader")) {
      expect(header.firstElementChild).toHaveStyle({ position: "absolute", width: "1px", height: "1px", overflow: "hidden" });
    }

    const [first, second] = table.getAllByTestId("eval-recent-run").map((r) => within(r));
    expect(first!.getByText("Security Reviewer")).toBeInTheDocument();
    expect(first!.getByText("2026-05-29 09:14")).toBeInTheDocument();
    expect(first!.getByText("v7")).toBeInTheDocument();
    for (const value of ["82%", "91%", "95%", "17/20"]) expect(first!.getByText(value)).toBeInTheDocument();
    const fills = first!
      .getAllByRole("cell")
      .map((cell) => cell.querySelector<HTMLElement>("[aria-hidden] > div"))
      .filter((fill): fill is HTMLElement => fill !== null);
    expect(fills).toHaveLength(3);
    expect(fills[0]).toHaveStyle({ width: "82%", background: METRIC_COLOR.recall });
    expect(fills[1]).toHaveStyle({ width: "91%", background: METRIC_COLOR.precision });
    expect(fills[2]).toHaveStyle({ width: "95%", background: METRIC_COLOR.citation_accuracy });

    expect(second!.getAllByText("—")).toHaveLength(3);
    expect(second!.getAllByRole("cell").some((cell) => cell.querySelector("[aria-hidden] > div"))).toBe(false);
    expect(second!.getByText("Failed")).toBeInTheDocument();
    expect(screen.queryByText("mock/reviewer")).not.toBeInTheDocument();
    expect(screen.queryByText(/\$/)).not.toBeInTheDocument();
  });
});

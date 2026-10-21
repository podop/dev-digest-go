import { describe, it, expect, afterEach } from "vitest";
import { renderWithProviders, screen, cleanup, within, pseudoMessages } from "@/test/render";
import { strayText } from "@/test/pseudo";
import { makeRun } from "@/test/eval-fixtures";
import { TrendChart } from "./TrendChart";

afterEach(cleanup);

const at = (day: number) => new Date(2026, 4, day, 9, 14).toISOString();
const run = (version: number, over: Parameters<typeof makeRun>[0] = {}) =>
  makeRun({ id: `r${version}`, agent_version: version, started_at: at(20 + version), ...over });

const five = [run(3), run(4), run(5), run(6), run(7, { recall: 0.82, precision: 0.91, citation_accuracy: 0.95, cost_usd: 0.23 })];
const series = (name: string) => document.querySelectorAll(`polyline[data-series="${name}"]`);

describe("TrendChart", () => {
  it("shows the title, the legend, axis labels 1.0 to 0.6 and one dot per series — the newest", () => {
    renderWithProviders(<TrendChart runs={five} period="30d" />);
    expect(screen.getByRole("heading", { name: "METRIC TREND" })).toBeInTheDocument();
    for (const name of ["Recall", "Precision", "Citation"]) expect(screen.getByText(name)).toBeInTheDocument();
    for (const tick of ["1.0", "0.9", "0.8", "0.7", "0.6"]) expect(screen.getByText(tick)).toBeInTheDocument();
    expect(screen.queryByText("0.5")).not.toBeInTheDocument();
    const dots = screen.getAllByTestId("trend-dot");
    expect(dots.map((d) => d.getAttribute("data-series"))).toEqual(["recall", "precision", "citation_accuracy"]);
    expect(series("recall")).toHaveLength(1);
  });

  it("moves the axis bottom below a value under 0.6 so no point is clipped", () => {
    renderWithProviders(<TrendChart runs={[run(1, { recall: 0.55 }), run(2)]} period="30d" />);
    expect(screen.getByText("0.5")).toBeInTheDocument();
    expect(screen.queryByText("0.4")).not.toBeInTheDocument();
  });

  it("keyboard focus shows a tooltip with version, date, metrics and cost; arrows move, Escape hides", async () => {
    const { user } = renderWithProviders(<TrendChart runs={five} period="30d" />);
    expect(screen.queryByRole("tooltip")).not.toBeInTheDocument();

    await user.tab(); // the newest run is the one tab stop
    const tooltip = screen.getByRole("tooltip");
    for (const text of ["v7", "2026-05-27 09:14", "82%", "91%", "95%", "$0.23"]) expect(within(tooltip).getByText(text)).toBeInTheDocument();
    expect(screen.getAllByRole("button")[4]).toHaveAccessibleDescription(/v7/);
    expect(screen.getAllByRole("button")[4]).toHaveAccessibleName(/^v7, 2026-05-27 09:14, Recall 82%, Precision 91%, Citation 95%, Cost \$0\.23$/);
    expect(screen.getAllByTestId("trend-dot")).toHaveLength(3); // the newest run's dots are the resting ones

    await user.keyboard("{ArrowLeft}");
    expect(within(screen.getByRole("tooltip")).getByText("v6")).toBeInTheDocument();
    expect(screen.getAllByTestId("trend-dot")).toHaveLength(6);
    await user.keyboard("{Home}");
    expect(within(screen.getByRole("tooltip")).getByText("v3")).toBeInTheDocument();
    await user.keyboard("{End}");
    expect(within(screen.getByRole("tooltip")).getByText("v7")).toBeInTheDocument();
    await user.keyboard("{Escape}");
    expect(screen.queryByRole("tooltip")).not.toBeInTheDocument();
    await user.tab();
    expect(screen.queryByRole("tooltip")).not.toBeInTheDocument();
  });

  it('shows "—" for an unknown cost', async () => {
    const { user } = renderWithProviders(<TrendChart runs={[run(7, { cost_usd: null })]} period="30d" />);
    await user.tab();
    expect(within(screen.getByRole("tooltip")).getByText("—")).toBeInTheDocument();
    expect(screen.getByRole("button")).toHaveAccessibleName(/Cost —$/);
  });

  it("leaves a gap for a failed run with null metrics and says Failed in its tooltip", async () => {
    const failed = run(5, { status: "failed", recall: null, precision: null, citation_accuracy: null, cost_usd: null });
    const { user } = renderWithProviders(<TrendChart runs={[run(3), run(4), failed, run(6), run(7)]} period="30d" />);
    for (const name of ["recall", "precision", "citation_accuracy"]) expect(series(name)).toHaveLength(2);

    await user.tab();
    await user.keyboard("{ArrowLeft}{ArrowLeft}");
    const tooltip = screen.getByRole("tooltip");
    expect(within(tooltip).getByText("Failed")).toBeInTheDocument();
    expect(within(tooltip).getAllByText("—")).toHaveLength(4); // three metrics and the cost
    expect(screen.getAllByTestId("trend-dot")).toHaveLength(3); // no point to mark at the failed run
  });

  it("draws a lone run as one dot per series and no line", () => {
    renderWithProviders(<TrendChart runs={[run(1)]} period="30d" />);
    expect(screen.getAllByTestId("trend-dot")).toHaveLength(3);
    expect(document.querySelectorAll("polyline[data-series]")).toHaveLength(0);
  });

  it("says no finished runs in the period, naming it", () => {
    const { rerender } = renderWithProviders(<TrendChart runs={[]} period="7d" />);
    expect(screen.getByText("No finished runs in the last 7 days")).toBeInTheDocument();
    rerender(<TrendChart runs={[]} period="all" />);
    expect(screen.getByText("No finished runs yet")).toBeInTheDocument();
  });

  it("renders only message-file copy", async () => {
    const { container, user } = renderWithProviders(<TrendChart runs={five} period="30d" />, { messages: pseudoMessages() });
    await user.tab();
    expect(strayText(container, [/^\d\.\d$/, /^\d+%$/, /^v\d+$/, /^\d{4}-\d\d-\d\d \d\d:\d\d$/, /^\$[\d.]+$/])).toEqual([]);
  });
});

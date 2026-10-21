import { describe, it, expect, afterEach } from "vitest";
import { renderWithProviders, screen, cleanup, within, pseudoMessages } from "@/test/render";
import { strayText } from "@/test/pseudo";
import { makeRun } from "@/test/eval-fixtures";
import { AgentMetricTiles } from "./AgentMetricTiles";

afterEach(cleanup);

const tile = (name: string) => within(screen.getByRole("group", { name }));

describe("AgentMetricTiles", () => {
  it("shows exactly three tiles with whole percents and arrow + two-decimal ratio deltas", () => {
    const previous = makeRun({ id: "p", recall: 0.78, precision: 0.93, citation_accuracy: 0.95 });
    const latest = makeRun({ recall: 0.82, precision: 0.91, citation_accuracy: 0.95 });
    renderWithProviders(<AgentMetricTiles latest={latest} previous={previous} history={[previous, latest]} />);

    expect(screen.getAllByRole("group").map((g) => g.getAttribute("aria-label"))).toEqual(["RECALL", "PRECISION", "CITATION ACCURACY"]);
    expect(tile("RECALL").getByText("↑ 0.04")).toBeInTheDocument();
    expect(screen.getByRole("group", { name: "RECALL" })).toHaveTextContent("82%");
    expect(tile("PRECISION").getByText("↓ 0.02")).toBeInTheDocument();
    expect(screen.getByRole("group", { name: "PRECISION" })).toHaveTextContent("91%");
    expect(tile("CITATION ACCURACY").getByText("→ 0.00")).toBeInTheDocument();
    expect(screen.queryByText(/cases passed/i)).not.toBeInTheDocument();
  });

  it("draws a sparkline per series from two points on, and none for one run", () => {
    const a = makeRun({ id: "a" });
    const b = makeRun({ id: "b", precision: null });
    const { container, rerender } = renderWithProviders(<AgentMetricTiles latest={b} previous={a} history={[a, b]} />);
    expect(container.querySelectorAll("svg")).toHaveLength(2); // precision has one defined value
    rerender(<AgentMetricTiles latest={a} previous={null} history={[a]} />);
    expect(container.querySelectorAll("svg")).toHaveLength(0);
  });

  it('renders "—" and no delta for an undefined metric, and only message-file copy', () => {
    const latest = makeRun({ recall: null });
    const { container } = renderWithProviders(<AgentMetricTiles latest={latest} previous={makeRun({ id: "p" })} history={[latest]} />, {
      messages: pseudoMessages(),
    });
    expect(strayText(container, [/^\d+$/])).toEqual([]);
    cleanup();
    renderWithProviders(<AgentMetricTiles latest={latest} previous={makeRun({ id: "p" })} history={[latest]} />);
    expect(tile("RECALL").getByText("—")).toBeInTheDocument();
    expect(tile("RECALL").queryByTitle("Change vs the previous run")).not.toBeInTheDocument();
  });
});

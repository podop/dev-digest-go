import { describe, it, expect, afterEach } from "vitest";
import { renderWithProviders, screen, cleanup, within, pseudoMessages } from "@/test/render";
import { makeRun } from "@/test/eval-fixtures";
import { strayText } from "@/test/pseudo";
import { MetricTiles } from "./MetricTiles";

afterEach(cleanup);

const tile = (name: string) => within(screen.getByRole("group", { name }));

describe("MetricTiles", () => {
  it("shows whole percents with signed deltas (arrow + sign, not colour alone) and the cases passed tile", () => {
    const latest = makeRun({ recall: 0.75, precision: 0.8, citation_accuracy: 0.9, cases_passed: 6, cases_total: 8 });
    const previous = makeRun({ id: "run0", recall: 0.7, precision: 0.82, citation_accuracy: 0.9, cases_passed: 5 });
    renderWithProviders(<MetricTiles latest={latest} previous={previous} showCasesPassed />);

    expect(tile("Recall").getByText("75%")).toBeInTheDocument();
    expect(tile("Recall").getByText("▲ +5pt")).toBeInTheDocument();
    expect(tile("Precision").getByText("80%")).toBeInTheDocument();
    expect(tile("Precision").getByText("▼ −2pt")).toBeInTheDocument();
    expect(tile("Citation accuracy").getByText("90%")).toBeInTheDocument();
    expect(tile("Citation accuracy").getByText("■ 0pt")).toBeInTheDocument();
    expect(tile("Cases passed").getByText("6 / 8")).toBeInTheDocument();
    expect(tile("Cases passed").getByText("▲ +1")).toBeInTheDocument();
  });

  it('renders "—" and no delta when a metric is undefined, and nothing to compare without a previous run', () => {
    renderWithProviders(<MetricTiles latest={makeRun({ recall: null })} previous={null} />);
    expect(tile("Recall").getByText("—")).toBeInTheDocument();
    expect(tile("Precision").getByText("80%")).toBeInTheDocument();
    expect(screen.queryByText(/pt$/)).not.toBeInTheDocument();
    expect(screen.queryByRole("group", { name: "Cases passed" })).not.toBeInTheDocument();
  });

  it("takes every visible string from the message files", () => {
    const { container } = renderWithProviders(
      <MetricTiles latest={makeRun()} previous={makeRun({ recall: 0.5 })} history={[makeRun(), makeRun()]} showCasesPassed />,
      { messages: pseudoMessages() },
    );
    expect(strayText(container)).toEqual([]);
  });
});

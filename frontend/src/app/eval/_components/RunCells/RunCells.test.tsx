import { describe, it, expect, afterEach } from "vitest";
import { renderWithProviders, screen, cleanup, within, pseudoMessages } from "@/test/render";
import { strayText } from "@/test/pseudo";
import { makeRun } from "@/test/eval-fixtures";
import { METRIC_COLOR } from "../../constants";
import { MetricBarCell, PassCell } from "./RunCells";

afterEach(cleanup);

function inRow(...cells: React.ReactNode[]) {
  return (
    <table>
      <tbody>
        <tr>{cells}</tr>
      </tbody>
    </table>
  );
}

describe("MetricBarCell", () => {
  it("shows a bar filled to the value in the series colour with the whole percent; null is an empty bar and a dash", () => {
    const { container } = renderWithProviders(
      inRow(<MetricBarCell key="a" ratio={0.82} metric="recall" />, <MetricBarCell key="b" ratio={null} metric="precision" />),
    );
    const [recall, precision] = screen.getAllByRole("cell");
    expect(within(recall!).getByText("82%")).toBeInTheDocument();
    const fill = recall!.querySelector<HTMLElement>("[aria-hidden] > div");
    expect(fill).toHaveStyle({ width: "82%", background: METRIC_COLOR.recall });
    expect(within(precision!).getByText("—")).toBeInTheDocument();
    expect(precision!.querySelector("[aria-hidden] > div")).toBeNull();
    expect(strayText(container, [/^\d+%$/, "—"])).toEqual([]);
  });
});

describe("PassCell", () => {
  it("renders 17/20 for a finished run, Running x / N while running and Failed for a failed one", () => {
    renderWithProviders(
      inRow(
        <PassCell key="a" run={makeRun({ status: "completed", cases_passed: 17, cases_total: 20 })} />,
        <PassCell key="b" run={makeRun({ status: "running", cases_finished: 3, cases_total: 20 })} />,
        <PassCell key="c" run={makeRun({ status: "failed", error: "model unavailable" })} />,
      ),
    );
    const [done, running, failed] = screen.getAllByRole("cell");
    expect(done).toHaveTextContent("17/20");
    expect(running).toHaveTextContent("Running 3 / 20");
    expect(failed).toHaveTextContent("Failed");
    expect(failed).toHaveAttribute("title", "model unavailable");
    expect(failed).toHaveStyle({ color: "var(--crit)" });
  });

  it("takes every string from the message bundle", () => {
    const { container } = renderWithProviders(inRow(<PassCell key="a" run={makeRun({ status: "failed" })} />), { messages: pseudoMessages() });
    expect(strayText(container)).toEqual([]);
  });
});

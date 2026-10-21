import { describe, it, expect, afterEach } from "vitest";
import { renderWithProviders, screen, cleanup, within, pseudoMessages } from "@/test/render";
import { strayText } from "@/test/pseudo";
import { makeRun } from "@/test/eval-fixtures";
import { RegressionBanner } from "./RegressionBanner";

afterEach(cleanup);

const previous = makeRun({ id: "p", agent_version: 6, recall: 0.78, precision: 0.93, citation_accuracy: 0.94 });
const precisionDip = { metrics: [{ metric: "precision" as const, delta: -0.02 }] };

describe("RegressionBanner", () => {
  it("reads one line: bold dip, version, then how the other metrics moved", () => {
    const latest = makeRun({ agent_version: 7, recall: 0.82, precision: 0.91, citation_accuracy: 0.97 });
    renderWithProviders(<RegressionBanner alert={precisionDip} latest={latest} previous={previous} />);
    const banner = screen.getByRole("alert");
    expect(banner).toHaveTextContent("Precision dipped 2pts on v7 — Recall and citation both up.");
    expect(within(banner).getByText("Precision dipped 2pts").tagName).toBe("STRONG");
  });

  it("joins several dipped metrics with 'and', and names a flat metric", () => {
    const latest = makeRun({ agent_version: 7, recall: 0.7, precision: 0.9, citation_accuracy: 0.94 });
    const alert = {
      metrics: [
        { metric: "recall" as const, delta: -0.08 },
        { metric: "precision" as const, delta: -0.03 },
      ],
    };
    renderWithProviders(<RegressionBanner alert={alert} latest={latest} previous={previous} />);
    expect(screen.getByRole("alert")).toHaveTextContent("Recall dipped 8pts and Precision dipped 3pts on v7 — Citation unchanged.");
  });

  it("says 'Recall up, citation unchanged.' for mixed moves", () => {
    const latest = makeRun({ agent_version: 7, recall: 0.82, precision: 0.9, citation_accuracy: 0.94 });
    const alert = { metrics: [{ metric: "precision" as const, delta: -0.03 }] };
    renderWithProviders(<RegressionBanner alert={alert} latest={latest} previous={previous} />);
    expect(screen.getByRole("alert")).toHaveTextContent("Precision dipped 3pts on v7 — Recall up, citation unchanged.");
  });

  it("omits the sentence when every metric dipped", () => {
    const allDipped = { metrics: (["recall", "precision", "citation_accuracy"] as const).map((metric) => ({ metric, delta: -0.05 })) };
    renderWithProviders(<RegressionBanner alert={allDipped} latest={makeRun({ agent_version: 7 })} previous={previous} />);
    expect(screen.getByRole("alert").textContent).toMatch(/on v7 —$/);
  });

  it("renders only message-file copy", () => {
    const { container } = renderWithProviders(<RegressionBanner alert={precisionDip} latest={makeRun()} previous={previous} />, { messages: pseudoMessages() });
    expect(strayText(container)).toEqual([]);
  });
});

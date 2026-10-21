/* CompareModal — runs A → B: metrics old → new with signed deltas, flipped cases, the
   prompt line diff, keyboard close, plain-text rendering and an in-modal error. */
import { describe, it, expect, afterEach, vi } from "vitest";
import { renderWithProviders, screen, cleanup, within } from "@/test/render";
import { mockFetch, jsonResponse } from "@/test/fetch-mock";
import { makeCompare } from "@/test/eval-fixtures";
import { CompareModal } from "./CompareModal";

afterEach(cleanup);

const HOSTILE = "<img src=x onerror=alert(1)>";

describe("CompareModal", () => {
  it("shows each metric old → new with a signed delta, the flipped case and the prompt line diff", async () => {
    const api = mockFetch({ "GET /eval-runs/compare": makeCompare() });
    renderWithProviders(<CompareModal aId="ra" bId="rb" onClose={() => {}} />);

    expect(await screen.findByText("Compare runs · v2 → v3")).toBeInTheDocument();
    expect(api.requests("GET", "/eval-runs/compare")[0]!.search).toBe("?a=ra&b=rb");

    const metrics = within(screen.getByRole("region", { name: "Metrics" }));
    const recall = within(metrics.getByRole("row", { name: /Recall/ }));
    expect(recall.getByText("50% → 75%")).toBeInTheDocument();
    expect(recall.getByText("▲ +25pt")).toBeInTheDocument();
    expect(within(metrics.getByRole("row", { name: /Precision/ })).getByText("▼ −5pt")).toBeInTheDocument();
    expect(within(metrics.getByRole("row", { name: /Citation accuracy/ })).getByText("■ 0pt")).toBeInTheDocument();
    const cost = within(metrics.getByRole("row", { name: /Cost/ }));
    expect(cost.getByText("$0.01 → $0.012")).toBeInTheDocument();
    expect(cost.getByText("▲ +$0.002")).toBeInTheDocument();

    expect(screen.getByText("1 case flipped")).toBeInTheDocument();
    const cases = screen.getAllByTestId("compare-case");
    expect(within(cases[0]!).getByText("Failed → Passed")).toBeInTheDocument();
    expect(within(cases[0]!).getByText("flipped")).toBeInTheDocument();
    expect(within(cases[1]!).queryByText("flipped")).not.toBeInTheDocument();
    expect(within(cases[2]!).getByText("Absent → Passed")).toBeInTheDocument();

    const diff = within(screen.getByTestId("prompt-diff"));
    expect(diff.getByText("Flag every issue.")).toBeInTheDocument();
    expect(diff.getByText("Flag only high-confidence issues.")).toBeInTheDocument();
    expect(diff.getByText("−")).toBeInTheDocument();
    expect(diff.getByText("+")).toBeInTheDocument();
    expect(screen.getByText("Model, strategy and skills unchanged")).toBeInTheDocument();
  });

  it("closes with Escape and with the Close button operated by keyboard", async () => {
    mockFetch({ "GET /eval-runs/compare": makeCompare() });
    const onClose = vi.fn();
    const { user } = renderWithProviders(<CompareModal aId="ra" bId="rb" onClose={onClose} />);
    await screen.findByText("Compare runs · v2 → v3");

    await user.keyboard("{Escape}");
    expect(onClose).toHaveBeenCalledTimes(1);

    screen.getByRole("button", { name: "Close compare" }).focus();
    await user.keyboard("{Enter}");
    expect(onClose).toHaveBeenCalledTimes(2);
  });

  it("renders prompt lines, case names, model and skill changes as literal text", async () => {
    const compare = makeCompare({ prompt: [{ op: "add", line: HOSTILE }] });
    compare.cases = [{ case_id: "c1", case_name: HOSTILE, a: "passed", b: "failed", flipped: true }];
    compare.config_diff.model = { a: "gpt-4.1", b: "claude" };
    compare.config_diff.skills_added = [HOSTILE];
    mockFetch({ "GET /eval-runs/compare": compare });
    renderWithProviders(<CompareModal aId="ra" bId="rb" onClose={() => {}} />);

    expect(await screen.findByText("Model: gpt-4.1 → claude")).toBeInTheDocument();
    expect(within(screen.getByTestId("prompt-diff")).getByText(HOSTILE)).toBeInTheDocument();
    expect(within(screen.getByTestId("compare-case")).getByText(HOSTILE)).toBeInTheDocument();
    expect(screen.getByText(`Skills added: ${HOSTILE}`)).toBeInTheDocument();
    expect(document.querySelector("img")).toBeNull();
  });

  it("says so when the prompt is unchanged", async () => {
    mockFetch({ "GET /eval-runs/compare": makeCompare({ prompt: [{ op: "same", line: "x" }] }) });
    renderWithProviders(<CompareModal aId="ra" bId="rb" onClose={() => {}} />);
    expect(await screen.findByText("System prompt unchanged")).toBeInTheDocument();
    expect(screen.queryByTestId("prompt-diff")).not.toBeInTheDocument();
  });

  it("shows an error with Retry inside the modal when the runs are not comparable", async () => {
    mockFetch({ "GET /eval-runs/compare": jsonResponse({ error: { code: "runs_not_comparable", message: "no" } }, 422) });
    renderWithProviders(<CompareModal aId="ra" bId="rb" onClose={() => {}} />);
    expect(await screen.findByText("Could not compare these runs")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Retry" })).toBeInTheDocument();
    expect(screen.getByRole("dialog")).toBeInTheDocument();
  });
});

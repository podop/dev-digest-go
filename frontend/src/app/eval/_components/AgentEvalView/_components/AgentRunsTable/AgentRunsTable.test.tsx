/* AgentRunsTable — headers, bars, the heading row with Compare, selection, and the
   loading / error / empty states. */
import { describe, it, expect, afterEach, vi } from "vitest";
import { renderWithProviders, screen, cleanup, within, pseudoMessages } from "@/test/render";
import { makeRun } from "@/test/eval-fixtures";
import { strayText } from "@/test/pseudo";
import { METRIC_COLOR } from "@/app/eval/constants";
import { AgentRunsTable } from "./AgentRunsTable";

afterEach(cleanup);

const DONE = makeRun({
  id: "r7",
  agent_version: 7,
  started_at: new Date(2026, 4, 29, 9, 14).toISOString(),
  recall: 0.82,
  precision: 0.91,
  citation_accuracy: 0.95,
  cases_total: 20,
  cases_passed: 17,
  cost_usd: 0.23,
});
const RUNNING = makeRun({ id: "r8", agent_version: 8, status: "running", finished_at: null, cases_finished: 3, recall: null, precision: null, citation_accuracy: null, cost_usd: null });

const selection = (selectedIds: string[] = [], onToggle = vi.fn()) => ({ selectedIds, onToggle });

describe("AgentRunsTable", () => {
  it("lists the columns, bars with percents, the stamp, version, pass and cost under a heading with the compare hint", async () => {
    const onToggle = vi.fn();
    const { user } = renderWithProviders(
      <AgentRunsTable runs={[RUNNING, DONE]} state="ready" onRetry={vi.fn()} selection={selection([], onToggle)} />,
    );

    expect(screen.getByRole("heading", { name: "RECENT RUNS" })).toBeInTheDocument();
    expect(screen.getByText("Select two runs to compare")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Compare" })).toBeDisabled();

    const headers = screen.getAllByRole("columnheader").map((h) => h.textContent);
    expect(headers).toEqual(["Select", "RAN AT", "VERSION", "RECALL", "PRECISION", "CITATION", "PASS", "COST"]);

    const row = within(screen.getByRole("row", { name: /2026-05-29 09:14/ }));
    expect(row.getByText("v7")).toBeInTheDocument();
    expect(row.getByText("82%")).toBeInTheDocument();
    expect(row.getByText("91%")).toBeInTheDocument();
    expect(row.getByText("95%")).toBeInTheDocument();
    expect(row.getByText("17/20")).toBeInTheDocument();
    expect(row.getByText("$0.23")).toBeInTheDocument();

    // Each metric cell draws its bar, filled to the value in the series colour.
    const bars = row
      .getAllByRole("cell")
      .map((cell) => cell.querySelector<HTMLElement>("[aria-hidden] > div"))
      .filter((fill): fill is HTMLElement => fill !== null);
    expect(bars).toHaveLength(3);
    expect(bars[0]).toHaveStyle({ width: "82%", background: METRIC_COLOR.recall });
    expect(bars[1]).toHaveStyle({ width: "91%", background: METRIC_COLOR.precision });
    expect(bars[2]).toHaveStyle({ width: "95%", background: METRIC_COLOR.citation_accuracy });

    // A running run is listed but cannot be selected; its cost is a dash (EC9).
    const running = screen.getByRole("checkbox", { name: /^Select run v8 / });
    expect(running).toBeDisabled();
    expect(within(screen.getAllByRole("row")[1]!).getByText("Running 3 / 8")).toBeInTheDocument();
    expect(within(screen.getAllByRole("row")[1]!).getAllByText("—").length).toBeGreaterThan(0);

    await user.click(screen.getByRole("checkbox", { name: "Select run v7 · 2026-05-29 09:14" }));
    expect(onToggle).toHaveBeenCalledWith("r7");
  });

  it("enables Compare when the caller passes onCompare", async () => {
    const onCompare = vi.fn();
    const { user } = renderWithProviders(
      <AgentRunsTable runs={[DONE]} state="ready" onRetry={vi.fn()} selection={selection(["r7"])} onCompare={onCompare} />,
    );
    expect(screen.getByRole("checkbox", { name: /^Select run v7/ })).toBeChecked();
    await user.click(screen.getByRole("button", { name: "Compare" }));
    expect(onCompare).toHaveBeenCalledOnce();
  });

  it("says 'No runs in this period' when empty, and keeps the heading on error with Retry", async () => {
    const onRetry = vi.fn();
    const { rerender, user } = renderWithProviders(
      <AgentRunsTable runs={[]} state="ready" onRetry={onRetry} selection={selection()} />,
    );
    expect(screen.getByText("No runs in this period")).toBeInTheDocument();
    expect(screen.queryByRole("table")).not.toBeInTheDocument();

    rerender(<AgentRunsTable runs={undefined} state="error" onRetry={onRetry} selection={selection()} />);
    expect(screen.getByRole("heading", { name: "RECENT RUNS" })).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Retry" }));
    expect(onRetry).toHaveBeenCalledOnce();
  });

  it("AC21: every visible string comes from the message files", () => {
    const { container } = renderWithProviders(
      <AgentRunsTable runs={[DONE, RUNNING]} state="ready" onRetry={vi.fn()} selection={selection()} />,
      { messages: pseudoMessages() },
    );
    expect(strayText(container, ["—", /^\$\d/, /^\d{4}-\d{2}-\d{2} \d{2}:\d{2}$/])).toEqual([]);
  });
});

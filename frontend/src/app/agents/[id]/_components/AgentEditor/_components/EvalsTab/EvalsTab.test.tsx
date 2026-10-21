/* Agent Editor → Evals tab: latest-run tiles with deltas, "n / m passing", case rows,
   Run all evals with polling progress, case edit / delete, plain-text rendering and
   message-file copy. fetch is stubbed; the real hooks run. */
import { describe, it, expect, afterEach, vi } from "vitest";
import type { AgentEvalCase, EvalAgentDashboard, EvalRunSummary } from "@devdigest/shared";
import { renderWithProviders, screen, cleanup, waitFor, within, act, pseudoMessages } from "@/test/render";
import { mockFetch, jsonResponse } from "@/test/fetch-mock";
import { AGENT } from "@/test/skill-fixtures";
import { makeCase, makeRun } from "@/test/eval-fixtures";
import { strayText } from "@/test/pseudo";
import { EvalsTab } from "./EvalsTab";

afterEach(() => {
  vi.useRealTimers();
  cleanup();
});

const HOSTILE = "<img src=x onerror=alert(1)>";

const dashboard = (over: Partial<EvalAgentDashboard> = {}): EvalAgentDashboard => ({
  agent_id: "ag1",
  cases_total: 8,
  latest: null,
  previous: null,
  trend: [],
  alert: null,
  ...over,
});

/** A one-hunk diff whose new side covers lines 10–12 of `file`, so the case passes the editor's checks. */
const diffFor = (file: string) => `diff --git a/${file} b/${file}\n--- a/${file}\n+++ b/${file}\n@@ -10,1 +10,3 @@\n ctx\n+one\n+two\n`;

/** 8 cases of one finished run: 5 passed, 1 failed, 1 error, 1 never run. */
function eightCases(): AgentEvalCase[] {
  const status = ["passed", "passed", "passed", "passed", "passed", "failed", "error", null] as const;
  return status.map((st, i) =>
    makeCase({
      id: `c${i}`,
      name: i === 7 ? HOSTILE : `case-${i}`,
      expectation: { ...makeCase().expectation, kind: i % 2 ? "must_not_flag" : "must_find", file: `src/f${i}.ts` },
      input_diff: diffFor(`src/f${i}.ts`),
      last_result: st ? { run_id: "r2", status: st, ran_at: "2026-10-14T10:01:00.000Z" } : null,
    }),
  );
}

const LATEST = makeRun({ id: "r2", recall: 0.75, precision: 0.8, citation_accuracy: 0.9, cases_passed: 5 });
const PREVIOUS = makeRun({ id: "r1", agent_version: 2, recall: 0.7, precision: 0.82, citation_accuracy: 0.9, cases_passed: 4 });

function stubFinishedAgent() {
  return mockFetch({
    "GET /agents/ag1/eval-cases": { cases: eightCases() },
    "GET /agents/ag1/eval/dashboard": dashboard({ latest: LATEST, previous: PREVIOUS }),
    "GET /agents/ag1/eval-runs": { runs: [LATEST, PREVIOUS] },
  });
}

describe("EvalsTab", () => {
  it("shows metric tiles with deltas, 'n / 8 passing', case rows with status + expectation, and the history; case text is literal", async () => {
    stubFinishedAgent();
    const { container } = renderWithProviders(<EvalsTab agent={AGENT} />);

    expect(await screen.findByText("5 / 8 passing")).toBeInTheDocument();
    const tiles = (name: string) => within(screen.getByRole("group", { name }));
    expect(tiles("Recall").getByText("75%")).toBeInTheDocument();
    expect(tiles("Recall").getByText("▲ +5pt")).toBeInTheDocument();
    expect(tiles("Precision").getByText("▼ −2pt")).toBeInTheDocument();
    expect(tiles("Cases passed").getByText("5 / 8")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "View full dashboard →" })).toHaveAttribute("href", "/eval?agent=ag1");

    const rows = screen.getAllByTestId("eval-case-row");
    expect(rows).toHaveLength(8);
    expect(within(rows[0]!).getByText("passed")).toBeInTheDocument();
    expect(within(rows[0]!).getByText(/must find/)).toBeInTheDocument();
    expect(within(rows[0]!).getByText("src/f0.ts:10–12")).toBeInTheDocument();
    expect(within(rows[1]!).getByText(/must not flag/)).toBeInTheDocument();
    expect(within(rows[5]!).getByText("failed")).toBeInTheDocument();
    expect(within(rows[6]!).getByText("error")).toBeInTheDocument();
    expect(within(rows[7]!).getByText("never run")).toBeInTheDocument();
    expect(within(rows[0]!).getByText("critical · security")).toBeInTheDocument();
    // AC32: hostile case name is text, not an element.
    expect(within(rows[7]!).getByRole("button", { name: HOSTILE })).toBeInTheDocument();
    expect(container.querySelector("img")).toBeNull();
    expect(screen.getByRole("table", { name: "Eval runs" })).toBeInTheDocument();
  });

  it("shows empty states: no runs yet and no cases (Run all disabled with a reason)", async () => {
    mockFetch({
      "GET /agents/ag1/eval-cases": { cases: [] },
      "GET /agents/ag1/eval/dashboard": dashboard({ cases_total: 0 }),
      "GET /agents/ag1/eval-runs": { runs: [] },
    });
    renderWithProviders(<EvalsTab agent={AGENT} />);
    expect(await screen.findByText(/No eval cases yet/)).toBeInTheDocument();
    expect(screen.getByText(/No runs yet/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Run all evals" })).toBeDisabled();
    expect(screen.getByTitle("Create an eval case first")).toBeInTheDocument();
  });

  it("shows an error with Retry when the cases cannot be loaded", async () => {
    mockFetch({
      "GET /agents/ag1/eval-cases": jsonResponse({ error: { code: "internal", message: "boom" } }, 500),
      "GET /agents/ag1/eval/dashboard": dashboard(),
      "GET /agents/ag1/eval-runs": { runs: [] },
    });
    renderWithProviders(<EvalsTab agent={AGENT} />);
    expect(await screen.findByText("Could not load the eval cases")).toBeInTheDocument();
    expect(screen.getAllByRole("button", { name: "Retry" }).length).toBeGreaterThan(0);
  });

  it("Run all evals: shows 'Running x / 8', stays disabled until the run ends, then refreshes tiles and rows", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const running = makeRun({ id: "r9", status: "running", finished_at: null, cases_finished: 0, cases_passed: 0, recall: null, precision: null, citation_accuracy: null });
    const done = makeRun({ id: "r9", recall: 0.5, precision: 1, citation_accuracy: 1, cases_passed: 4 });
    const state: { phase: "idle" | "running" | "done"; finished: number } = { phase: "idle", finished: 0 };
    const caseRows = (withResult: boolean) =>
      eightCases().map((c, i) => ({ ...c, last_result: withResult ? { run_id: "r9", status: i < 4 ? ("passed" as const) : ("failed" as const), ran_at: "2026-10-14T10:05:00.000Z" } : null }));
    const current = (): EvalRunSummary | null => (state.phase === "idle" ? null : state.phase === "running" ? { ...running, cases_finished: state.finished } : done);
    const api = mockFetch({
      "GET /agents/ag1/eval-cases": () => ({ cases: caseRows(state.phase === "done") }),
      "GET /agents/ag1/eval/dashboard": () => dashboard({ latest: state.phase === "done" ? done : null }),
      "GET /agents/ag1/eval-runs": () => ({ runs: current() ? [current()] : [] }),
      "GET /eval-runs/r9": () => ({ run: current(), results: [] }),
      "POST /agents/ag1/eval-runs": () => {
        state.phase = "running";
        return jsonResponse(running, 202);
      },
    });
    const { user } = renderWithProviders(<EvalsTab agent={AGENT} />);

    expect(await screen.findByText("0 / 8 passing")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Run all evals" }));
    const progress = await screen.findByRole("button", { name: "Running 0 / 8" });
    expect(progress).toBeDisabled();
    expect(api.requests("POST", "/agents/ag1/eval-runs")).toHaveLength(1);

    state.finished = 3;
    await act(() => vi.advanceTimersByTimeAsync(2000));
    expect(await screen.findByRole("button", { name: "Running 3 / 8" })).toBeDisabled();

    state.phase = "done";
    await act(() => vi.advanceTimersByTimeAsync(2000));
    expect(await screen.findByRole("button", { name: "Run all evals" })).toBeEnabled();
    expect(await screen.findByText("4 / 8 passing")).toBeInTheDocument();
    await waitFor(() => expect(within(screen.getByRole("group", { name: "Recall" })).getByText("50%")).toBeInTheDocument());
  });

  it("edits a case name in the modal and deletes a case after confirming", async () => {
    let cases = eightCases();
    const api = mockFetch({
      "GET /agents/ag1/eval-cases": () => ({ cases }),
      "GET /agents/ag1/eval/dashboard": dashboard({ latest: LATEST, previous: PREVIOUS }),
      "GET /agents/ag1/eval-runs": { runs: [LATEST] },
      "PATCH /eval-cases/c0": (req) => {
        cases = cases.map((c) => (c.id === "c0" ? { ...c, ...(req.body as { name: string }) } : c));
        return cases[0];
      },
      "DELETE /eval-cases/c1": () => {
        cases = cases.filter((c) => c.id !== "c1");
        return jsonResponse(null, 204);
      },
    });
    const { user } = renderWithProviders(<EvalsTab agent={AGENT} />);
    await screen.findByText("5 / 8 passing");

    // edit
    await user.click(screen.getByRole("button", { name: "Open case case-0" }));
    const dialog = screen.getByRole("dialog");
    const name = within(dialog).getByLabelText("Name");
    await user.clear(name);
    await user.type(name, "renamed-case");
    await user.click(within(dialog).getByRole("button", { name: "Save" }));
    expect(await screen.findByRole("button", { name: "renamed-case" })).toBeInTheDocument();
    expect(api.requests("PATCH", "/eval-cases/c0")[0]?.body).toEqual({ name: "renamed-case" });
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());

    // delete, confirm as a sibling dialog
    await user.click(screen.getByRole("button", { name: "Delete case case-1" }));
    const confirm = screen.getByRole("dialog");
    expect(within(confirm).getByText("Delete eval case?")).toBeInTheDocument();
    await user.click(within(confirm).getByRole("button", { name: "Cancel" }));
    expect(screen.getAllByTestId("eval-case-row")).toHaveLength(8);

    await user.click(screen.getByRole("button", { name: "Delete case case-1" }));
    await user.click(within(screen.getByRole("dialog")).getByRole("button", { name: "Delete" }));
    await waitFor(() => expect(screen.getAllByTestId("eval-case-row")).toHaveLength(7));
    expect(screen.queryByRole("button", { name: "case-1" })).not.toBeInTheDocument();
    expect(api.requests("DELETE", "/eval-cases/c1")).toHaveLength(1);
  });

  it("takes every visible string from the message files", async () => {
    stubFinishedAgent();
    const { container } = renderWithProviders(<EvalsTab agent={AGENT} />, { messages: pseudoMessages() });
    await waitFor(() => expect(screen.getAllByTestId("eval-case-row")).toHaveLength(8));
    await waitFor(() => expect(screen.getByRole("table")).toBeInTheDocument());
    // data: case names, severity · category chip, dates, model, cost
    expect(strayText(container, [/^case-\d$/, HOSTILE, "critical · security", /\d{1,4}[/.\-]\d{1,2}[/.\-]\d{1,4}/, "mock/reviewer", /^[—$<]/])).toEqual([]);
  });
});

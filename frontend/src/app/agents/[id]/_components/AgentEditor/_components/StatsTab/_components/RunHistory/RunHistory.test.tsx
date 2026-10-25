import { describe, it, expect, afterEach } from "vitest";
import type { RunHistoryItem } from "@devdigest/shared";
import { renderWithProviders, screen, cleanup, within, pseudoMessages } from "@/test/render";
import { strayText } from "@/test/pseudo";
import { RunHistory } from "./RunHistory";

afterEach(cleanup);

function makeRun(id: string, source: RunHistoryItem["source"], extra: Partial<RunHistoryItem> = {}): RunHistoryItem {
  return {
    run_id: id,
    ran_at: "2026-10-20T10:00:00.000Z",
    status: "done",
    source,
    pr: { id: `pr-${id}`, repo_id: "repo-1", number: 7 },
    tokens: 1200,
    cost_usd: 0.0123,
    findings_count: 3,
    has_trace: source !== "ci",
    ...extra,
  };
}

const RUNS = [makeRun("r1", "local"), makeRun("r2", "ci"), makeRun("r3", "auto")];

describe("RunHistory", () => {
  it("labels each run with its source: local, CI and Auto", () => {
    renderWithProviders(<RunHistory runs={RUNS} agentName="Reviewer" />);
    const [, local, ci, auto] = screen.getAllByRole("row");
    expect(within(local!).getByText("local")).toBeInTheDocument();
    expect(within(ci!).getByText("CI")).toBeInTheDocument();
    expect(within(auto!).getByText("Auto")).toBeInTheDocument();
    expect(within(auto!).getByRole("button", { name: "View trace" })).toBeInTheDocument();
    expect(within(ci!).queryByRole("button")).not.toBeInTheDocument();
  });

  it("keeps the failed status next to an auto run and has no hard-coded copy", () => {
    const runs = [...RUNS, makeRun("r4", "auto", { status: "failed", cost_usd: null })];
    const { container } = renderWithProviders(<RunHistory runs={runs} agentName="Reviewer" />, {
      messages: pseudoMessages(),
    });
    expect(screen.getAllByText("⟦agentPerformance.statsTab.history.source.auto⟧")).toHaveLength(2);
    expect(screen.getByText("⟦agentPerformance.statsTab.history.status.failed⟧")).toBeInTheDocument();
    // data: the time, the PR "#" and number, tokens, cost ("—" / "$…") and the findings count
    expect(strayText(container, [/\d{1,2}[:.]\d{2}/, "#", /^\d+$/, /^[\d.,\sk]+$/, /^[—$<]/])).toEqual([]);
  });
});

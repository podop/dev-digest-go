/* Skill Evals tab — tiles with deltas, "Run with" default and pick, Run all with polling progress,
   its disabled reasons, history with skill version + host agent, case creation, plain-text and
   message-file copy. fetch is stubbed; the real hooks run. */
import { describe, it, expect, afterEach, vi } from "vitest";
import type { EvalRunSummary } from "@devdigest/shared";
import { renderWithProviders, screen, cleanup, waitFor, within, act, pseudoMessages } from "@/test/render";
import { mockFetch, jsonResponse } from "@/test/fetch-mock";
import { pickOption } from "@/test/select";
import { AGENT, makeSkill } from "@/test/skill-fixtures";
import { makeCase, makeRun } from "@/test/eval-fixtures";
import { strayText } from "@/test/pseudo";
import { EvalsTab } from "./EvalsTab";

afterEach(() => {
  vi.useRealTimers();
  cleanup();
});

const HOSTILE = "<img src=x onerror=alert(1)>";
const SKILL = makeSkill({ id: "sk1", name: HOSTILE, version: 3 });
const AGENT_A = { ...AGENT, id: "agA", name: "Alpha Reviewer" };
const AGENT_B = { ...AGENT, id: "agB", name: "Beta Reviewer" };
const DIFF = ["diff --git a/src/a.ts b/src/a.ts", "--- a/src/a.ts", "+++ b/src/a.ts", "@@ -1,1 +1,3 @@", " x", "+y", "+z", ""].join("\n");

const skillCase = (over = {}) =>
  makeCase({ id: "c1", name: "skill-case", owner_kind: "skill", owner_id: "sk1", agent_id: null, origin: "manual", source_finding_id: null, source_pr_id: null, ...over });

const skillRun = (over: Partial<EvalRunSummary> = {}) =>
  makeRun({ owner_kind: "skill", skill_id: "sk1", skill_version: 3, agent_id: "agB", agent_name: "Beta Reviewer", agent_version: 7, ...over });

const LATEST = skillRun({ id: "r2", recall: 0.75, precision: 0.8, citation_accuracy: 0.9, cases_passed: 5, cases_total: 6 });
const PREVIOUS = skillRun({ id: "r1", skill_version: 2, recall: 0.7, precision: 0.82, citation_accuracy: 0.9, cases_passed: 4, cases_total: 6 });

function stubBase(over: Record<string, unknown> = {}) {
  return mockFetch({
    "GET /skills/sk1/eval-cases": { cases: [skillCase({ last_result: { run_id: "r2", status: "passed", ran_at: "2026-10-14T10:01:00.000Z" } })] },
    "GET /skills/sk1/eval-runs": { runs: [LATEST, PREVIOUS] },
    "GET /skills/sk1/agents": [{ id: "agB", name: "Beta Reviewer", enabled: true }],
    "GET /agents": [AGENT_A, AGENT_B],
    ...over,
  } as Parameters<typeof mockFetch>[0]);
}

describe("skill EvalsTab", () => {
  it("shows tiles with deltas, preselects the agent that uses the skill, and lists runs with skill version and host agent; text is literal", async () => {
    stubBase();
    const { container } = renderWithProviders(<EvalsTab skill={SKILL} />);

    expect(await screen.findByText("1 / 1 passing")).toBeInTheDocument();
    const tile = (name: string) => within(screen.getByRole("group", { name }));
    expect(tile("Recall").getByText("75%")).toBeInTheDocument();
    expect(tile("Recall").getByText("▲ +5pt")).toBeInTheDocument();
    expect(tile("Cases passed").getByText("5 / 6")).toBeInTheDocument();
    expect(await screen.findByRole("combobox", { name: "Run with" })).toHaveTextContent("Beta Reviewer");

    const table = await screen.findByRole("table", { name: "Eval runs" });
    expect(within(table).getAllByText("Beta Reviewer")).toHaveLength(2);
    expect(within(table).getByText("v3")).toBeInTheDocument();
    expect(within(table).getByText("v2")).toBeInTheDocument();
    expect(within(table).getByRole("columnheader", { name: "Agent" })).toBeInTheDocument();
    // the hostile skill name is never rendered as markup
    expect(container.querySelector("img")).toBeNull();
  });

  it("Run all evals posts the picked host agent, shows 'Running x / N', stays disabled, then refreshes tiles", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const running = skillRun({ id: "r9", agent_id: "agA", agent_name: "Alpha Reviewer", status: "running", finished_at: null, cases_total: 1, cases_finished: 0, cases_passed: 0, recall: null, precision: null, citation_accuracy: null });
    const done = skillRun({ id: "r9", agent_id: "agA", agent_name: "Alpha Reviewer", recall: 0.5, cases_total: 1, cases_passed: 1 });
    const state: { phase: "idle" | "running" | "done" } = { phase: "idle" };
    const current = () => (state.phase === "running" ? running : done);
    const api = stubBase({
      "GET /skills/sk1/eval-runs": () => ({ runs: state.phase === "idle" ? [] : [current()] }),
      "GET /eval-runs/r9": () => ({ run: current(), results: [] }),
      "POST /skills/sk1/eval-runs": () => {
        state.phase = "running";
        return jsonResponse(running, 202);
      },
    });
    const { user } = renderWithProviders(<EvalsTab skill={SKILL} />);

    expect(await screen.findAllByText(/No runs yet/)).toHaveLength(2);
    await waitFor(() => expect(screen.getByRole("combobox", { name: "Run with" })).toHaveTextContent("Beta Reviewer"));
    await pickOption(user, screen.getByRole("combobox", { name: "Run with" }), "Alpha Reviewer");
    await user.click(screen.getByRole("button", { name: "Run all evals" }));

    expect(await screen.findByRole("button", { name: "Running 0 / 1" })).toBeDisabled();
    expect(api.requests("POST", "/skills/sk1/eval-runs")[0]?.body).toEqual({ agent_id: "agA" });

    state.phase = "done";
    await act(() => vi.advanceTimersByTimeAsync(2000));
    expect(await screen.findByRole("button", { name: "Run all evals" })).toBeEnabled();
    await waitFor(() => expect(within(screen.getByRole("group", { name: "Recall" })).getByText("50%")).toBeInTheDocument());
  });

  it("disables Run all evals with a reason: no cases, then no agents", async () => {
    stubBase({ "GET /skills/sk1/eval-cases": { cases: [] }, "GET /skills/sk1/eval-runs": { runs: [] } });
    renderWithProviders(<EvalsTab skill={SKILL} />);
    expect(await screen.findByText(/No eval cases yet/)).toBeInTheDocument();
    await waitFor(() => expect(screen.getByTitle("Add an eval case first")).toBeInTheDocument());
    expect(screen.getByRole("button", { name: "Run all evals" })).toBeDisabled();
    cleanup();

    stubBase({ "GET /agents": [], "GET /skills/sk1/agents": [] });
    renderWithProviders(<EvalsTab skill={SKILL} />);
    expect(await screen.findByTitle("Create an agent first")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Run all evals" })).toBeDisabled();
    expect(screen.getByRole("combobox", { name: "Run with" })).toHaveAttribute("aria-disabled", "true");
  });

  it("a new case saved on the skill tab is listed first as 'never run' without the manual tag", async () => {
    let cases = [] as ReturnType<typeof skillCase>[];
    const api = stubBase({
      "GET /skills/sk1/eval-cases": () => ({ cases }),
      "GET /skills/sk1/eval-runs": { runs: [] },
      "POST /skills/sk1/eval-cases": () => {
        const created = skillCase({ id: "c9", name: "hand-made", last_result: null });
        cases = [created];
        return jsonResponse(created, 201);
      },
    });
    const { user } = renderWithProviders(<EvalsTab skill={SKILL} />);
    await user.click(await screen.findByRole("button", { name: "New eval case" }));
    const dialog = screen.getByRole("dialog");
    await user.type(within(dialog).getByLabelText("Name"), "hand-made");
    await user.click(within(dialog).getByLabelText("Pasted unified diff"));
    await user.paste(DIFF);
    await user.type(within(dialog).getByLabelText("Start line"), "1");
    await user.type(within(dialog).getByLabelText("End line"), "2");
    await waitFor(() => expect(within(dialog).getByRole("button", { name: "Save" })).toBeEnabled());
    await user.click(within(dialog).getByRole("button", { name: "Save" }));

    const row = await screen.findByTestId("eval-case-row");
    expect(within(row).getByText("hand-made")).toBeInTheDocument();
    expect(within(row).getByText("never run")).toBeInTheDocument();
    expect(within(row).queryByText("manual")).not.toBeInTheDocument();
    expect(api.requests("POST", "/skills/sk1/eval-cases")).toHaveLength(1);
  }, 20_000);

  it("takes every visible string from the message files", async () => {
    stubBase();
    const { container } = renderWithProviders(<EvalsTab skill={SKILL} />, { messages: pseudoMessages() });
    await waitFor(() => expect(screen.getByTestId("eval-case-row")).toBeInTheDocument());
    await waitFor(() => expect(screen.getByRole("table")).toBeInTheDocument());
    expect(strayText(container, [/^skill-case$/, "Beta Reviewer", "Alpha Reviewer", "critical · security", /\d{1,4}[/.\-]\d{1,2}[/.\-]\d{1,4}/, "mock/reviewer", /^[—$<]/])).toEqual([]);
  });
});
